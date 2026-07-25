package runner

import (
	"errors"
	"github.com/shinpr/galley/internal/proc"
	"testing"
)

// validExecutorResultLine is a minimal executor result that passes Validate; a
// well-formed result alone is not a normal terminal without a provider terminal event.
const validExecutorResultLine = `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`

func TestExecutorTerminalDecision(t *testing.T) {
	startErr := &proc.CommandError{Kind: proc.CommandErrorStart, Err: errors.New("start claude: no such file")}
	timeoutErr := &proc.CommandError{Kind: proc.CommandErrorTimeout, Err: errors.New("timed out")}
	idleErr := &proc.CommandError{Kind: proc.CommandErrorIdleTimeout, Err: errors.New("idle")}
	killErr := &proc.CommandError{Kind: proc.CommandErrorKilled, Err: errors.New("killed")}
	exitErr := &proc.CommandError{Kind: proc.CommandErrorExitNonZero, Err: errors.New("exit status 7")}

	tests := []struct {
		name       string
		got        ExecutorTerminal
		wantNormal bool
		wantReason string
	}{
		{
			name:       "claude success result event",
			got:        ClaudeTerminal("claude", []byte(`{"type":"system"}`+"\n"+`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s1"}`), nil),
			wantNormal: true,
			wantReason: TerminalReasonClaudeResultSuccess,
		},
		{
			name:       "claude API-error result event",
			got:        ClaudeTerminal("claude", []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s2"}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonClaudeResultError,
		},
		{
			// A result event whose subtype is neither "success" nor an error is
			// not a reliable normal terminal, so it must not reach Supervisor.
			name:       "claude unknown non-success subtype",
			got:        ClaudeTerminal("claude", []byte(`{"type":"result","subtype":"other","is_error":false}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonClaudeResultError,
		},
		{
			name:       "claude missing subtype",
			got:        ClaudeTerminal("claude", []byte(`{"type":"result","is_error":false}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonClaudeResultError,
		},
		{
			name:       "claude structured result without terminal",
			got:        ClaudeTerminal("claude", []byte(validExecutorResultLine), nil),
			wantNormal: false,
			wantReason: TerminalReasonNoNormalTerminal,
		},
		{
			name:       "claude no reliable terminal",
			got:        ClaudeTerminal("claude", []byte("not-json output\n"), nil),
			wantNormal: false,
			wantReason: TerminalReasonNoNormalTerminal,
		},
		{
			name:       "claude start failure",
			got:        ClaudeTerminal("claude", []byte(validExecutorResultLine), startErr),
			wantNormal: false,
			wantReason: TerminalReasonRunnerStartFailed,
		},
		{
			name:       "claude total timeout",
			got:        ClaudeTerminal("claude", nil, timeoutErr),
			wantNormal: false,
			wantReason: TerminalReasonRunnerTimeout,
		},
		{
			name:       "claude idle timeout",
			got:        ClaudeTerminal("claude", nil, idleErr),
			wantNormal: false,
			wantReason: TerminalReasonRunnerIdleTimeout,
		},
		{
			name:       "claude killed",
			got:        ClaudeTerminal("claude", nil, killErr),
			wantNormal: false,
			wantReason: TerminalReasonRunnerKilled,
		},
		{
			name:       "claude non-zero exit without terminal",
			got:        ClaudeTerminal("claude", []byte("partial\n"), exitErr),
			wantNormal: false,
			wantReason: TerminalReasonRunnerExitNonZero,
		},
		{
			name:       "codex turn.completed",
			got:        CodexTerminal([]byte(`{"type":"turn.completed","usage":{}}`), nil),
			wantNormal: true,
			wantReason: TerminalReasonCodexTurnCompleted,
		},
		{
			name:       "codex turn.failed with detail",
			got:        CodexTerminal([]byte(`{"type":"turn.failed","error":{"message":"model overloaded","code":"rate_limit"}}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonCodexTurnFailed,
		},
		{
			name:       "codex turn.failed without detail",
			got:        CodexTerminal([]byte(`{"type":"turn.failed"}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonCodexTurnFailed,
		},
		{
			// An unparseable error field keeps turn.failed routing with a generic
			// reason rather than collapsing to "no terminal".
			name:       "codex turn.failed with unparseable detail",
			got:        CodexTerminal([]byte(`{"type":"turn.failed","error":"boom"}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonCodexTurnFailed,
		},
		{
			name:       "codex last message without turn.completed",
			got:        CodexTerminal([]byte(`{"type":"item.completed"}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonNoNormalTerminal,
		},
		{
			name:       "grok EndTurn",
			got:        GrokTerminal([]byte(`{"text":"hi","stopReason":"EndTurn","sessionId":"g1"}`), nil),
			wantNormal: true,
			wantReason: TerminalReasonGrokEndTurn,
		},
		{
			// EndTurn is a normal terminal even without the optional sessionId or
			// result payload; those are diagnostic detail and never gate routing.
			name:       "grok EndTurn without session or payload",
			got:        GrokTerminal([]byte(`{"stopReason":"EndTurn"}`), nil),
			wantNormal: true,
			wantReason: TerminalReasonGrokEndTurn,
		},
		{
			name:       "grok non-EndTurn with exit code zero",
			got:        GrokTerminal([]byte(`{"text":"partial","stopReason":"MaxTokens","sessionId":"g2"}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonGrokNonEndTurn,
		},
		{
			name:       "grok malformed envelope",
			got:        GrokTerminal([]byte("not json"), nil),
			wantNormal: false,
			wantReason: TerminalReasonMalformedTerminal,
		},
		{
			name:       "grok missing stop reason",
			got:        GrokTerminal([]byte(`{"text":"hi","sessionId":"g3"}`), nil),
			wantNormal: false,
			wantReason: TerminalReasonMalformedTerminal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Normal != tc.wantNormal {
				t.Fatalf("Normal = %t, want %t (%+v)", tc.got.Normal, tc.wantNormal, tc.got)
			}
			if tc.got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q (%+v)", tc.got.Reason, tc.wantReason, tc.got)
			}
			if tc.got.Interrupted() == tc.got.Normal {
				t.Fatalf("Interrupted() must be the inverse of Normal (%+v)", tc.got)
			}
		})
	}
}

// TestClaudeTerminalPreservesProviderIdentity asserts that GLM, which shares
// Claude's transport, keeps its own provider identity in terminal evidence.
func TestClaudeTerminalPreservesProviderIdentity(t *testing.T) {
	glm := ClaudeTerminal("glm", []byte(`{"type":"result","subtype":"success","is_error":false}`), nil)
	if glm.Provider != "glm" {
		t.Fatalf("Provider = %q, want glm (%+v)", glm.Provider, glm)
	}
	glmInterrupt := ClaudeTerminal("glm", nil, &proc.CommandError{Kind: proc.CommandErrorTimeout, Err: errors.New("t")})
	if glmInterrupt.Provider != "glm" {
		t.Fatalf("interrupted Provider = %q, want glm (%+v)", glmInterrupt.Provider, glmInterrupt)
	}
}

func TestExecutorTerminalRetainsProviderDetailOnNonZeroExit(t *testing.T) {
	exitErr := &proc.CommandError{Kind: proc.CommandErrorExitNonZero, Err: errors.New("exit status 1")}

	claude := ClaudeTerminal("claude", []byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":529,"terminal_reason":"api_error","stop_reason":"stop_sequence","session_id":"s1","result":"api overloaded"}`), exitErr)
	if claude.Normal {
		t.Fatalf("claude non-zero exit must stay interrupted: %+v", claude)
	}
	if claude.Reason != TerminalReasonClaudeResultError {
		t.Fatalf("claude reason = %q, want %q", claude.Reason, TerminalReasonClaudeResultError)
	}
	if claude.RunError == "" || claude.SessionID != "s1" || claude.Status != "api_error" || claude.Code != "529" || claude.StopReason != "stop_sequence" || claude.Message != "api overloaded" {
		t.Fatalf("claude provider detail not retained alongside run error: %+v", claude)
	}

	codex := CodexTerminal([]byte(`{"type":"turn.failed","error":{"message":"model overloaded","code":"rate_limit"}}`), exitErr)
	if codex.Normal || codex.Reason != TerminalReasonCodexTurnFailed {
		t.Fatalf("codex non-zero exit must stay codex_turn_failed: %+v", codex)
	}
	if codex.RunError == "" || codex.Code != "rate_limit" || codex.Message != "model overloaded" {
		t.Fatalf("codex provider detail not retained alongside run error: %+v", codex)
	}

	grok := GrokTerminal([]byte(`{"text":"partial","stopReason":"MaxTokens","sessionId":"g1"}`), exitErr)
	if grok.Normal || grok.Reason != TerminalReasonGrokNonEndTurn {
		t.Fatalf("grok non-zero exit must stay grok_non_end_turn: %+v", grok)
	}
	if grok.RunError == "" || grok.StopReason != "MaxTokens" || grok.SessionID != "g1" {
		t.Fatalf("grok provider detail not retained alongside run error: %+v", grok)
	}
}

func TestExecutorTerminalRejectsConflictingStreams(t *testing.T) {
	claude := ClaudeTerminal("claude", []byte(
		`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s1"}`+"\n"+
			`{"type":"result","subtype":"success","is_error":false,"session_id":"s2"}`), nil)
	if claude.Normal || claude.Reason != TerminalReasonClaudeResultError {
		t.Fatalf("claude error-then-success must stay interrupted: %+v", claude)
	}

	codex := CodexTerminal([]byte(
		`{"type":"turn.failed","error":{"message":"boom","code":"x"}}`+"\n"+
			`{"type":"turn.completed"}`), nil)
	if codex.Normal || codex.Reason != TerminalReasonCodexTurnFailed {
		t.Fatalf("codex turn.failed-then-completed must stay interrupted: %+v", codex)
	}
}
