package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyExecutorTerminalRouting(t *testing.T) {
	validResult := `{"status":"completed","summary":"done","files_modified":[],"acceptance_criteria":[],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	tests := []struct {
		name       string
		cli        string
		stdout     string
		runErr     error
		result     RunResult
		wantNormal bool
		wantReason string
		wantDetail func(t *testing.T, d ProviderTerminalDetail)
	}{
		{
			name:       "claude success result event",
			cli:        "claude",
			stdout:     `{"type":"result","subtype":"success","is_error":false,"session_id":"abc","result":"` + escape(validResult) + `"}`,
			wantNormal: true,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Status != "success" || d.SessionID != "abc" {
					t.Fatalf("detail = %#v", d)
				}
			},
		},
		{
			name:       "claude api error result event",
			cli:        "claude",
			stdout:     `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"abc","error":"rate limited"}`,
			wantNormal: false,
			wantReason: TerminalReasonProviderAPIError,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Message != "rate limited" {
					t.Fatalf("expected retained provider message, got %#v", d)
				}
			},
		},
		{
			name:       "claude bare result payload alone is an interruption",
			cli:        "claude",
			stdout:     validResult,
			wantNormal: false,
			wantReason: TerminalReasonMissingTerminal,
		},
		{
			name:       "claude output without a terminal is an interruption",
			cli:        "claude",
			stdout:     "not-json\nstill nothing\n",
			wantNormal: false,
			wantReason: TerminalReasonMissingTerminal,
		},
		{
			// A result event without an explicit success signal (no is_error and a
			// non-"success" subtype) fails closed instead of being read as success.
			name:       "claude ambiguous result without an explicit success signal fails closed",
			cli:        "claude",
			stdout:     `{"type":"result","subtype":"in_progress","session_id":"amb"}`,
			wantNormal: false,
			wantReason: TerminalReasonAmbiguousResult,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Status != "in_progress" || d.SessionID != "amb" {
					t.Fatalf("ambiguous result must retain diagnostic detail, got %#v", d)
				}
			},
		},
		{
			// A bare result event with no fields at all is also ambiguous, never a
			// normal terminal.
			name:       "claude result event with no signal fields fails closed",
			cli:        "claude",
			stdout:     `{"type":"result"}`,
			wantNormal: false,
			wantReason: TerminalReasonAmbiguousResult,
		},
		{
			name:       "claude api error after a success result is an interruption",
			cli:        "claude",
			stdout:     `{"type":"result","subtype":"success","is_error":false}` + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true,"error":"overloaded"}`,
			wantNormal: false,
			wantReason: TerminalReasonProviderAPIError,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Message != "overloaded" {
					t.Fatalf("mixed history must retain the api-error detail, got %#v", d)
				}
			},
		},
		{
			name:       "glm reuses the claude contract",
			cli:        "glm",
			stdout:     `{"type":"result","subtype":"success"}`,
			wantNormal: true,
		},
		{
			name:       "glm api error result event",
			cli:        "glm",
			stdout:     `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"glm-1","error":"upstream 529"}`,
			wantNormal: false,
			wantReason: TerminalReasonProviderAPIError,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Message != "upstream 529" || d.SessionID != "glm-1" {
					t.Fatalf("glm api error must retain provider detail, got %#v", d)
				}
			},
		},
		{
			name:       "codex turn.completed",
			cli:        "codex",
			stdout:     `{"type":"thread.started"}` + "\n" + `{"type":"turn.completed"}`,
			wantNormal: true,
		},
		{
			name:       "codex turn.failed with nested detail",
			cli:        "codex",
			stdout:     `{"type":"turn.failed","error":{"code":"context_length","message":"too long"}}`,
			wantNormal: false,
			wantReason: TerminalReasonProviderTurnFailed,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Code != "context_length" || d.Message != "too long" {
					t.Fatalf("detail = %#v", d)
				}
			},
		},
		{
			name:       "codex turn.failed without nested detail",
			cli:        "codex",
			stdout:     `{"type":"turn.failed"}`,
			wantNormal: false,
			wantReason: TerminalReasonProviderTurnFailed,
		},
		{
			name:       "codex turn.failed after turn.completed is an interruption",
			cli:        "codex",
			stdout:     `{"type":"turn.completed"}` + "\n" + `{"type":"turn.failed","error":{"code":"stream_error"}}`,
			wantNormal: false,
			wantReason: TerminalReasonProviderTurnFailed,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Code != "stream_error" {
					t.Fatalf("mixed history must retain the turn.failed detail, got %#v", d)
				}
			},
		},
		{
			name:       "codex bare result payload alone is an interruption",
			cli:        "codex",
			stdout:     validResult,
			wantNormal: false,
			wantReason: TerminalReasonMissingTerminal,
		},
		{
			name:       "grok EndTurn with exit code 0 is a normal terminal",
			cli:        "grok",
			stdout:     `{"text":"{}","stopReason":"EndTurn","sessionId":"s1"}`,
			result:     RunResult{ExitCode: 0},
			wantNormal: true,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.StopReason != "EndTurn" || d.SessionID != "s1" {
					t.Fatalf("detail = %#v", d)
				}
			},
		},
		{
			// The stopReason marker alone decides routing; a missing sessionId
			// (optional diagnostic) must not prevent the EndTurn normal terminal.
			name:       "grok EndTurn without sessionId is a normal terminal",
			cli:        "grok",
			stdout:     `{"text":"{}","stopReason":"EndTurn"}`,
			result:     RunResult{ExitCode: 0},
			wantNormal: true,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.StopReason != "EndTurn" || d.SessionID != "" {
					t.Fatalf("detail = %#v", d)
				}
			},
		},
		{
			name:       "grok non-EndTurn with exit code 0 is an interruption",
			cli:        "grok",
			stdout:     `{"text":"{}","stopReason":"Cancelled","sessionId":"s2"}`,
			result:     RunResult{ExitCode: 0},
			wantNormal: false,
			wantReason: TerminalReasonProviderNonEndTurn,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.StopReason != "Cancelled" {
					t.Fatalf("detail = %#v", d)
				}
			},
		},
		{
			name:       "runner start failure is an interruption regardless of output",
			cli:        "claude",
			stdout:     validResult,
			runErr:     &CommandError{Kind: CommandErrorStart, Err: fmt.Errorf("start claude: no such file")},
			wantNormal: false,
			wantReason: TerminalReasonStartFailed,
		},
		{
			name:       "runner total timeout is an interruption",
			cli:        "codex",
			runErr:     &CommandError{Kind: CommandErrorTimeout, Err: fmt.Errorf("timed out")},
			result:     RunResult{TimedOut: true},
			wantNormal: false,
			wantReason: TerminalReasonTimedOut,
		},
		{
			name:       "runner idle timeout is an interruption",
			cli:        "grok",
			runErr:     &CommandError{Kind: CommandErrorIdleTimeout, Err: fmt.Errorf("idle")},
			result:     RunResult{IdleTimedOut: true},
			wantNormal: false,
			wantReason: TerminalReasonIdleTimeout,
		},
		{
			name:       "runner kill is an interruption",
			cli:        "claude",
			runErr:     &CommandError{Kind: CommandErrorKilled, Err: fmt.Errorf("signal: killed")},
			wantNormal: false,
			wantReason: TerminalReasonKilled,
		},
		{
			// Exit-code boundary: a success marker cannot override a non-zero
			// exit, and (paired with the grok exit-0 cases above) proves the exit
			// code never decides routing on its own.
			name:       "non-zero exit with a success result is an interruption",
			cli:        "claude",
			stdout:     `{"type":"result","subtype":"success","is_error":false}`,
			runErr:     &CommandError{Kind: CommandErrorExitNonZero, Err: fmt.Errorf("exit status 7")},
			wantNormal: false,
			wantReason: TerminalReasonExitNonZero,
		},
		{
			name:       "context deadline is an interruption",
			cli:        "claude",
			runErr:     context.DeadlineExceeded,
			wantNormal: false,
			wantReason: TerminalReasonTimedOut,
		},
		{
			// A structured provider failure plus a non-zero exit: the runner
			// failure controls the routing reason, but the provider's diagnostic
			// detail is still scanned and retained.
			name:       "structured provider failure plus non-zero exit retains provider detail",
			cli:        "claude",
			stdout:     `{"type":"result","subtype":"error_during_execution","is_error":true,"error":"upstream 529"}`,
			runErr:     &CommandError{Kind: CommandErrorExitNonZero, Err: fmt.Errorf("exit status 1")},
			wantNormal: false,
			wantReason: TerminalReasonExitNonZero,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Message != "upstream 529" {
					t.Fatalf("runner failure must still retain provider detail, got %#v", d)
				}
			},
		},
		{
			// Same contract for a total timeout over a codex turn.failed.
			name:       "structured provider failure plus timeout retains provider detail",
			cli:        "codex",
			stdout:     `{"type":"turn.failed","error":{"code":"deadline","message":"exceeded"}}`,
			runErr:     &CommandError{Kind: CommandErrorTimeout, Err: fmt.Errorf("timed out")},
			result:     RunResult{TimedOut: true},
			wantNormal: false,
			wantReason: TerminalReasonTimedOut,
			wantDetail: func(t *testing.T, d ProviderTerminalDetail) {
				if d.Code != "deadline" || d.Message != "exceeded" {
					t.Fatalf("runner failure must still retain provider detail, got %#v", d)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var stdoutPath string
			if tc.stdout != "" {
				stdoutPath = filepath.Join(dir, "stdout")
				if err := os.WriteFile(stdoutPath, []byte(tc.stdout), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := ClassifyExecutorTerminal(tc.cli, stdoutPath, tc.stdout, tc.result, tc.runErr)
			if got.NormalTerminal != tc.wantNormal {
				t.Fatalf("NormalTerminal = %t; want %t (reason=%q)", got.NormalTerminal, tc.wantNormal, got.Reason)
			}
			if !tc.wantNormal && got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q; want %q", got.Reason, tc.wantReason)
			}
			if tc.wantNormal && got.Reason != "" {
				t.Fatalf("normal terminal must not carry an interruption reason, got %q", got.Reason)
			}
			if tc.wantDetail != nil {
				tc.wantDetail(t, got.Detail)
			}
		})
	}
}

func TestClassifyExecutorTerminalFallsBackToTail(t *testing.T) {
	got := ClassifyExecutorTerminal("grok", "", `{"text":"{}","stopReason":"EndTurn","sessionId":"s"}`, RunResult{}, nil)
	if !got.NormalTerminal {
		t.Fatalf("expected normal terminal from tail fallback, got %#v", got)
	}
}

func escape(s string) string {
	out := make([]rune, 0, len(s)+8)
	for _, r := range s {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
