package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Terminal classification decides whether an executor process reached a normal
// provider terminal (reviewable by the supervisor) or was interrupted (partial
// work preserved, failed task published without a supervisor verdict). Routing
// depends only on runner state plus the provider's machine-readable
// normal-terminal marker, never on the process exit code or error-text
// matching. Provider failure detail is retained for diagnosis only. Extracting
// the executor-result payload is a separate concern handled by the executor
// result parser; a bare result payload is not a substitute for a terminal
// marker here.
const (
	TerminalReasonStartFailed        = "start_failed"
	TerminalReasonTimedOut           = "timed_out"
	TerminalReasonIdleTimeout        = "idle_timeout"
	TerminalReasonKilled             = "killed"
	TerminalReasonExitNonZero        = "exit_nonzero"
	TerminalReasonProviderAPIError   = "provider_api_error"
	TerminalReasonProviderTurnFailed = "provider_turn_failed"
	TerminalReasonProviderNonEndTurn = "provider_non_end_turn"
	TerminalReasonMalformedOutput    = "malformed_provider_output"
	TerminalReasonMissingTerminal    = "missing_normal_terminal"
	TerminalReasonAmbiguousResult    = "ambiguous_provider_result"
	TerminalReasonUnknown            = "unknown"
)

// ProviderTerminalDetail carries the optional structured fields a provider
// exposes about how a turn ended. Every field is diagnostic and may be empty
// when the provider CLI omits it; none of them controls routing.
type ProviderTerminalDetail struct {
	Status     string `json:"status,omitempty"`
	Code       string `json:"code,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ExecutorTerminal is Galley's routing decision for one executor attempt.
// NormalTerminal true means "runner succeeded and the provider emitted its
// reliable normal-terminal marker". Reason is empty for a normal terminal and
// otherwise names the diagnostic interruption cause.
type ExecutorTerminal struct {
	CLI            string                 `json:"cli"`
	NormalTerminal bool                   `json:"normal_terminal"`
	Reason         string                 `json:"reason,omitempty"`
	Detail         ProviderTerminalDetail `json:"detail"`
}

// ClassifyExecutorTerminal produces the single routing decision for an executor
// attempt. A non-nil runErr (start failure, timeout, idle timeout, kill, or
// non-zero exit) is always an interruption because a normal terminal requires
// the runner to have succeeded. The runner failure controls the routing reason,
// but the provider output is still scanned so any structured diagnostic detail
// (status, code, stop reason, session ID, message) is retained for diagnosis.
// When the runner succeeded, the provider stdout is inspected for the
// provider-specific normal-terminal marker.
func ClassifyExecutorTerminal(cli, stdoutPath, stdoutTail string, result RunResult, runErr error) ExecutorTerminal {
	if runErr != nil {
		return ExecutorTerminal{
			CLI:            cli,
			NormalTerminal: false,
			Reason:         runnerFailureReason(result, runErr),
			Detail:         scanProviderDetail(cli, stdoutPath, stdoutTail),
		}
	}
	switch cli {
	case "grok":
		return grokTerminal(cli, stdoutPath, stdoutTail)
	case "codex":
		return codexTerminal(cli, stdoutPath, stdoutTail)
	default: // claude, glm, and the default executor share the Claude transport.
		return claudeTerminal(cli, stdoutPath, stdoutTail)
	}
}

// scanProviderDetail extracts whatever structured provider detail is present in
// the captured output for diagnosis only. It never changes routing and is used
// on the runner-failure path where the interruption reason is already fixed by
// runnerFailureReason.
func scanProviderDetail(cli, stdoutPath, stdoutTail string) ProviderTerminalDetail {
	switch cli {
	case "grok":
		return grokDetail(stdoutPath, stdoutTail)
	case "codex":
		return scanCodex(stdoutPath, stdoutTail).detail()
	default:
		return scanClaude(stdoutPath, stdoutTail).detail
	}
}

// runnerFailureReason classifies a runner failure through the typed sentinels
// rather than error-message substrings so a wording change cannot silently
// reclassify a timeout or kill.
func runnerFailureReason(result RunResult, err error) string {
	switch {
	case errors.Is(err, ErrIdleTimeout):
		return TerminalReasonIdleTimeout
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return TerminalReasonTimedOut
	case errors.Is(err, ErrKilled):
		return TerminalReasonKilled
	}
	var cmdErr *CommandError
	if errors.As(err, &cmdErr) {
		switch cmdErr.Kind {
		case CommandErrorStart:
			return TerminalReasonStartFailed
		case CommandErrorIdleTimeout:
			return TerminalReasonIdleTimeout
		case CommandErrorTimeout:
			return TerminalReasonTimedOut
		case CommandErrorKilled:
			return TerminalReasonKilled
		case CommandErrorExitNonZero:
			return TerminalReasonExitNonZero
		}
	}
	if result.IdleTimedOut {
		return TerminalReasonIdleTimeout
	}
	if result.TimedOut {
		return TerminalReasonTimedOut
	}
	return TerminalReasonUnknown
}

// grokTerminal routes on the Grok stopReason marker alone. The stopReason is
// the routing marker; sessionId is optional diagnostic detail that never gates
// the decision, so an "EndTurn" envelope is a normal terminal even when
// sessionId is absent. A missing result payload under EndTurn is an ordinary
// completed-result failure the supervisor still reviews. Output that is not a
// parseable envelope, or that carries no stopReason marker, has no reliable
// normal terminal and fails closed to an interruption.
func grokTerminal(cli, stdoutPath, stdoutTail string) ExecutorTerminal {
	env, ok := parseGrokRouting(stdoutPath, stdoutTail)
	if !ok || env.StopReason == "" {
		return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonMalformedOutput, Detail: env.detail()}
	}
	if env.StopReason == "EndTurn" {
		return ExecutorTerminal{CLI: cli, NormalTerminal: true, Detail: env.detail()}
	}
	return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonProviderNonEndTurn, Detail: env.detail()}
}

// grokRouting is the minimal, routing-focused view of a Grok envelope: only the
// stopReason marker gates routing, and sessionId is retained as diagnostic
// detail. It deliberately does not require sessionId (unlike ParseGrokEnvelope,
// which validates the stricter executor-result contract) so terminal
// classification never depends on optional session detail.
type grokRouting struct {
	StopReason string
	SessionID  string
}

func (g grokRouting) detail() ProviderTerminalDetail {
	return ProviderTerminalDetail{StopReason: g.StopReason, SessionID: g.SessionID}
}

func parseGrokRouting(stdoutPath, stdoutTail string) (grokRouting, bool) {
	var env struct {
		StopReason string `json:"stopReason"`
		SessionID  string `json:"sessionId"`
	}
	if err := json.Unmarshal(readProviderOutput(stdoutPath, stdoutTail), &env); err != nil {
		return grokRouting{}, false
	}
	return grokRouting{StopReason: strings.TrimSpace(env.StopReason), SessionID: env.SessionID}, true
}

// grokDetail returns any diagnostic detail available in the captured output
// without deciding routing (used on the runner-failure path).
func grokDetail(stdoutPath, stdoutTail string) ProviderTerminalDetail {
	env, ok := parseGrokRouting(stdoutPath, stdoutTail)
	if !ok {
		return ProviderTerminalDetail{}
	}
	return env.detail()
}

// claudeResultClass classifies a Claude/GLM `result` event. A normal terminal
// requires an explicit known-success signal; a result event without one is
// ambiguous rather than a success, so classification fails closed.
type claudeResultClass int

const (
	claudeResultNone claudeResultClass = iota
	claudeResultSuccess
	claudeResultError
	claudeResultAmbiguous
)

// claudeScan is the result of inspecting the complete Claude/GLM stdout
// history. class is the highest-precedence classification observed (error >
// success > ambiguous > none) and detail carries the matching diagnostic detail.
type claudeScan struct {
	class  claudeResultClass
	detail ProviderTerminalDetail
}

// scanClaude walks the full stdout history and reduces every `result` event to
// one classification. An explicit API-error result takes precedence over a
// success so a mixed history fails closed; an explicit success takes precedence
// over an ambiguous result event; an ambiguous result (a `result` event without
// an explicit success or error signal) is retained so the attempt fails closed
// rather than being accepted as a normal terminal.
func scanClaude(stdoutPath, stdoutTail string) claudeScan {
	var (
		apiError        *ProviderTerminalDetail
		successDetail   ProviderTerminalDetail
		ambiguousDetail ProviderTerminalDetail
		sawSuccess      bool
		sawAmbiguous    bool
	)
	forEachProviderLine(stdoutPath, stdoutTail, func(line string) bool {
		detail, class := claudeResultEvent(line)
		switch class {
		case claudeResultError:
			if apiError == nil {
				d := detail
				apiError = &d
			}
		case claudeResultSuccess:
			sawSuccess = true
			successDetail = detail
		case claudeResultAmbiguous:
			sawAmbiguous = true
			ambiguousDetail = detail
		}
		return true
	})
	switch {
	case apiError != nil:
		return claudeScan{class: claudeResultError, detail: *apiError}
	case sawSuccess:
		return claudeScan{class: claudeResultSuccess, detail: successDetail}
	case sawAmbiguous:
		return claudeScan{class: claudeResultAmbiguous, detail: ambiguousDetail}
	default:
		return claudeScan{class: claudeResultNone}
	}
}

// claudeTerminal requires an explicit known-success Claude/GLM result event to
// establish a normal terminal; a bare executor-result payload is never a
// substitute, and a `result` event without an explicit success or error signal
// is treated as an ambiguous interruption rather than a normal terminal.
func claudeTerminal(cli, stdoutPath, stdoutTail string) ExecutorTerminal {
	scan := scanClaude(stdoutPath, stdoutTail)
	switch scan.class {
	case claudeResultError:
		return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonProviderAPIError, Detail: scan.detail}
	case claudeResultSuccess:
		return ExecutorTerminal{CLI: cli, NormalTerminal: true, Detail: scan.detail}
	case claudeResultAmbiguous:
		return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonAmbiguousResult, Detail: scan.detail}
	default:
		return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonMissingTerminal}
	}
}

// codexScan is the result of inspecting the complete Codex stdout history: the
// nested detail of the first `turn.failed` (if any) and whether a
// `turn.completed` was seen.
type codexScan struct {
	failed    *ProviderTerminalDetail
	completed bool
}

func (c codexScan) detail() ProviderTerminalDetail {
	if c.failed != nil {
		return *c.failed
	}
	return ProviderTerminalDetail{}
}

// scanCodex walks the full stdout history so an explicit `turn.failed` anywhere
// takes precedence over a completed turn.
func scanCodex(stdoutPath, stdoutTail string) codexScan {
	var scan codexScan
	forEachProviderLine(stdoutPath, stdoutTail, func(line string) bool {
		typ, raw := codexEventType(line)
		switch typ {
		case "turn.completed":
			scan.completed = true
		case "turn.failed":
			if scan.failed == nil {
				d := codexFailureDetail(raw)
				scan.failed = &d
			}
		}
		return true
	})
	return scan
}

// codexTerminal requires a `turn.completed` event for a normal terminal. An
// explicit `turn.failed` anywhere fails closed; absent any turn terminal the
// attempt is a missing-terminal interruption; a bare executor-result payload is
// never a substitute.
func codexTerminal(cli, stdoutPath, stdoutTail string) ExecutorTerminal {
	scan := scanCodex(stdoutPath, stdoutTail)
	if scan.failed != nil {
		return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonProviderTurnFailed, Detail: *scan.failed}
	}
	if scan.completed {
		return ExecutorTerminal{CLI: cli, NormalTerminal: true}
	}
	return ExecutorTerminal{CLI: cli, NormalTerminal: false, Reason: TerminalReasonMissingTerminal}
}

// claudeResultEvent classifies a Claude/GLM output line. A normal terminal
// requires an explicit known-success signal (is_error explicitly false or a
// "success" subtype). A `result` event with an explicit error signal (is_error
// true or an "error" subtype) is an API error; a `result` event with neither
// signal is ambiguous and fails closed rather than being read as success.
func claudeResultEvent(line string) (ProviderTerminalDetail, claudeResultClass) {
	line = strings.TrimSpace(line)
	if line == "" {
		return ProviderTerminalDetail{}, claudeResultNone
	}
	var event struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		IsError   *bool  `json:"is_error"`
		SessionID string `json:"session_id"`
		Result    string `json:"result"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return ProviderTerminalDetail{}, claudeResultNone
	}
	if event.Type != "result" {
		return ProviderTerminalDetail{}, claudeResultNone
	}
	detail := ProviderTerminalDetail{Status: event.Subtype, SessionID: event.SessionID}
	switch {
	case (event.IsError != nil && *event.IsError) || isErrorSubtype(event.Subtype):
		detail.Message = firstNonEmptyString(event.Error, event.Result)
		return detail, claudeResultError
	case (event.IsError != nil && !*event.IsError) || event.Subtype == "success":
		return detail, claudeResultSuccess
	default:
		return detail, claudeResultAmbiguous
	}
}

