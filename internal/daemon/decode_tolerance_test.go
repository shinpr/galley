package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

func TestPollPRCommentsSkipsUndecodableTaskAndProcessesReadable(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, commentProfileSpec{Root: root, Repo: repo, PollComments: true, ReplyComments: false})

	donePath := filepath.Join(root, "tasks", "done", "readable.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "maintainer"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}

	// A known field with the wrong type makes task.Load fail; it sorts before the readable task.
	brokenPath := filepath.Join(root, "tasks", "done", "decode-broken.yaml")
	writeDaemonTask(t, brokenPath, repo)
	data, err := os.ReadFile(brokenPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), "timeout_ms: 5000", `timeout_ms: "soon"`, 1)
	if broken == string(data) {
		t.Fatal("timeout_ms line not found in task fixture")
	}
	if err := os.WriteFile(brokenPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":42,"body":"/galley tighten tests","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","user":{"login":"maintainer"}}]]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	var pollErr error
	stderr := captureStderr(t, func() {
		pollErr = pollPRComments(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults())
	})
	if pollErr != nil {
		t.Fatalf("poll must not abort when a sibling task is unreadable: %v", pollErr)
	}
	if !strings.Contains(stderr, "galley: skipping PR comment scan for unreadable task "+brokenPath) {
		t.Fatalf("stderr must warn about the unreadable task, got %q", stderr)
	}

	requeued, err := task.Load(filepath.Join(root, "tasks", "queued", "readable.yaml"))
	if err != nil {
		t.Fatalf("readable task should have been requeued: %v", err)
	}
	if requeued.Status != "queued" || !slices.Contains(requeued.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("readable task got status=%q processed=%#v", requeued.Status, requeued.PR.ProcessedCommentIDs)
	}
	if _, err := os.Stat(brokenPath); err != nil {
		t.Fatalf("unreadable task must stay in place: %v", err)
	}
}
