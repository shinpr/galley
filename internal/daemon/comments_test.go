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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
	echo '[[{"id":42,"body":"/galley rerun tighten tests","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","author_association":"COLLABORATOR","user":{"login":"maintainer"}}]]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
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

	if err := pollPRComments(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":99,"body":"/galley rerun reply please","html_url":"https://github.com/example/galley/pull/123#issuecomment-99","author_association":"OWNER","user":{"login":"owner"}}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
cat >> `+marker+`
echo '{"id":100}'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root, ReplyPRComments: true, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"body":"Galley requeued`) {
		t.Fatalf("reply command got %q", string(data))
	}
}

func TestPollPRCommentsContinuesAfterReplyFailure(t *testing.T) {
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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":42,"body":"/galley rerun first fix","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","author_association":"COLLABORATOR","user":{"login":"maintainer"}},{"id":43,"body":"/galley rerun second fix","html_url":"https://github.com/example/galley/pull/123#issuecomment-43","author_association":"COLLABORATOR","user":{"login":"maintainer"}}]]'
elif [ "$1" = "api" ]; then
echo reply-failed >&2
exit 1
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root, ReplyPRComments: true, GHBin: ghBin}.withDefaults()); err == nil {
		t.Fatal("expected reply error")
	}
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	requeued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(requeued.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("processed comments missing 42: %#v", requeued.PR.ProcessedCommentIDs)
	}
	if slices.Contains(requeued.PR.ProcessedCommentIDs, "43") {
		t.Fatalf("requeue should stop after first command, got processed comments %#v", requeued.PR.ProcessedCommentIDs)
	}
	if !hasRevisionRequest(requeued.RevisionRequests, "pr-comment-42") {
		t.Fatalf("revision requests missing pr-comment-42: %#v", requeued.RevisionRequests)
	}
	if hasRevisionRequest(requeued.RevisionRequests, "pr-comment-43") {
		t.Fatalf("requeue should stop after first command, got revision requests %#v", requeued.RevisionRequests)
	}
}

func TestPollPRCommentsRecordsFailedRequeueForManualRecovery(t *testing.T) {
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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":42,"body":"/galley rerun retry after queue clears","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","author_association":"COLLABORATOR","user":{"login":"collab"}}]]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err == nil {
		t.Fatal("expected requeue destination error")
	}
	stillDone, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stillDone.PR.ProcessedCommentIDs, "42") {
		t.Fatalf("comment should be marked processed after preserving recovery request: %#v", stillDone.PR.ProcessedCommentIDs)
	}
	if !hasRevisionRequest(stillDone.RevisionRequests, "pr-comment-42") {
		t.Fatalf("revision request missing after failed requeue: %#v", stillDone.RevisionRequests)
	}
	if len(stillDone.Risks) == 0 || !strings.Contains(stillDone.Risks[len(stillDone.Risks)-1].Detail, "could not requeue") {
		t.Fatalf("requeue failure risk missing: %#v", stillDone.Risks)
	}
}

func hasRevisionRequest(values []task.RevisionRequest, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func TestPollPRCommentsIgnoresUntrustedAuthor(t *testing.T) {
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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":77,"body":"/galley rerun please run my code","html_url":"https://github.com/example/galley/pull/123#issuecomment-77","author_association":"MEMBER","user":{"login":"org-member"}}]]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	if err := pollPRComments(context.Background(), Options{Root: root, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	stillDone, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if stillDone.Status != "pr_opened" {
		t.Fatalf("status got %q", stillDone.Status)
	}
	if !slices.Contains(stillDone.PR.ProcessedCommentIDs, "77") {
		t.Fatalf("untrusted comment should be marked processed after ignore: %#v", stillDone.PR.ProcessedCommentIDs)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 0)
}