func isErrorSubtype(subtype string) bool {
	return strings.HasPrefix(subtype, "error")
}

// codexEventType returns a JSONL event's top-level type and the raw event so a
// turn.failed event's nested error detail can be extracted for diagnosis.
func codexEventType(line string) (string, json.RawMessage) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", nil
	}
	return event.Type, json.RawMessage(line)
}

// codexFailureDetail extracts the optional nested error status/code/message a
// Codex turn.failed event may carry. Missing nested detail yields an empty
// struct without changing routing.
func codexFailureDetail(raw json.RawMessage) ProviderTerminalDetail {
	var event struct {
		Error *struct {
			Status  string `json:"status"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &event); err != nil || event.Error == nil {
		return ProviderTerminalDetail{}
	}
	return ProviderTerminalDetail{Status: event.Error.Status, Code: event.Error.Code, Message: event.Error.Message}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// readProviderOutput returns the captured stdout file contents, falling back to
// the in-memory tail when the file is unavailable.
func readProviderOutput(stdoutPath, stdoutTail string) []byte {
	if stdoutPath != "" {
		if data, err := os.ReadFile(stdoutPath); err == nil {
			return data
		}
	}
	return []byte(stdoutTail)
}

// forEachProviderLine streams the captured stdout line by line, preferring the
// on-disk file and falling back to the in-memory tail. The callback returns
// false to stop early.
func forEachProviderLine(stdoutPath, stdoutTail string, fn func(line string) bool) {
	if stdoutPath != "" {
		if file, err := os.Open(stdoutPath); err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
			for scanner.Scan() {
				if !fn(scanner.Text()) {
					return
				}
			}
			if scanner.Err() == nil {
				return
			}
		}
	}
	for _, line := range strings.Split(stdoutTail, "\n") {
		if !fn(line) {
			return
		}
	}
}
