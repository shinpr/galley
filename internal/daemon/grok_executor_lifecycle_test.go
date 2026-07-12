package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/task"
)

func TestGrokAllExecutorRolesUseNoFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	capture := filepath.Join(t.TempDir(), "grok.args")
	grokBin := filepath.Join(t.TempDir(), "grok")
	setupResult := `{"status":"ready","commands":[{"run":"true","source":"discovered","exit_code":0}],"successful_commands":[{"run":"true","why":"deterministic fixture readiness"}],"readiness_evidence":"fixture ready","source":"discovered"}`
	creatorResult := `{"outputs":[{"ac_id":"AC1","path":"internal/grok/grok_test.go","kind":"integration","purpose":"verify AC1","satisfies":"AC1 observable behavior","integration_point":"implementation completes this skeleton","implementation_required":true}],"no_skeletons":[]}`
	result := `{"status":"completed","summary":"grok done","files_modified":["grok-output.txt","internal/grok/grok_test.go"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["diff"],"notes":"done"}],"verification":[],"scope_expansions":[],"decisions":[],"risks":[]}`
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + capture + `"
prompt=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--prompt-file" ]; then prompt="$2"; shift 2; else shift; fi
done
case "$prompt" in
  *grok.setup.prompt.md)
    text='` + strings.ReplaceAll(setupResult, `'`, `'"'"'`) + `'
    session="grok-setup-session"
    ;;
  *grok.acceptance-skeleton.prompt.md)
    mkdir -p internal/grok
    printf 'package grok_test\n\n// TODO(galley-skeleton): implement AC1.\n' > internal/grok/grok_test.go
    text='` + strings.ReplaceAll(creatorResult, `'`, `'"'"'`) + `'
    session="grok-skeleton-session"
    ;;
  *)
    printf '%s\n' grok > grok-output.txt
    text='` + strings.ReplaceAll(result, `'`, `'"'"'`) + `'
    session="grok-session"
    ;;
esac
escaped=$(printf '%s' "$text" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf '{"text":"%s","stopReason":"EndTurn","sessionId":"%s"}\n' "$escaped" "$session"
`
	if err := os.WriteFile(grokBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeFakeClaude(t, "exit 1\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)
	loaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Executor.CLI = "grok"
	loaded.Preflight = &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}}
	if err := task.Save(taskPath, loaded); err != nil {
		t.Fatal(err)
	}
	envPath := writeSetupEnvironmentProfile(t, t.TempDir(), `id: "grok-all-roles"
cwd: `+workdirQuote(repo)+`
commands:
  test_unit: "true"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
`)
	opts := testDaemonOptions(Options{Root: root, SystemPromptFile: promptPath, JSONSchemaFile: schemaPath, EnvironmentProfileFile: envPath, Once: true, MaxConcurrentTasks: 1, Supervisor: "claude", ClaudeBin: claudeBin, GrokBin: grokBin})
	deps := opts.daemonDependencies()
	deps.setupExecutorRunner = func(ctx context.Context, setupOpts setuppreflight.Options) (*setuppreflight.Result, error) {
		return setuppreflight.RunExecutor(ctx, setupOpts)
	}
	opts.dependencies = &deps
	if err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	done, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "accepted" {
		t.Fatalf("status = %q", done.Status)
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--prompt-file") {
		t.Fatalf("Grok command evidence = %s", args)
	}
	for _, rolePrompt := range []string{"grok.setup.prompt.md", "grok.acceptance-skeleton.prompt.md", "grok.prompt.md"} {
		if !strings.Contains(string(args), rolePrompt) {
			t.Fatalf("missing %s in Grok command evidence: %s", rolePrompt, args)
		}
	}
	assertGlobCount(t, filepath.Join(root, "runs", "*", runartifact.SetupResultFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", runartifact.PreflightCreatorManifestFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", runartifact.GrokSetupCompletionFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", runartifact.GrokSkeletonCompletionFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", runartifact.GrokCompletionMetadataFilename), 1)
	assertGlobCount(t, filepath.Join(root, "runs", "*", "attempt-1", "grok.stdout.json"), 1)
	metadataPaths, err := filepath.Glob(filepath.Join(root, "runs", "*", "attempt-1", runartifact.GrokCompletionMetadataFilename))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(metadataPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"classification": "completed"`, `"stop_reason": "EndTurn"`, `"session_id": "grok-session"`} {
		if !strings.Contains(string(metadata), want) {
			t.Fatalf("completion metadata missing %s: %s", want, metadata)
		}
	}
}
