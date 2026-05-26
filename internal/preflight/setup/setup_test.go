package setup

import (
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
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
