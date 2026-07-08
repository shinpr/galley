package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/daemonconfig"
)

// TestRunOnceDeliversNotificationThroughRunWiring drives the production
// notification chain end-to-end: Run installs the dispatcher, processClaimedTask
// fires notifyTerminalPublication from its defer after the task has already been
// published to tasks/done, and deliverTerminalNotification resolves the moved
// task by filepath.Base(runningPath) before running the command. The
// deliverTerminalNotification unit tests all call that function directly with a
// hand-built Options.Notifications, so a regression that leaves
// opts.notifyDispatcher nil, drops the defer, or breaks the post-move base-name
// lookup would keep every existing test green while silently disabling all
// notifications in the real daemon. This test proves the wiring by driving a
// real Run to an accepted terminal status and asserting the operator command
// actually executed with the persisted task status on its environment.
func TestRunOnceDeliversNotificationThroughRunWiring(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	marker := filepath.Join(t.TempDir(), "notified")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := Run(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Once:               true,
		MaxConcurrentTasks: 1,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		// accepted is an opt-in status; naming it here drives a status the fake
		// claude reliably produces (terminal move to tasks/done).
		Notifications: &daemonconfig.NotificationConfig{
			Enabled: true,
			On:      []string{"accepted"},
			// Write the persisted status the hook observed. A wiring regression
			// (nil dispatcher, missing defer, or a base-name lookup that no
			// longer finds the moved task) leaves this marker unwritten.
			Command: "printf '%s' \"$GALLEY_TASK_STATUS\" > " + shellPath(marker),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Run defers notifyDispatcher.Wait(), so by the time Run returns the detached
	// delivery goroutine has finished and the marker must exist.
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("notification hook did not run through Run wiring: %v", err)
	}
	if got := string(data); got != "accepted" {
		t.Fatalf("hook observed task status %q, want accepted (post-move base-name lookup must resolve the published task)", got)
	}
}
