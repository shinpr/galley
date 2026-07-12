package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/task"
)

func preflightTestTask(acIDs ...string) task.Task {
	if len(acIDs) == 0 {
		acIDs = []string{"AC1"}
	}
	crit := make([]task.AcceptanceCriterion, 0, len(acIDs))
	for _, id := range acIDs {
		crit = append(crit, task.AcceptanceCriterion{ID: id, Text: id, Verification: "see " + id, Status: "pending"})
	}
	return task.Task{
		ID:                 "preflight-test",
		AcceptanceCriteria: crit,
		Scope: task.Scope{
			AllowedPaths:   []string{"."},
			ForbiddenPaths: []string{"secret"},
		},
		Preflight: &task.Preflight{AcceptanceSkeleton: &task.AcceptanceSkeletonConfig{Enabled: true}},
	}
}

func runPreflightWithOptions(t *testing.T, tk task.Task, opts skeletonpreflight.Options) (*skeletonpreflight.Result, error, string) {
	t.Helper()
	work := t.TempDir()
	runDir := t.TempDir()
	res, err := runPreflightInWorkdir(t, tk, opts, work, runDir)
	return res, err, runDir
}

func runPreflightInWorkdir(t *testing.T, tk task.Task, opts skeletonpreflight.Options, work, runDir string) (*skeletonpreflight.Result, error) {
	t.Helper()
	if err := exec.Command("git", "-C", work, "rev-parse", "--git-dir").Run(); err != nil {
		if err := exec.Command("git", "init", work).Run(); err != nil {
			t.Fatalf("initialize test repository: %v", err)
		}
	}
	opts.Task = tk
	opts.WorkDir = work
	opts.RunDir = runDir
	return skeletonpreflight.Run(context.Background(), opts)
}

func fakeCreator(t *testing.T, manifest string, fileWrites string) string {
	t.Helper()
	return writeFakeCommand(t, "claude", fileWrites+"\nprintf '%s\n' '"+manifest+"'\n")
}

func resultManifest(outputs string) string {
	return `{"type":"result","result":"{\"outputs\":` + outputs + `,\"no_skeletons\":[]}"}`
}

func TestAcceptanceSkeletonPreflightBuiltInCreatorHappyPath(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-secret")
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 user-visible behavior\",\"integration_point\":\"executor completes this test before acceptance\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, `mkdir -p internal/foo
printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 assertion.\n' > internal/foo/foo_test.go`)

	res, err, runDir := runPreflightWithOptions(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin})
	if err != nil {
		t.Fatalf("preflight error: %v", err)
	}
	if res == nil || res.Status != "completed" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Outputs) != 1 {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	out := res.Outputs[0]
	if out.Path != "internal/foo/foo_test.go" || out.Satisfies == "" || out.IntegrationPoint == "" {
		t.Fatalf("output metadata not propagated: %+v", out)
	}
	if len(res.Baseline.SkeletonHashes) != 1 || res.Baseline.SkeletonHashes[0].Path != "internal/foo/foo_test.go" {
		t.Fatalf("baseline = %+v", res.Baseline)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "preflight_result.json")); statErr != nil {
		t.Fatalf("preflight_result.json not written: %v", statErr)
	}
	var plan struct {
		Argv []string `json:"argv"`
		Env  []string `json:"env"`
	}
	data, readErr := os.ReadFile(filepath.Join(runDir, "preflight_creator_command_plan.json"))
	if readErr != nil {
		t.Fatalf("read command plan: %v", readErr)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode command plan: %v", err)
	}
	joinedArgv := strings.Join(plan.Argv, "\n")
	if !strings.Contains(joinedArgv, "--plugin-dir") || !strings.Contains(joinedArgv, "claude-guard-plugin") {
		t.Fatalf("creator command plan missing guard plugin: %+v", plan.Argv)
	}
	if len(plan.Env) != 0 {
		t.Fatalf("creator command plan persisted environment: %+v", plan.Env)
	}
	if strings.Contains(string(data), "sk-ant-test-secret") || strings.Contains(string(data), "ANTHROPIC_API_KEY") {
		t.Fatalf("creator command plan persisted secret env: %s", data)
	}
}

