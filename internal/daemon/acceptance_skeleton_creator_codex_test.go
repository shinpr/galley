package daemon

// AC: AC1, AC2, AC4 — The built-in acceptance skeleton creator selects its
// provider from the task implementation executor backend (task.executor.cli)
// and, for Codex tasks, runs through the Codex command planner with the
// acceptance skeleton prompt, manifest schema, attempt-scoped output schema,
// and attempt-scoped last-message capture.
//
// Behavior under test:
//   - Trigger: AcceptanceSkeletonPreflight runs for a preflight-enabled task.
//   - Process: buildBuiltinCreatorCommandPlan routes to the Codex command
//     planner when executor.cli="codex" and to the Claude planner otherwise;
//     the Codex creator captures the manifest from the
//     `codex exec --output-last-message` file.
//   - Observable result: the persisted preflight_creator_command_plan.json
//     reflects the selected provider, the Codex plan invokes `codex exec` with
//     schema and last-message capture and persists no secret environment
//     values, and the task executor model/effort settings propagate to both
//     providers.
//
// @lane: integration
// @category: core-functionality
// @dependency: AcceptanceSkeletonPreflight, runner Codex/Claude command planners
// @complexity: medium

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/task"
)

// fakeCodexCreator returns a fake `codex` binary that acts as the acceptance
// skeleton creator. It parses `--output-last-message` from argv, discards the
// combined stdin prompt, runs the caller-supplied file writes, and writes the
// manifest JSON to the capture file the way the real Codex CLI writes its
// final assistant message (R2).
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
func readCreatorCommandPlan(t *testing.T, runDir string) (struct {
	Argv  []string `json:"argv"`
	Env   []string `json:"env"`
	Stdin string   `json:"stdin"`
}, []byte) {
	t.Helper()
	var plan struct {
		Argv  []string `json:"argv"`
		Env   []string `json:"env"`
		Stdin string   `json:"stdin"`
	}
	data, err := os.ReadFile(filepath.Join(runDir, "preflight_creator_command_plan.json"))
	if err != nil {
		t.Fatalf("read command plan: %v", err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode command plan: %v", err)
	}
	return plan, data
}

// TestAcceptanceSkeletonPreflightCodexProviderHappyPath proves AC2/AC6: a
// Codex executor task runs the creator through `codex exec`, captures the
// manifest from the last-message file, and persists secret-free run evidence.
func TestAcceptanceSkeletonPreflightCodexProviderHappyPath(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-secret")
	t.Setenv("OPENAI_API_KEY", "sk-codex-test-secret")
	manifest := codexCreatorManifest(`[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"executor completes this test before acceptance","implementation_required":true}]`)
	codexBin := fakeCodexCreator(t, manifest, `mkdir -p internal/foo
printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go`)

	res, err, runDir := runPreflightWithOptions(t, codexPreflightTask("AC1"), skeletonpreflight.Options{CodexBin: codexBin})
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
	for _, name := range []string{"preflight_result.json", "preflight_creator_manifest.json", "preflight_creator_command_plan.json"} {
		if _, statErr := os.Stat(filepath.Join(runDir, name)); statErr != nil {
			t.Fatalf("%s not written: %v", name, statErr)
		}
	}

	plan, raw := readCreatorCommandPlan(t, runDir)
	if len(plan.Argv) < 2 || filepath.Base(plan.Argv[0]) != "codex" || plan.Argv[1] != "exec" {
		t.Fatalf("codex creator command plan does not invoke `codex exec`: %+v", plan.Argv)
	}
	joinedArgv := strings.Join(plan.Argv, "\n")
	if !strings.Contains(joinedArgv, "--output-schema") {
		t.Fatalf("codex creator command plan missing --output-schema: %+v", plan.Argv)
	}
	if !strings.Contains(joinedArgv, "--output-last-message") {
		t.Fatalf("codex creator command plan missing --output-last-message: %+v", plan.Argv)
	}
	if len(plan.Env) != 0 {
		t.Fatalf("codex creator command plan persisted environment: %+v", plan.Env)
	}
	if strings.Contains(string(raw), "sk-ant-test-secret") ||
		strings.Contains(string(raw), "sk-codex-test-secret") ||
		strings.Contains(string(raw), "ANTHROPIC_API_KEY") ||
		strings.Contains(string(raw), "OPENAI_API_KEY") {
		t.Fatalf("codex creator command plan persisted secret env: %s", raw)
	}

	// The attempt-scoped output schema and last-message capture files live
	// alongside the other run evidence (AC2).
	for _, name := range []string{"codex.output-schema.json", "codex.last-message.txt"} {
		if _, statErr := os.Stat(filepath.Join(runDir, name)); statErr != nil {
			t.Fatalf("attempt-scoped codex file %s not written: %v", name, statErr)
		}
	}

	// The Codex provider uses the Codex-tuned acceptance skeleton prompt,
	// delivered through the combined stdin envelope (AC7).
	if !strings.Contains(plan.Stdin, "running inside Codex") {
		t.Fatalf("codex creator did not use the Codex skeleton creator prompt; stdin head: %.200s", plan.Stdin)
	}
	if strings.Contains(plan.Stdin, "running inside Claude Code") {
		t.Fatalf("codex creator used the Claude skeleton creator prompt")
	}
}

// TestAcceptanceSkeletonPreflightCodexFallsBackToStdout proves the best-effort
// fallback path: if Codex exits successfully but does not write a valid
// --output-last-message capture file, Galley can still recover the creator
// manifest from the Codex JSON stdout stream.
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

	res, err, runDir := runPreflightWithOptions(t, codexPreflightTask("AC1"), skeletonpreflight.Options{CodexBin: codexBin})
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

// TestAcceptanceSkeletonPreflightSelectsProviderFromExecutorCLI proves AC1:
// the creator provider is selected from task.executor.cli for both supported
// backends.
func TestAcceptanceSkeletonPreflightSelectsProviderFromExecutorCLI(t *testing.T) {
	claudeOutputs := `[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`
	codexOutputs := `[{"ac_id":"AC1","path":"internal/foo/foo_test.go","kind":"go-test","purpose":"verify AC1","satisfies":"AC1 behavior","integration_point":"executor completes this test","implementation_required":true}]`
	fileWrite := `mkdir -p internal/foo
printf 'package foo_test\n' > internal/foo/foo_test.go`

	for _, tc := range []struct {
		name       string
		cli        string
		opts       skeletonpreflight.Options
		wantBin    string
		wantArg1   string
		wantInArgv string
	}{
		{
			name:       "claude",
			cli:        "claude",
			opts:       skeletonpreflight.Options{ClaudeBin: fakeCreator(t, resultManifest(claudeOutputs), fileWrite)},
			wantBin:    "claude",
			wantInArgv: "--plugin-dir",
		},
		{
			name:       "codex",
			cli:        "codex",
			opts:       skeletonpreflight.Options{CodexBin: fakeCodexCreator(t, codexCreatorManifest(codexOutputs), fileWrite)},
			wantBin:    "codex",
			wantArg1:   "exec",
			wantInArgv: "--output-last-message",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tk := preflightTestTask("AC1")
			tk.Executor.CLI = tc.cli
			res, err, runDir := runPreflightWithOptions(t, tk, tc.opts)
			if err != nil {
				t.Fatalf("preflight error: %v", err)
			}
			if res == nil || res.Status != "completed" {
				t.Fatalf("res = %+v", res)
			}
			plan, _ := readCreatorCommandPlan(t, runDir)
			if len(plan.Argv) == 0 || filepath.Base(plan.Argv[0]) != tc.wantBin {
				t.Fatalf("provider %q used wrong binary: %+v", tc.cli, plan.Argv)
			}
			if tc.wantArg1 != "" && (len(plan.Argv) < 2 || plan.Argv[1] != tc.wantArg1) {
				t.Fatalf("provider %q argv[1] = %v, want %q", tc.cli, plan.Argv, tc.wantArg1)
			}
			if !strings.Contains(strings.Join(plan.Argv, "\n"), tc.wantInArgv) {
				t.Fatalf("provider %q argv missing %q: %+v", tc.cli, tc.wantInArgv, plan.Argv)
			}
		})
	}
}

