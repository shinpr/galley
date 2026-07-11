package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestResolveExecutorResultIncludesCodexLastMessageParseError(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.jsonl")
	lastMessagePath := filepath.Join(dir, "codex-last-message.txt")
	if err := os.WriteFile(stdoutPath, []byte(`{"event":"not-a-result"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lastMessagePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := resolveExecutorResult("codex", stdoutPath, `{"event":"not-a-result"}`, lastMessagePath)
	if err == nil {
		t.Fatal("expected parse error")
	}
	got := err.Error()
	for _, want := range []string{
		"codex last-message parse failed",
		"stdout file parse failed",
		"stdout tail parse failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error missing %q:\n%s", want, got)
		}
	}
}

func TestCodexParseFailureEvidenceUsesProviderSpecificVocabulary(t *testing.T) {
	t.Parallel()
	loaded := task.Task{Executor: task.Executor{CLI: "codex"}}
	mergeAttemptEvidence(&loaded, attemptOutcome{ParseErr: errors.New("invalid output")}, "run", "/work", "/attempt")
	if len(loaded.Risks) != 1 {
		t.Fatalf("risks = %#v", loaded.Risks)
	}
	risk := loaded.Risks[0]
	if !strings.HasPrefix(risk.ID, "executor-result-parse-") || !strings.Contains(risk.Mitigation, "codex executor") || strings.Contains(risk.Mitigation, "Claude") {
		t.Fatalf("risk = %#v", risk)
	}
}