func TestAcceptanceSkeletonPreflightMissingDeclaredFileFails(t *testing.T) {
	t.Parallel()
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"p\",\"satisfies\":\"s\",\"integration_point\":\"i\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, "")
	res, err, _ := runPreflightWithOptions(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin})
	if err == nil {
		t.Fatal("expected error for missing creator-declared file")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_creator" || !strings.Contains(res.Error.Message, "does not exist") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightRejectsUndeclaredCreatorChanges(t *testing.T) {
	t.Parallel()
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, `mkdir -p internal/foo
printf 'package foo_test\n' > internal/foo/foo_test.go
printf 'package foo_test\n' > internal/foo/extra_test.go`)

	res, err, _ := runPreflightWithOptions(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin})
	if err == nil {
		t.Fatal("expected error for undeclared creator file")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_creator" || !strings.Contains(res.Error.Message, "undeclared path") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightRequiresDeclaredOutputChangedByCreator(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "internal/foo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "internal/foo/foo_test.go"), []byte("package foo_test\n"), 0o644); err != nil {
		t.Fatalf("write existing test: %v", err)
	}
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, "")

	res, err := runPreflightInWorkdir(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin}, work, runDir)
	if err == nil {
		t.Fatal("expected error for unchanged declared output")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_creator" || !strings.Contains(res.Error.Message, "not created or modified") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightRejectsRunDirOutput(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	runDir := filepath.Join(work, ".galley", "runs", "run-1")
	outputPath := ".galley/runs/run-1/preflight_creator_command_plan.json"
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"` + outputPath + `\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, "")

	res, err := runPreflightInWorkdir(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin}, work, runDir)
	if err == nil {
		t.Fatal("expected error for run evidence output")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, "run evidence directory") {
		t.Fatalf("error = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightRejectsSymlinkDirectoryEscape(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	outside := t.TempDir()
	runDir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(work, "tests")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"tests/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, `printf 'package foo_test\n' > tests/foo_test.go`)

	res, err := runPreflightInWorkdir(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin}, work, runDir)
	if err == nil {
		t.Fatal("expected error for symlink directory escape")
	}
	if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_creator" || !strings.Contains(res.Error.Message, "escapes the worktree") {
		t.Fatalf("error = %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "foo_test.go")); statErr != nil {
		t.Fatalf("expected creator to write outside file through symlink: %v", statErr)
	}
}

func TestAcceptanceSkeletonPreflightIgnoresRunDirInsideWorktreeSnapshot(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	runDir := filepath.Join(work, ".galley", "runs", "run-1")
	manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor completes this test\",\"implementation_required\":true}]`)
	claudeBin := fakeCreator(t, manifest, `mkdir -p internal/foo
printf 'package foo_test\n' > internal/foo/foo_test.go`)

	res, err := runPreflightInWorkdir(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin}, work, runDir)
	if err != nil {
		t.Fatalf("preflight error: %v", err)
	}
	if res == nil || res.Status != "completed" {
		t.Fatalf("res = %+v", res)
	}
}

func TestAcceptanceSkeletonPreflightRejectsUnsafePath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		path     string
		wantText string
	}{
		{name: "absolute", path: "/etc/galley_skeleton_test.go", wantText: "absolute"},
		{name: "traversal", path: "../escape_test.go", wantText: "traversal"},
		{name: "forbidden", path: "secret/foo_test.go", wantText: "forbidden_paths"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manifest := resultManifest(`[{\"ac_id\":\"AC1\",\"path\":\"` + tc.path + `\",\"kind\":\"go-test\",\"purpose\":\"p\",\"satisfies\":\"s\",\"integration_point\":\"i\",\"implementation_required\":true}]`)
			claudeBin := fakeCreator(t, manifest, `mkdir -p secret
printf 'package foo\n' > secret/foo_test.go`)
			res, err, _ := runPreflightWithOptions(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin})
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, tc.wantText) {
				t.Fatalf("error = %+v", res)
			}
		})
	}
}

