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
	"github.com/shinpr/galley/internal/vcs"
)

func TestParsePRCommand(t *testing.T) {
	t.Parallel()
	command, ok := parsePRCommand(vcs.PRComment{ID: 123, Body: "Looks close.\n/galley rerun fix AC2\n"})
	if !ok {
		t.Fatal("expected command")
	}
	if command.CommentID != "123" || command.Reason != "fix AC2" {
		t.Fatalf("command got %#v", command)
	}
}

func TestParsePRCommandKeepsMultilineInstruction(t *testing.T) {
	t.Parallel()
	command, ok := parsePRCommand(vcs.PRComment{ID: 123, Body: "/galley rerun\nPlease update the CLI help.\nAlso keep tests passing."})
	if !ok {
		t.Fatal("expected command")
	}
	for _, want := range []string{"Please update the CLI help.", "Also keep tests passing."} {
		if !strings.Contains(command.Reason, want) {
			t.Fatalf("reason missing %q: %q", want, command.Reason)
		}
	}
}

func TestPollPRCommentsRequeuesTaskOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
	echo '[[{"id":42,"body":"/galley rerun tighten tests","html_url":"https://github.com/example/galley/pull/123#issuecomment-42"}]]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	requeued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "queued" {
		t.Fatalf("status got %q", requeued.Status)
	}
	if !slices.Contains(requeued.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("processed comments got %#v", requeued.PR.ProcessedCommentIDs)
	}
	if len(requeued.RevisionRequests) != 1 || requeued.RevisionRequests[0].Text != "tighten tests" || requeued.RevisionRequests[0].Status != "pending" {
		t.Fatalf("revision requests got %#v", requeued.RevisionRequests)
	}

	if err := pollPRComments(context.Background(), Options{Root: root}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 1)
	if _, err := os.Stat(donePath); !os.IsNotExist(err) {
		t.Fatalf("done task should remain moved, err=%v", err)
	}
}

func TestPollPRCommentsPostsReply(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "posted")
	writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":99,"body":"/galley rerun reply please","html_url":"https://github.com/example/galley/pull/123#issuecomment-99"}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
echo '{"id":100}'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root, ReplyPRComments: true}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "body=Galley requeued") {
		t.Fatalf("reply command got %q", string(data))
	}
}

func TestPollPRCommentsDoesNotMarkFailedRequeueProcessed(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	writeDaemonTask(t, queuedPath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":42,"body":"/galley rerun retry after queue clears","html_url":"https://github.com/example/galley/pull/123#issuecomment-42"}]]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root}.withDefaults()); err == nil {
		t.Fatal("expected requeue destination error")
	}
	stillDone, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(stillDone.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("comment should not be marked processed after failed requeue: %#v", stillDone.PR.ProcessedCommentIDs)
	}
}
