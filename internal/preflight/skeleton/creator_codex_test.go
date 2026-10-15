package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

// fakeCodexCreator runs fileWrites, then writes manifest to the
// --output-last-message file as the real Codex CLI does.
func fakeCodexCreator(t *testing.T, manifest, fileWrites string) string {
	t.Helper()
	return writeFakeCommand(t, "codex", `out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
`+fileWrites+`
printf '%s\n' '`+manifest+`' > "$out"
`)
}

// codexCreatorManifest returns a raw acceptance skeleton manifest JSON object,
// the shape the Codex CLI writes verbatim into the --output-last-message file.
func codexCreatorManifest(outputs string) string {
	return `{"outputs":` + outputs + `,"no_skeletons":[]}`
}

func codexPreflightTask(acIDs ...string) task.Task {
	tk := preflightTestTask(acIDs...)
	tk.Executor.CLI = "codex"
	return tk
}

// readCreatorCommandPlan loads the persisted preflight creator command plan.
func readCreatorCommandPlan(t *testing.T, runDir string) creatorCommandPlan {
	t.Helper()
	var plan creatorCommandPlan
	data, err := os.ReadFile(filepath.Join(runDir, "preflight_creator_command_plan.json"))
	if err != nil {
		t.Fatalf("read command plan: %v", err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode command plan: %v", err)
	}
	return plan
}

type creatorCommandPlan struct {
	Argv  []string `json:"argv"`
	Stdin string   `json:"stdin"`
}

// TestAcceptanceSkeletonPreflightCodexProviderHappyPath proves a Codex task captures
// the creator manifest from the last-message file using the Codex creator prompt.
func TestAcceptanceSkeletonPreflightCodexProviderHappyPath(t *testing.T) {
	manifest := codexCreatorManifest(`[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this test before acceptance","implementation_required":true}]`)
	codexBin := fakeCodexCreator(t, manifest, `mkdir -p internal/foo
printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go`)

	res, runDir, err := runPreflightWithOptions(t, codexPreflightTask("AC1"), Options{CodexBin: codexBin})
	if err != nil {
		t.Fatalf("preflight error: %v", err)
	}
	if res == nil || res.Status != "completed" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Path != "internal/foo/foo_test.go" {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	if len(res.Baseline.SkeletonHashes) != 1 {
		t.Fatalf("baseline = %+v", res.Baseline)
	}

	plan := readCreatorCommandPlan(t, runDir)
	// The Codex provider uses the Codex-tuned acceptance skeleton prompt,
	// delivered through the combined stdin envelope (AC7).
	if !strings.Contains(plan.Stdin, "running inside Codex") {
		t.Fatalf("codex creator did not use the Codex skeleton creator prompt; stdin head: %.200s", plan.Stdin)
	}
	if strings.Contains(plan.Stdin, "running inside Claude Code") {
		t.Fatalf("codex creator used the Claude skeleton creator prompt")
	}
}

// TestAcceptanceSkeletonPreflightCodexFallsBackToStdout proves a missing
// last-message capture still recovers the manifest from Codex JSON stdout.
func TestAcceptanceSkeletonPreflightCodexFallsBackToStdout(t *testing.T) {
	manifest := codexCreatorManifest(`[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"integration","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this test before acceptance","implementation_required":true}]`)
	codexBin := writeFakeCommand(t, "codex", `while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
mkdir -p internal/foo
printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go
printf '%s\n' '{"event":"assistant_message","message":'`+strconv.Quote(manifest)+`'}'
`)

	res, runDir, err := runPreflightWithOptions(t, codexPreflightTask("AC1"), Options{CodexBin: codexBin})
	if err != nil {
		t.Fatalf("preflight error: %v", err)
	}
	if res == nil || res.Status != "completed" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Path != "internal/foo/foo_test.go" {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "codex.last-message.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("last-message capture should be absent to exercise stdout fallback, stat err = %v", statErr)
	}
}

// TestAcceptanceSkeletonPreflightPropagatesModelAndEffort proves the creator
// command plan carries the task executor model and effort for each provider.
func TestAcceptanceSkeletonPreflightPropagatesModelAndEffort(t *testing.T) {
	claudeOutputs := `[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`
	codexOutputs := `[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 behavior","integration_point":"executor completes this test","implementation_required":true}]`
	fileWrite := `mkdir -p internal/foo
printf 'package foo_test\n' > internal/foo/foo_test.go`

	for _, tc := range []struct {
		name      string
		cli       string
		model     string
		effort    string
		opts      Options
		wantInSeq [][2]string
	}{
		{
			name:   "claude",
			cli:    "claude",
			model:  "opus",
			effort: "high",
			opts:   Options{ClaudeBin: fakeCreator(t, resultManifest(claudeOutputs), fileWrite)},
			// Claude exposes --model and --effort flags directly.
			wantInSeq: [][2]string{{"--model", "opus"}, {"--effort", "high"}},
		},
		{
			name:   "codex",
			cli:    "codex",
			model:  "gpt-5-codex",
			effort: "high",
			opts:   Options{CodexBin: fakeCodexCreator(t, codexCreatorManifest(codexOutputs), fileWrite)},
			// Codex exposes --model and the -c model_reasoning_effort override.
			wantInSeq: [][2]string{{"--model", "gpt-5-codex"}, {"-c", `model_reasoning_effort="high"`}},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tk := preflightTestTask("AC1")
			tk.Executor.CLI = tc.cli
			tk.Executor.Model = tc.model
			tk.Executor.Effort = tc.effort
			res, runDir, err := runPreflightWithOptions(t, tk, tc.opts)
			if err != nil {
				t.Fatalf("preflight error: %v", err)
			}
			if res == nil || res.Status != "completed" {
				t.Fatalf("res = %+v", res)
			}
			plan := readCreatorCommandPlan(t, runDir)
			for _, pair := range tc.wantInSeq {
				if !argvHasFlagValue(plan.Argv, pair[0], pair[1]) {
					t.Fatalf("provider %q argv missing %q %q: %+v", tc.cli, pair[0], pair[1], plan.Argv)
				}
			}
		})
	}
}

// argvHasFlagValue reports whether argv contains flag immediately followed by
// value.
func argvHasFlagValue(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