func TestAcceptanceSkeletonPreflightAcceptsDuplicateOutputPaths(t *testing.T) {
	t.Parallel()
	manifest := resultManifest(`[` +
		`{\"ac_id\":\"AC1\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC1\",\"satisfies\":\"AC1 user-visible behavior\",\"integration_point\":\"executor finishes AC1 case\",\"implementation_required\":true},` +
		`{\"ac_id\":\"AC2\",\"path\":\"internal/foo/foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"verify AC2\",\"satisfies\":\"AC2 user-visible behavior\",\"integration_point\":\"executor finishes AC2 case\",\"implementation_required\":true}` +
		`]`)
	claudeBin := fakeCreator(t, manifest, `mkdir -p internal/foo
printf 'package foo_test\n\n// TODO(galley-skeleton): implement AC1 and AC2 assertions.\n' > internal/foo/foo_test.go`)

	res, err, runDir := runPreflightWithOptions(t, preflightTestTask("AC1", "AC2"), skeletonpreflight.Options{ClaudeBin: claudeBin})
	if err != nil {
		t.Fatalf("preflight error: %v", err)
	}
	if res == nil || res.Status != "completed" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("expected 2 outputs for duplicate-path case, got %+v", res.Outputs)
	}
	byAC := map[string]skeletonpreflight.Output{}
	for _, out := range res.Outputs {
		byAC[out.ACID] = out
	}
	for _, ac := range []string{"AC1", "AC2"} {
		out, ok := byAC[ac]
		if !ok {
			t.Fatalf("missing output for %s in %+v", ac, res.Outputs)
		}
		if out.Path != "internal/foo/foo_test.go" {
			t.Fatalf("%s path = %q, want shared skeleton path", ac, out.Path)
		}
		if out.Purpose == "" || out.Satisfies == "" || out.IntegrationPoint == "" {
			t.Fatalf("%s metadata not preserved: %+v", ac, out)
		}
		if !strings.Contains(out.Purpose, ac) || !strings.Contains(out.Satisfies, ac) || !strings.Contains(out.IntegrationPoint, ac) {
			t.Fatalf("%s metadata mixed up: %+v", ac, out)
		}
	}
	if len(res.Baseline.SkeletonHashes) != 1 {
		t.Fatalf("baseline should dedupe the shared path, got %+v", res.Baseline.SkeletonHashes)
	}
	persisted, lerr := skeletonpreflight.LoadResult(runDir)
	if lerr != nil {
		t.Fatalf("LoadPreflightResult: %v", lerr)
	}
	if persisted == nil || len(persisted.Outputs) != 2 {
		t.Fatalf("persisted preflight_result.json did not preserve duplicate outputs: %+v", persisted)
	}
	for _, ac := range []string{"AC1", "AC2"} {
		found := false
		for _, out := range persisted.Outputs {
			if out.ACID == ac && out.Path == "internal/foo/foo_test.go" && out.Purpose != "" && out.Satisfies != "" && out.IntegrationPoint != "" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("persisted preflight_result.json missing %s entry: %+v", ac, persisted.Outputs)
		}
	}
}

func TestAcceptanceSkeletonPreflightRejectsInvalidManifestFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		output   string
		wantText string
	}{
		{name: "invalid AC", output: `{\"ac_id\":\"AC-nope\",\"path\":\"foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"p\",\"satisfies\":\"s\",\"integration_point\":\"i\",\"implementation_required\":true}`, wantText: "acceptance_criteria.id"},
		{name: "missing kind", output: `{\"ac_id\":\"AC1\",\"path\":\"foo_test.go\",\"kind\":\"\",\"purpose\":\"p\",\"satisfies\":\"s\",\"integration_point\":\"i\",\"implementation_required\":true}`, wantText: "kind"},
		{name: "missing purpose", output: `{\"ac_id\":\"AC1\",\"path\":\"foo_test.go\",\"kind\":\"go-test\",\"purpose\":\" \",\"satisfies\":\"s\",\"integration_point\":\"i\",\"implementation_required\":true}`, wantText: "purpose"},
		{name: "missing satisfies", output: `{\"ac_id\":\"AC1\",\"path\":\"foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"p\",\"satisfies\":\" \",\"integration_point\":\"i\",\"implementation_required\":true}`, wantText: "satisfies"},
		{name: "missing integration", output: `{\"ac_id\":\"AC1\",\"path\":\"foo_test.go\",\"kind\":\"go-test\",\"purpose\":\"p\",\"satisfies\":\"s\",\"integration_point\":\" \",\"implementation_required\":true}`, wantText: "integration_point"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manifest := resultManifest(`[` + tc.output + `]`)
			claudeBin := fakeCreator(t, manifest, `printf 'package foo\n' > foo_test.go`)
			res, err, runDir := runPreflightWithOptions(t, preflightTestTask("AC1"), skeletonpreflight.Options{ClaudeBin: claudeBin})
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if res == nil || res.Error == nil || res.Error.Phase != "acceptance_skeleton_provider" || !strings.Contains(res.Error.Message, tc.wantText) {
				t.Fatalf("error = %+v", res)
			}
			persisted, lerr := skeletonpreflight.LoadResult(runDir)
			if lerr != nil {
				t.Fatalf("LoadPreflightResult: %v", lerr)
			}
			if persisted == nil || persisted.Status != "failed" {
				t.Fatalf("persisted preflight_result.json = %+v", persisted)
			}
		})
	}
}
