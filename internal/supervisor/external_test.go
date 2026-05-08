package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func TestSupervisorVerdictSchemaStatusEnumMatchesValidator(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "schemas", "supervisor-verdict.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), schema.Properties["status"].Enum...)
	want := []string{"accepted", "hard_stop", "needs_revision", "needs_supervisor_review"}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("schema status enum got %#v, want %#v", got, want)
	}
	for _, status := range got {
		if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", NextWorkOrder: "work"}); err != nil && status != "needs_revision" {
			t.Fatalf("validator rejected schema status %q: %v", status, err)
		}
		if status == "needs_revision" {
			if err := ValidateVerdict(Verdict{Status: status, Summary: "ok", NextWorkOrder: "work"}); err != nil {
				t.Fatalf("validator rejected needs_revision with work order: %v", err)
			}
		}
	}
}

func TestNewExternalRequestSerializesErrorsAsStrings(t *testing.T) {
	request := NewExternalRequest(Evidence{
		ParseError:   errors.New("bad json"),
		RunError:     errors.New("executor failed"),
		DiffError:    errors.New("diff failed"),
		Diff:         "diff --git a/file b/file",
		DiffDirty:    true,
		Attempt:      2,
		AttemptsLeft: 1,
	})
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"parse_error":"bad json"`,
		`"run_error":"executor failed"`,
		`"diff_error":"diff failed"`,
		`"diff_dirty":true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request JSON missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"ParseError"`) || strings.Contains(text, `"RunError"`) {
		t.Fatalf("request leaked Go field names: %s", text)
	}
}
