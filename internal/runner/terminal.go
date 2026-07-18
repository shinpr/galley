package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ExecutorTerminal is Galley's routing decision for one executor process exit.
//
// Normal reports whether the runner itself succeeded and the provider emitted a
// reliable normal terminal. When Normal is false the attempt was interrupted
// and must bypass Supervisor review. Reason is a stable machine code used for
// routing and diagnosis; the remaining provider detail fields are retained for
// diagnosis only and never determine routing.
type ExecutorTerminal struct {
	Normal     bool   `json:"normal"`
	Reason     string `json:"reason"`
	Provider   string `json:"provider,omitempty"`
	Status     string `json:"status,omitempty"`
	Code       string `json:"code,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Message    string `json:"message,omitempty"`
	RunError   string `json:"run_error,omitempty"`
}

// Interrupted reports whether the attempt was interrupted rather than reaching a
// normal provider terminal.
func (t ExecutorTerminal) Interrupted() bool { return !t.Normal }

// Terminal reason codes. Normal terminals establish that Supervisor may review
// the attempt; interruption reasons establish that it must not.
const (
	// Normal terminals.
	TerminalReasonClaudeResultSuccess = "claude_result_success"
	TerminalReasonCodexTurnCompleted  = "codex_turn_completed"
	TerminalReasonGrokEndTurn         = "grok_end_turn"

	// Provider interruptions (runner succeeded, provider reported a non-normal
	// terminal or produced no reliable one).
	TerminalReasonClaudeResultError = "claude_result_error"
	TerminalReasonCodexTurnFailed   = "codex_turn_failed"
	TerminalReasonGrokNonEndTurn    = "grok_non_end_turn"
	TerminalReasonMalformedTerminal = "malformed_provider_terminal"
	TerminalReasonNoNormalTerminal  = "no_normal_terminal"

	// Runner interruptions (the executor process never produced a reliable
	// terminal).
	TerminalReasonRunnerStartFailed = "runner_start_failed"
	TerminalReasonRunnerTimeout     = "runner_timeout"
	TerminalReasonRunnerIdleTimeout = "runner_idle_timeout"
	TerminalReasonRunnerKilled      = "runner_killed"
	TerminalReasonRunnerExitNonZero = "runner_exit_nonzero"
)

func runnerInterruption(provider string, runErr error) ExecutorTerminal {
	t := ExecutorTerminal{Provider: provider, RunError: runErr.Error()}
	switch {
	case errors.Is(runErr, ErrIdleTimeout):
		t.Reason = TerminalReasonRunnerIdleTimeout
	case errors.Is(runErr, ErrTimeout):
		t.Reason = TerminalReasonRunnerTimeout
	case errors.Is(runErr, ErrKilled):
		t.Reason = TerminalReasonRunnerKilled
	default:
		var cmdErr *CommandError
		if errors.As(runErr, &cmdErr) && cmdErr.Kind == CommandErrorStart {
			t.Reason = TerminalReasonRunnerStartFailed
		} else {
			t.Reason = TerminalReasonRunnerExitNonZero
		}
	}
	return t
}

// mergeRunnerInterruption keeps runner-failure interruption routing while
// retaining any provider terminal detail parsed from the same exit for
// diagnosis. A provider success or an absent/malformed terminal never flips
// routing or masks the runner error reason.
func mergeRunnerInterruption(runnerTerm, providerTerm ExecutorTerminal) ExecutorTerminal {
	switch providerTerm.Reason {
	case "", TerminalReasonNoNormalTerminal, TerminalReasonMalformedTerminal:
		return runnerTerm
	}
	runnerTerm.Status = providerTerm.Status
	runnerTerm.Code = providerTerm.Code
	runnerTerm.StopReason = providerTerm.StopReason
	runnerTerm.SessionID = providerTerm.SessionID
	if providerTerm.Message != "" {
		runnerTerm.Message = providerTerm.Message
	}
	if !providerTerm.Normal {
		runnerTerm.Reason = providerTerm.Reason
	}
	return runnerTerm
}

// ClaudeTerminal classifies a Claude (or GLM, which shares Claude's transport)
// executor exit from its stream-json stdout and runner error. provider carries
// the actual executor identity ("claude" or "glm") so interruption evidence
// names the executor that ran rather than the shared transport.
func ClaudeTerminal(provider string, stdout []byte, runErr error) ExecutorTerminal {
	if runErr != nil {
		return mergeRunnerInterruption(runnerInterruption(provider, runErr), scanClaudeStream(provider, stdout))
	}
	return scanClaudeStream(provider, stdout)
}

type claudeResultEvent struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	IsError        bool            `json:"is_error"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`
	TerminalReason string          `json:"terminal_reason"`
	StopReason     string          `json:"stop_reason"`
	Result         json.RawMessage `json:"result"`
	SessionID      string          `json:"session_id"`
}

func scanClaudeStream(provider string, stdout []byte) ExecutorTerminal {
	var success *claudeResultEvent
	var failure *claudeResultEvent
	forEachLine(stdout, func(line string) {
		var ev claudeResultEvent
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.Type == "result" {
			captured := ev
			if !captured.IsError && captured.Subtype == "success" {
				success = &captured
			} else {
				failure = &captured
			}
		}
	})
	// An explicit failure result wins over any later success event, so a normal
	// terminal never masks an earlier provider failure; a missing or unknown
	// subtype also stays an interruption so unreliable terminals never reach
	// Supervisor.
	if failure != nil {
		status := failure.TerminalReason
		if status == "" {
			status = failure.Subtype
		}
		return ExecutorTerminal{
			Provider:   provider,
			Reason:     TerminalReasonClaudeResultError,
			Status:     status,
			Code:       claudeAPIErrorStatus(failure.APIErrorStatus),
			StopReason: failure.StopReason,
			SessionID:  failure.SessionID,
			Message:    claudeResultMessage(failure.Result),
		}
	}
	if success != nil {
		return ExecutorTerminal{Provider: provider, Normal: true, Reason: TerminalReasonClaudeResultSuccess, Status: success.Subtype, SessionID: success.SessionID}
	}
	return ExecutorTerminal{Provider: provider, Reason: TerminalReasonNoNormalTerminal}
}

func claudeAPIErrorStatus(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return text
	}
	return string(trimmed)
}

func claudeResultMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

// CodexTerminal classifies a Codex executor exit from its JSONL stdout and the
// runner error. Only a `turn.completed` event establishes normal completion;
// any other stream, including a final message without that event, is an
// interruption.
func CodexTerminal(stdout []byte, runErr error) ExecutorTerminal {
	if runErr != nil {
		return mergeRunnerInterruption(runnerInterruption("codex", runErr), scanCodexStream(stdout))
	}
	return scanCodexStream(stdout)
}

func scanCodexStream(stdout []byte) ExecutorTerminal {
	var failedLine string
	var sawFailed, sawCompleted bool
	forEachLine(stdout, func(line string) {
		var head codexTypeEvent
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			return
		}
		switch head.Type {
		case "turn.failed":
			sawFailed = true
			failedLine = line
		case "turn.completed":
			sawCompleted = true
		}
	})
	// A `turn.failed` keeps failure routing even when a later `turn.completed`
	// appears, so a normal terminal never masks an earlier provider failure.
	if sawFailed {
		t := ExecutorTerminal{Provider: "codex", Reason: TerminalReasonCodexTurnFailed}
		// Decode nested detail best-effort: a missing or unparseable error field
		// keeps turn.failed routing with a generic reason.
		var detailed codexTerminalEvent
		if err := json.Unmarshal([]byte(failedLine), &detailed); err == nil && detailed.Error != nil {
			t.Code = detailed.Error.Code
			t.Message = detailed.Error.Message
		}
		return t
	}
	if sawCompleted {
		return ExecutorTerminal{Normal: true, Provider: "codex", Reason: TerminalReasonCodexTurnCompleted}
	}
	return ExecutorTerminal{Provider: "codex", Reason: TerminalReasonNoNormalTerminal}
}

type codexTypeEvent struct {
	Type string `json:"type"`
}

type codexTerminalEvent struct {
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// GrokTerminal classifies a Grok executor exit from its JSON envelope and the
// runner error. The normal terminal is a parseable envelope whose stopReason is
// `EndTurn`; the session ID and result payload are optional diagnostic detail
// and never gate routing. Every other stopReason or an unparseable envelope is
// an interruption.
func GrokTerminal(data []byte, runErr error) ExecutorTerminal {
	if runErr != nil {
		return mergeRunnerInterruption(runnerInterruption("grok", runErr), scanGrokEnvelope(data))
	}
	return scanGrokEnvelope(data)
}

func scanGrokEnvelope(data []byte) ExecutorTerminal {
	var envelope grokTerminalEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ExecutorTerminal{Provider: "grok", Reason: TerminalReasonMalformedTerminal, Message: err.Error()}
	}
	if strings.TrimSpace(envelope.StopReason) == "" {
		return ExecutorTerminal{Provider: "grok", Reason: TerminalReasonMalformedTerminal, Message: "grok envelope has no stopReason"}
	}
	t := ExecutorTerminal{Provider: "grok", StopReason: envelope.StopReason, SessionID: envelope.SessionID}
	if envelope.StopReason != "EndTurn" {
		t.Reason = TerminalReasonGrokNonEndTurn
		return t
	}
	t.Normal = true
	t.Reason = TerminalReasonGrokEndTurn
	return t
}

// grokTerminalEnvelope decodes only the fields the terminal decision reads. It
// is deliberately more lenient than ParseGrokEnvelope, which also enforces the
// executor-result contract (sessionId and a result payload) unrelated to
// routing.
type grokTerminalEnvelope struct {
	StopReason string `json:"stopReason"`
	SessionID  string `json:"sessionId"`
}

// forEachLine calls fn for every non-empty trimmed line. It uses a growing
// reader rather than bufio.Scanner so provider result lines that embed large
// JSON payloads are not silently truncated at the scanner token limit.
func forEachLine(data []byte, fn func(line string)) {
	reader := bufio.NewReader(bytes.NewReader(data))
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			fn(trimmed)
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}
