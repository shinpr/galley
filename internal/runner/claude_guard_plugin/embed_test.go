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
