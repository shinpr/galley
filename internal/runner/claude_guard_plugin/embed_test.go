package claude_guard_plugin

import (
	"os"
	"os/exec"
	"path/filepath"
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
		if name == "scripts/block-finalizer-commands.py" && info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("script is not executable: %v", info.Mode().Perm())
		}
		if name == "scripts/require-final-json.py" && info.Mode().Perm()&0o100 == 0 {
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
	cmd := exec.Command(script)
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
	cmd := exec.Command(script)
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
	cmd := exec.Command(script)
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
