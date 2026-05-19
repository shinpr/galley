package daemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
)

// captureStderr temporarily redirects os.Stderr to a pipe and returns the
// captured bytes after the callback exits. Daemon helper sweeps write
// operator-visible "skipping ... unreadable task" warnings to os.Stderr; the
// tolerance tests assert that the warning fires for the unreadable task
// while readable tasks continue processing.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w

	var (
		mu     sync.Mutex
		buf    strings.Builder
		doneCh = make(chan struct{})
	)
	go func() {
		data, _ := io.ReadAll(r)
		mu.Lock()
		buf.Write(data)
		mu.Unlock()
		close(doneCh)
	}()

	fn()

	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	return buf.String()
}

// writeLegacyDaemonTaskYAML writes a task with an unknown nested field that
// strict task.Load rejects. The contents otherwise resemble a done-state
// task with a PR URL so it shows up under the daemon's PR-comment and
// worktree-cleanup scans.
func writeLegacyDaemonTaskYAML(t *testing.T, path, repo string) {
	t.Helper()
	body := `id: "task-legacy-daemon"
mode: "afk"
status: "pr_opened"
goal: "Legacy daemon fixture."
acceptance_criteria:
  - id: "AC1"
    text: "Loads."
    verification: "true"
    status: "pending"
scope:
  cwd: ` + strconv.Quote(repo) + `
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "edit"
execution_policy:
  loop_budget: 1
  timeout_ms: 5000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/task-legacy-daemon"
  path: "../worktrees/task-legacy-daemon"
supervisor:
  review_iterations: 0
  provider: "legacy-supervisor"
executor:
  cli: "claude"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: "https://github.com/example/galley/pull/999"
  status: "open"
  author_login: "maintainer"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPollPRCommentsSkipsUnreadableTask covers AC: PR comment polling must
// skip unreadable historical task files with operator-visible warning
// evidence while continuing to process readable tasks in the same sweep.
func TestPollPRCommentsSkipsUnreadableTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, false)

	// Readable task that should still be requeued.
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

	// Sibling unreadable historical task (legacy unknown field).
	legacyPath := filepath.Join(root, "tasks", "done", "legacy.yaml")
	writeLegacyDaemonTaskYAML(t, legacyPath, repo)

	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":42,"body":"/galley rerun tighten tests","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","user":{"login":"maintainer"}}]]'
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

	// Warning evidence covers the unreadable historical task.
	if !strings.Contains(stderr, "skipping PR comment scan for unreadable task") {
		t.Fatalf("expected operator-visible warning on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "legacy.yaml") {
		t.Fatalf("warning must name the unreadable task path, got %q", stderr)
	}

	// Readable task continues processing: it was requeued and the source
	// path was moved into tasks/queued.
	queuedPath := filepath.Join(root, "tasks", "queued", "readable.yaml")
	requeued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatalf("readable task should have been requeued: %v", err)
	}
	if requeued.Status != "queued" {
		t.Fatalf("readable task status got %q, want queued", requeued.Status)
	}
	if !slices.Contains(requeued.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("readable task processed comments got %#v", requeued.PR.ProcessedCommentIDs)
	}

	// Unreadable task stays untouched so the operator can inspect it.
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("unreadable task must remain in place for operator inspection: %v", err)
	}
}

// TestCleanupWorktreesSkipsUnreadableTask covers AC: worktree cleanup must
// skip unreadable historical task files with an operator-visible warning
// while continuing to act on readable tasks in the same sweep.
func TestCleanupWorktreesSkipsUnreadableTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, false, false)

	// Readable task with an open PR worktree.
	taskPath := filepath.Join(root, "tasks", "done", "readable.yaml")
	writeDaemonTask(t, taskPath, repo)
	doneTask, worktreePath := prepareDonePRTask(t, taskPath, repo, "open")

	// Sibling unreadable historical task: cleanup must skip it.
	legacyPath := filepath.Join(root, "tasks", "done", "legacy.yaml")
	writeLegacyDaemonTaskYAML(t, legacyPath, repo)

	// PR state lookup always reports "open" so the readable worktree must
	// be preserved (the existing TestCleanupWorktreesKeepsOpenPRWorktree
	// asserts the open-state contract). Coupling that assertion with the
	// new skip warning proves cleanup continued past the unreadable file.
	ghBin := writeFakeCommand(t, "gh", "echo '{\"state\":\"open\",\"merged\":false}'\n")

	var cleanupErr error
	stderr := captureStderr(t, func() {
		cleanupErr = cleanupWorktrees(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults())
	})
	if cleanupErr != nil {
		t.Fatalf("cleanup must not abort when a sibling task is unreadable: %v", cleanupErr)
	}

	if !strings.Contains(stderr, "skipping worktree cleanup for unreadable task") {
		t.Fatalf("expected operator-visible warning on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "legacy.yaml") {
		t.Fatalf("warning must name the unreadable task path, got %q", stderr)
	}

	// Readable task still processed: open PR keeps its worktree and PR
	// status is unchanged.
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("open PR worktree should remain after readable task processing: %v", err)
	}
	reloaded, err := task.Load(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PR.Status != doneTask.PR.Status {
		t.Fatalf("readable task PR status got %q want %q", reloaded.PR.Status, doneTask.PR.Status)
	}

	// Unreadable file kept untouched.
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("unreadable task must remain in place: %v", err)
	}
}
