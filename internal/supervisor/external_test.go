package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunExternal(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "supervisor")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"status\":\"accepted\",\"summary\":\"external ok\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	verdict, err := RunExternal(context.Background(), ExternalOptions{Argv: []string{command}}, Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Status != "accepted" {
		t.Fatalf("verdict got %#v", verdict)
	}
}

func TestValidateVerdictRejectsNeedsRevisionWithoutWorkOrder(t *testing.T) {
	err := ValidateVerdict(Verdict{Status: "needs_revision", Summary: "gaps"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
