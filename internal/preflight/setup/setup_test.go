package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func TestEnforceLearnedPlanContractRequiresReadyEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  *Result
		want string
	}{
		{
			name: "missing successful commands",
			res: &Result{
				Status: StatusReady,
				Commands: []CommandAttempt{{
					Run:      "go test ./...",
					Source:   SourceDiscovered,
					ExitCode: 0,
				}},
				ReadinessEvidence: "tests passed",
				Source:            SourceDiscovered,
			},
			want: "no successful_commands",
		},
		{
			name: "missing successful attempt",
			res: &Result{
				Status: StatusReady,
				SuccessfulCommands: []profile.SetupCommand{{
					Run: "go test ./...",
				}},
				Commands: []CommandAttempt{{
					Run:      "go test ./...",
					Source:   SourceReadinessCheck,
					ExitCode: 0,
				}},
				ReadinessEvidence: "tests passed",
				Source:            SourceDiscovered,
			},
			want: "no successful setup command attempt",
		},
		{
			name: "missing readiness",
			res: &Result{
				Status: StatusReady,
				SuccessfulCommands: []profile.SetupCommand{{
					Run: "go test ./...",
				}},
				Commands: []CommandAttempt{{
					Run:      "go test ./...",
					Source:   SourceDiscovered,
					ExitCode: 0,
				}},
				Source: SourceDiscovered,
			},
			want: "no readiness_evidence",
		},
		{
			name: "invalid source",
			res: &Result{
				Status: StatusReady,
				SuccessfulCommands: []profile.SetupCommand{{
					Run: "go test ./...",
				}},
				Commands: []CommandAttempt{{
					Run:      "go test ./...",
					Source:   SourceDiscovered,
					ExitCode: 0,
				}},
				ReadinessEvidence: "tests passed",
				Source:            "invalid",
			},
			want: "invalid source",
		},
		{
			name: "missing source",
			res: &Result{
				Status:             StatusReady,
				SuccessfulCommands: []profile.SetupCommand{{Run: "go test ./..."}},
				Commands:           []CommandAttempt{{Run: "go test ./...", Source: SourceDiscovered, ExitCode: 0}},
				ReadinessEvidence:  "tests passed",
			},
			want: "no source",
		},
		{
			name: "successful command exited non-zero",
			res: &Result{
				Status:             StatusReady,
				SuccessfulCommands: []profile.SetupCommand{{Run: "go test ./..."}},
				Commands:           []CommandAttempt{{Run: "go test ./...", Source: SourceDiscovered, ExitCode: 1}},
				ReadinessEvidence:  "tests passed",
				Source:             SourceDiscovered,
			},
			want: "no successful setup command attempt",
		},
		{
			name: "readiness check is not a plan source",
			res: &Result{
				Status:             StatusReady,
				SuccessfulCommands: []profile.SetupCommand{{Run: "go test ./..."}},
				Commands:           []CommandAttempt{{Run: "go test ./...", Source: SourceDiscovered, ExitCode: 0}},
				ReadinessEvidence:  "tests passed",
				Source:             SourceReadinessCheck,
			},
			want: "invalid source",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := EnforceLearnedPlanContract(tc.res)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error got %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestEnforceLearnedPlanContractAcceptsReadyResult(t *testing.T) {
	t.Parallel()
	res := &Result{
		Status: StatusReady,
		SuccessfulCommands: []profile.SetupCommand{{
			Run: "go test ./...",
		}},
		Commands: []CommandAttempt{{
			Run:      "go test ./...",
			Source:   SourceDiscovered,
			ExitCode: 0,
		}},
		ReadinessEvidence: "tests passed",
		Source:            SourceDiscovered,
	}
	if err := EnforceLearnedPlanContract(res); err != nil {
		t.Fatal(err)
	}
}

func TestRunExecutorRequestCarriesEnvironmentCommandsAndDiscoveredSignals(t *testing.T) {
	t.Parallel()
	work, runDir := t.TempDir(), t.TempDir()
	for _, name := range []string{"package.json", "go.mod"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commands := map[string]string{"test_unit": "go test ./...", "install": "npm ci"}
	// A missing Grok binary stops after the request is written to the prompt file.
	_, _ = RunExecutor(context.Background(), Options{
		Task:     task.Task{ID: "setup-request", Executor: task.Executor{CLI: "grok"}},
		WorkDir:  work,
		RunDir:   runDir,
		Profiles: profile.Bundle{Environment: &profile.Environment{ID: "request", Commands: commands}},
		GrokBin:  filepath.Join(t.TempDir(), "missing-grok"),
	})
	prompt, err := os.ReadFile(filepath.Join(runDir, "grok.setup.prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, workOrder, ok := strings.Cut(string(prompt), "# Work Order\n\n")
	if !ok {
		t.Fatalf("setup prompt has no work order:\n%s", prompt)
	}
	var request struct {
		Environment struct {
			Commands map[string]string `json:"commands"`
		} `json:"environment"`
		RepositorySignals []string `json:"repository_signals"`
	}
	if err := json.Unmarshal([]byte(workOrder), &request); err != nil {
		t.Fatalf("decode setup request: %v\n%s", err, workOrder)
	}
	if !reflect.DeepEqual(request.Environment.Commands, commands) {
		t.Fatalf("request environment.commands = %#v, want %#v", request.Environment.Commands, commands)
	}
	if want := []string{"package.json", "go.mod"}; !reflect.DeepEqual(request.RepositorySignals, want) {
		t.Fatalf("request repository_signals = %#v, want %#v", request.RepositorySignals, want)
	}
}

func TestParseResultTextExtractsAndNormalizesResult(t *testing.T) {
	t.Parallel()
	text := `prefix {"status":"ready","commands":[{"run":"  go test ./...  ","source":"discovered","exit_code":0,"stdout_excerpt":"  ok  "}],"successful_commands":[{"run":"go test ./..."}],"readiness_evidence":"ready","source":"discovered"} suffix`
	res, ok := parseResultText(text)
	if !ok {
		t.Fatal("expected parseResultText to find result JSON")
	}
	if res.Status != StatusReady || len(res.Commands) != 1 {
		t.Fatalf("unexpected result: %#v", res)
	}
	if res.Commands[0].StdoutExcerpt != "ok" {
		t.Fatalf("stdout excerpt was not normalized: %#v", res.Commands[0])
	}
}

func TestParseResultTextRejectsMissingCommands(t *testing.T) {
	t.Parallel()
	if res, ok := parseResultText(`{"status":"ready"}`); ok || res != nil {
		t.Fatalf("parseResultText got (%#v, %t), want nil false", res, ok)
	}
}

func TestNormalizeResultBoundsLargeFields(t *testing.T) {
	t.Parallel()
	res := &Result{
		Status: StatusReady,
		Commands: []CommandAttempt{{
			Run:           strings.Repeat("r", profile.MaxSetupCommandRunLength+10),
			Why:           strings.Repeat("w", profile.MaxSetupCommandWhyLength+10),
			Source:        SourceDiscovered,
			ExitCode:      0,
			StdoutExcerpt: "  " + strings.Repeat("o", maxResultExcerptLength+10) + "  ",
			StderrExcerpt: strings.Repeat("e", maxResultExcerptLength+10),
		}},
		SuccessfulCommands: make([]profile.SetupCommand, maxResultCommands+1),
		InspectedFiles:     make([]string, maxResultFiles+1),
		ReadinessEvidence:  strings.Repeat("x", maxResultTextLength+10),
		RepairGuidance:     strings.Repeat("g", maxResultTextLength+10),
		Error:              strings.Repeat("e", maxResultTextLength+10),
	}
	normalizeResult(res)
	if len(res.SuccessfulCommands) != maxResultCommands {
		t.Fatalf("successful commands length got %d", len(res.SuccessfulCommands))
	}
	if len(res.InspectedFiles) != maxResultFiles {
		t.Fatalf("inspected files length got %d", len(res.InspectedFiles))
	}
	if len(res.Commands[0].Run) != profile.MaxSetupCommandRunLength {
		t.Fatalf("run length got %d", len(res.Commands[0].Run))
	}
	if strings.HasPrefix(res.Commands[0].StdoutExcerpt, " ") || len(res.Commands[0].StdoutExcerpt) != maxResultExcerptLength {
		t.Fatalf("stdout excerpt not normalized: %q", res.Commands[0].StdoutExcerpt)
	}
}

func TestTruncateStringKeepsValidUTF8(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"cuts inside multi-byte rune": strings.Repeat("あ", 200),
		"leading orphan byte":         "\x81" + strings.Repeat("a", 200),
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			got := truncateString(in, 101)
			if !utf8.ValidString(got) {
				t.Fatalf("truncated string is not valid UTF-8: %q", got)
			}
			if len(got) > 101 {
				t.Fatalf("truncated string exceeds budget: len=%d", len(got))
			}
		})
	}
}

func TestSetupPlansEqualComparesRunOnly(t *testing.T) {
	t.Parallel()
	a := profile.SetupPlan{Commands: []profile.SetupCommand{
		{Run: " go test ./... ", Why: "fast"},
	}}
	b := profile.SetupPlan{Commands: []profile.SetupCommand{
		{Run: "go test ./...", Why: "different"},
	}}
	if !setupPlansEqual(a, b) {
		t.Fatal("setupPlansEqual should compare normalized run commands")
	}
	b.Commands = append(b.Commands, profile.SetupCommand{Run: "npm test"})
	if setupPlansEqual(a, b) {
		t.Fatal("setupPlansEqual should reject different command counts")
	}
}
