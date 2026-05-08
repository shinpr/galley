package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/task"
)

func TestParsePRCommand(t *testing.T) {
	command, ok := parsePRCommand(prComment{ID: 123, Body: "Looks close.\n/galley rerun fix AC2\n"})
	if !ok {
		t.Fatal("expected command")
	}
	if command.CommentID != "123" || command.Reason != "fix AC2" {
		t.Fatalf("command got %#v", command)
	}
}

func TestPollPRCommentsRequeuesTaskOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := ensureLayout(root); err != nil {
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
	if !containsString(requeued.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("processed comments got %#v", requeued.PR.ProcessedCommentIDs)
	}
	if len(requeued.Risks) == 0 || !strings.Contains(requeued.Risks[len(requeued.Risks)-1].Detail, "tighten tests") {
		t.Fatalf("risks got %#v", requeued.Risks)
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
	if err := ensureLayout(root); err != nil {
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
	if err := ensureLayout(root); err != nil {
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
	if containsString(stillDone.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("comment should not be marked processed after failed requeue: %#v", stillDone.PR.ProcessedCommentIDs)
	}
}

func TestDecodePRCommentsSlurpPages(t *testing.T) {
	comments, err := decodePRComments(`[[{"id":1,"body":"first"}],[{"id":2,"body":"second"}]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("comments got %#v", comments)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
