package claude_guard_plugin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureWritesExecutableGuardPlugin(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		".claude-plugin/plugin.json",
		"hooks/hooks.json",
		"scripts/block-finalizer-commands.py",
		"scripts/require-final-json.py",
	} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if runtime.GOOS != "windows" && name == "scripts/block-finalizer-commands.py" && info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("script is not executable: %v", info.Mode().Perm())
		}
		if runtime.GOOS != "windows" && name == "scripts/require-final-json.py" && info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("script is not executable: %v", info.Mode().Perm())
		}
	}
}

func TestGuardBlocksNestedFinalizerCommand(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "block-finalizer-commands.py")
	cmd := pythonCommand(t, script)
	cmd.Stdin = strings.NewReader(`{"tool_input":{"command":"bash -c 'git commit -m done'"}}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed: %v\n%s", err, output)
	}
	got := string(output)
	if !strings.Contains(got, `"permissionDecision": "deny"`) {
		t.Fatalf("expected deny output, got %s", got)
	}
	if !strings.Contains(got, "orchestrator handles commit") {
		t.Fatalf("expected orchestrator guidance, got %s", got)
	}
}

func TestRequireFinalJSONAcceptsCreatorManifestMode(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "require-final-json.py")
	cmd := pythonCommand(t, script)
	cmd.Env = append(os.Environ(), "GALLEY_CLAUDE_GUARD_MODE=acceptance_skeleton_creator")
	cmd.Stdin = strings.NewReader(`{"last_assistant_message":"{\"outputs\":[{\"ac_id\":\"AC1\",\"path\":\"tests/foo_test.go\",\"kind\":\"integration\",\"purpose\":\"verify foo\",\"satisfies\":\"AC1 behavior\",\"integration_point\":\"executor fills assertions\",\"implementation_required\":true}],\"no_skeletons\":[]}"}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("expected creator manifest to pass, got %s", output)
	}
}

func TestRequireFinalJSONBlocksInvalidCreatorManifest(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "require-final-json.py")
	cmd := pythonCommand(t, script)
	cmd.Env = append(os.Environ(), "GALLEY_CLAUDE_GUARD_MODE=acceptance_skeleton_creator")
	cmd.Stdin = strings.NewReader(`{"last_assistant_message":"{\"outputs\":[{\"ac_id\":\"AC1\"}],\"no_skeletons\":[]}"}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed: %v\n%s", err, output)
	}
	got := string(output)
	if !strings.Contains(got, `"decision": "block"`) {
		t.Fatalf("expected block output, got %s", got)
	}
	if !strings.Contains(got, "acceptance skeleton manifest") {
		t.Fatalf("expected creator manifest guidance, got %s", got)
	}
}

func TestRequireFinalJSONRequiresExecutorScopeExpansions(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "require-final-json.py")
	cmd := pythonCommand(t, script)
	cmd.Stdin = strings.NewReader(`{"last_assistant_message":"{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[],\"acceptance_criteria\":[],\"verification\":[],\"decisions\":[],\"risks\":[]}"}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed: %v\n%s", err, output)
	}
	got := string(output)
	if !strings.Contains(got, `"decision": "block"`) {
		t.Fatalf("expected block output, got %s", got)
	}
	if !strings.Contains(got, "scope_expansions is required") {
		t.Fatalf("expected scope_expansions guidance, got %s", got)
	}
}

func TestRequireFinalJSONRejectsInvalidScopeExpansionPath(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "require-final-json.py")
	tests := []string{
		"/tmp/foo.go",
		"C:/tmp/foo.go",
		`internal\foo.go`,
		"internal//foo.go",
		"internal/foo/",
		"internal/./foo.go",
		"internal/../foo.go",
	}
	for _, path := range tests {
		path := path
		t.Run(path, func(t *testing.T) {
			cmd := pythonCommand(t, script)
			result := map[string]any{
				"status":              "completed",
				"summary":             "done",
				"files_modified":      []string{path},
				"acceptance_criteria": []any{},
				"verification":        []any{},
				"scope_expansions": []map[string]string{{
					"path":               path,
					"reason":             "needed",
					"linked_requirement": "AC1",
					"minimality":         "one file",
				}},
				"decisions": []any{},
				"risks":     []any{},
			}
			resultJSON, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			hookInput, err := json.Marshal(map[string]string{"last_assistant_message": string(resultJSON)})
			if err != nil {
				t.Fatal(err)
			}
			body := string(hookInput)
			cmd.Stdin = strings.NewReader(body)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("guard script failed: %v\n%s", err, output)
			}
			got := string(output)
			if !strings.Contains(got, `"decision": "block"`) {
				t.Fatalf("expected block output, got %s", got)
			}
			if !strings.Contains(got, "scope_expansions[0].path must be a clean relative path") {
				t.Fatalf("expected clean relative path guidance, got %s", got)
			}
		})
	}
}

func TestRequireFinalJSONRejectsConflictingSupervisorQualityResult(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{
		"status": "accepted", "summary": "done", "acceptance_gaps": []any{}, "reviewed_files": []string{"file.go"},
		"acceptance_evidence": []any{}, "findings": []any{}, "residual_risks": []any{}, "discussion_items": []any{}, "confidence": "high", "next_work_order": "",
		"quality_passes": []string{"criterion-a"},
		"quality_gaps":   []string{" criterion-a "},
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	hookInput, err := json.Marshal(map[string]string{"last_assistant_message": string(resultJSON)})
	if err != nil {
		t.Fatal(err)
	}
	cmd := pythonCommand(t, filepath.Join(dir, "scripts", "require-final-json.py"))
	cmd.Env = append(os.Environ(), "GALLEY_CLAUDE_GUARD_MODE=supervisor")
	cmd.Stdin = strings.NewReader(string(hookInput))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guard script failed: %v\n%s", err, output)
	}
	if got := string(output); !strings.Contains(got, `"decision": "block"`) || !strings.Contains(got, "appears in both or multiple results") {
		t.Fatalf("expected conflicting quality result rejection, got %s", got)
	}
}

func TestRequireFinalJSONRequiresQualityResultArrays(t *testing.T) {
	dir, err := Ensure(filepath.Join(t.TempDir(), "guard"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "require-final-json.py")
	tests := []struct{ name, missing string }{
		{name: "missing passes", missing: "quality_passes"},
		{name: "missing gaps", missing: "quality_gaps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := map[string]any{
				"status": "accepted", "summary": "reviewed", "acceptance_gaps": []any{}, "reviewed_files": []string{"file.go"},
				"acceptance_evidence": []any{}, "quality_passes": []any{}, "quality_gaps": []any{}, "findings": []any{}, "residual_risks": []any{}, "discussion_items": []any{}, "confidence": "high",
				"next_work_order": "",
			}
			delete(result, tt.missing)
			resultJSON, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			hookInput, err := json.Marshal(map[string]string{"last_assistant_message": string(resultJSON)})
			if err != nil {
				t.Fatal(err)
			}
			cmd := pythonCommand(t, script)
			cmd.Env = append(os.Environ(), "GALLEY_CLAUDE_GUARD_MODE=supervisor")
			cmd.Stdin = strings.NewReader(string(hookInput))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("guard script failed: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), `"decision": "block"`) {
				t.Fatalf("expected missing %s to block; output: %s", tt.missing, output)
			}
		})
	}
}

func pythonCommand(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	if runtime.GOOS != "windows" {
		return exec.Command(script)
	}
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skipf("python not available: %v", err)
	}
	return exec.Command(python, script)
}