// TestAcceptanceSkeletonPreflightPropagatesModelAndEffort proves AC4: the
// creator command plan carries the task executor model and effort settings for
// both providers, so creator runs and implementation runs share the task's
// executor backend configuration.
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
		opts      skeletonpreflight.Options
		wantInSeq [][2]string
	}{
		{
			name:   "claude",
			cli:    "claude",
			model:  "opus",
			effort: "high",
			opts:   skeletonpreflight.Options{ClaudeBin: fakeCreator(t, resultManifest(claudeOutputs), fileWrite)},
			// Claude exposes --model and --effort flags directly.
			wantInSeq: [][2]string{{"--model", "opus"}, {"--effort", "high"}},
		},
		{
			name:   "codex",
			cli:    "codex",
			model:  "gpt-5-codex",
			effort: "high",
			opts:   skeletonpreflight.Options{CodexBin: fakeCodexCreator(t, codexCreatorManifest(codexOutputs), fileWrite)},
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
			res, err, runDir := runPreflightWithOptions(t, tk, tc.opts)
			if err != nil {
				t.Fatalf("preflight error: %v", err)
			}
			if res == nil || res.Status != "completed" {
				t.Fatalf("res = %+v", res)
			}
			plan, _ := readCreatorCommandPlan(t, runDir)
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
