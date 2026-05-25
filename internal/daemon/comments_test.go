package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/queue"
	"github.com/shinpr/galley/internal/task"
	"github.com/shinpr/galley/internal/vcs"
)

func TestParsePRCommandAcceptedForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "rerun alias keeps backward compatible reason", body: "/galley rerun fix X", want: "fix X"},
		{name: "requeue alias keeps backward compatible reason", body: "/galley requeue tighten tests", want: "tighten tests"},
		{name: "free-form request becomes the reason", body: "/galley please re-run with verbose", want: "please re-run with verbose"},
		{name: "bare /galley falls back to default reason", body: "/galley", want: "PR comment requested another Galley run."},
		{name: "bare /galley with body block uses block as reason", body: "/galley\n\nMore context.", want: "More context."},
		{name: "rerun alias with leading whitespace is trimmed", body: "  \n/galley rerun fix Y", want: "fix Y"},
		{name: "rerun alias exact body falls back to default reason", body: "/galley rerun", want: "PR comment requested another Galley run."},
		{name: "rerun alias with same-line reason and trailing block joins with blank line", body: "/galley rerun fix X\nMore context", want: "fix X\n\nMore context"},
		{name: "free-form request with same-line reason and trailing block joins with blank line", body: "/galley please fix X\nMore context", want: "please fix X\n\nMore context"},
		{name: "requeue alias with same-line reason and trailing block joins with blank line", body: "/galley requeue tighten tests\nfocus on edge cases", want: "tighten tests\n\nfocus on edge cases"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			command, ok := parsePRCommand(vcs.PRComment{ID: 123, Body: tc.body})
			if !ok {
				t.Fatalf("expected command for %q", tc.body)
			}
			if command.CommentID != "123" {
				t.Fatalf("comment id got %q", command.CommentID)
			}
			if command.Reason != tc.want {
				t.Fatalf("reason got %q want %q", command.Reason, tc.want)
			}
		})
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

func TestParsePRCommandRejectsNonLeadingOrMalformedBodies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{name: "leading prose with /galley rerun mid-line", body: "Looks good, /galley rerun fix X"},
		{name: "/galley command on second line is ignored", body: "Plain text on the first line.\n/galley rerun fix X"},
		{name: "/galley:galley does not match any prefix", body: "/galley:galley do something"},
		{name: "/galleyfoo does not match any prefix", body: "/galleyfoo bar"},
		{name: "empty body is ignored", body: ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if cmd, ok := parsePRCommand(vcs.PRComment{ID: 7, Body: tc.body}); ok {
				t.Fatalf("expected reject for %q, got %#v", tc.body, cmd)
			}
		})
	}
}

func TestPollPRCommentsRequeuesTaskOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
	echo '[[{"id":42,"body":"/galley rerun tighten tests","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","user":{"login":"maintainer"}}]]'
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

func TestPollPRCommentsRequeuesMemberPRAuthor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, false)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "org-member"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":78,"body":"/galley rerun please run my code","html_url":"https://github.com/example/galley/pull/123#issuecomment-78","user":{"login":"org-member"}}]]'
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
	if !slices.Contains(requeued.PR.ProcessedCommentIDs, "78") {
		t.Fatalf("processed comments got %#v", requeued.PR.ProcessedCommentIDs)
	}
	if len(requeued.RevisionRequests) != 1 || requeued.RevisionRequests[0].Text != "please run my code" {
		t.Fatalf("revision requests got %#v", requeued.RevisionRequests)
	}
}

func TestPollPRCommentsRequeuesNeedsSupervisorReviewOpenPR(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, false)
	failedPath := filepath.Join(root, "tasks", "failed", "task.yaml")
	writeDaemonTask(t, failedPath, repo)
	loaded, err := task.Load(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "needs_supervisor_review"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "author"
	if err := task.Save(failedPath, loaded); err != nil {
		t.Fatal(err)
	}
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":88,"body":"/galley address supervisor feedback","html_url":"https://github.com/example/galley/pull/123#issuecomment-88","user":{"login":"author"}}]]'
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
	if !slices.Contains(requeued.PR.ProcessedCommentIDs, "88") {
		t.Fatalf("processed comments got %#v", requeued.PR.ProcessedCommentIDs)
	}
	if len(requeued.RevisionRequests) != 1 || requeued.RevisionRequests[0].Text != "address supervisor feedback" {
		t.Fatalf("revision requests got %#v", requeued.RevisionRequests)
	}
	if _, err := os.Stat(failedPath); !os.IsNotExist(err) {
		t.Fatalf("failed task should be moved to queued, err=%v", err)
	}
}

func TestPollPRCommentsPostsReply(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "owner"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "posted")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":99,"body":"/galley rerun reply please","html_url":"https://github.com/example/galley/pull/123#issuecomment-99","user":{"login":"owner"}}]]'
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
	body := string(data)
	if !strings.Contains(body, "Galley requeued task `") {
		t.Fatalf("reply command got %q", body)
	}
	if !strings.Contains(body, "from this comment.") {
		t.Fatalf("reply command got %q", body)
	}
	if strings.Contains(body, "reply please") {
		t.Fatalf("reply must not echo the user-supplied reason text: %q", body)
	}
	if strings.Contains(body, "comment 99") {
		t.Fatalf("reply must not include the comment id: %q", body)
	}
}

func TestPollPRCommentsReplyOmitsReasonAndCommentID(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "owner"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "posted")
	// Free-form request with a long body and special characters that must not leak into the reply.
	wantReason := `please re-run focusing on the AC3 reply path -- trim reply verbosity & keep <tags> intact.`
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":555,"body":"/galley `+wantReason+`","html_url":"https://github.com/example/galley/pull/123#issuecomment-555","user":{"login":"owner"}}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
cat >> `+marker+`
echo '{"id":556}'
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
	body := string(data)
	if !strings.Contains(body, "Galley requeued task `") || !strings.Contains(body, "from this comment.") {
		t.Fatalf("reply must use the concise success template, got %q", body)
	}
	for _, fragment := range []string{
		"please re-run focusing",
		"trim reply verbosity",
		"<tags>",
		"comment 555",
	} {
		if strings.Contains(body, fragment) {
			t.Fatalf("reply must not contain %q: %q", fragment, body)
		}
	}

	// Reason still travels to the executor through RevisionRequest.Text.
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	requeued, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(requeued.RevisionRequests) != 1 {
		t.Fatalf("revision requests got %#v", requeued.RevisionRequests)
	}
	if requeued.RevisionRequests[0].Text != wantReason {
		t.Fatalf("revision request text got %q want %q", requeued.RevisionRequests[0].Text, wantReason)
	}
}

func TestPollPRCommentsReplyForQueuedOrRunningTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	queuedPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	writeDaemonTask(t, queuedPath, repo)
	loaded, err := task.Load(queuedPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "queued"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "owner"
	if err := task.Save(queuedPath, loaded); err != nil {
		t.Fatal(err)
	}
	// pollPRComments only scans tasks/done and tasks/failed; emulate the
	// queued/running path through processTaskPRComments directly so we can
	// assert the reply template without changing scanned states.
	marker := filepath.Join(t.TempDir(), "posted")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":808,"body":"/galley please nudge it","html_url":"https://github.com/example/galley/pull/123#issuecomment-808","user":{"login":"owner"}}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
cat >> `+marker+`
echo '{"id":809}'
else
echo unexpected-gh >&2
exit 1
fi
`)
	opts := Options{Root: root, ReplyPRComments: true, GHBin: ghBin}.withDefaults()
	if err := processTaskPRComments(context.Background(), opts, queuedPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "Galley noted this comment; task is already queued.") {
		t.Fatalf("queued reply got %q", body)
	}
	if strings.Contains(body, "nudge it") || strings.Contains(body, "comment 808") {
		t.Fatalf("queued reply must not echo reason or comment id: %q", body)
	}
}

func TestPollPRCommentsReplyForNonPRAuthor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "pr-author"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "posted")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":222,"body":"/galley please run it","html_url":"https://github.com/example/galley/pull/123#issuecomment-222","user":{"login":"org-member"}}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
cat >> `+marker+`
echo '{"id":223}'
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
	body := string(data)
	if !strings.Contains(body, "only the pull request author can run Galley from PR comments") {
		t.Fatalf("non-author reply got %q", body)
	}
	if strings.Contains(body, "please run it") || strings.Contains(body, "comment 222") {
		t.Fatalf("non-author reply must not echo reason or comment id: %q", body)
	}
}

func TestPollPRCommentsContinuesAfterReplyFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
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
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":42,"body":"/galley rerun first fix","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","user":{"login":"maintainer"}},{"id":43,"body":"/galley rerun second fix","html_url":"https://github.com/example/galley/pull/123#issuecomment-43","user":{"login":"maintainer"}}]]'
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
	writeDaemonEnvironmentProfile(t, root, repo, true, false)
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
	loaded.PR.AuthorLogin = "collab"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":42,"body":"/galley rerun retry after queue clears","html_url":"https://github.com/example/galley/pull/123#issuecomment-42","user":{"login":"collab"}}]]'
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

func TestPollPRCommentsIgnoresNonPRAuthor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, false)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "pr-author"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo '[[{"id":77,"body":"/galley rerun please run my code","html_url":"https://github.com/example/galley/pull/123#issuecomment-77","user":{"login":"org-member"}}]]'
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
		t.Fatalf("non-author comment should be marked processed after ignore: %#v", stillDone.PR.ProcessedCommentIDs)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 0)
}

func TestPollPRCommentsRejectsNonAuthor(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	loaded.PR.AuthorLogin = "pr-author"
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "posted")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":501,"body":"/galley rerun please re-run for me","html_url":"https://github.com/example/galley/pull/123#issuecomment-501","user":{"login":"other-maintainer"}}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
cat >> `+marker+`
echo '{"id":502}'
else
echo unexpected-gh >&2
exit 1
fi
`)
	if err := pollPRComments(context.Background(), Options{Root: root, ReplyPRComments: true, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	stillDone, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if stillDone.Status != "pr_opened" {
		t.Fatalf("status got %q want pr_opened", stillDone.Status)
	}
	if !slices.Contains(stillDone.PR.ProcessedCommentIDs, "501") {
		t.Fatalf("non-author comment should be marked processed: %#v", stillDone.PR.ProcessedCommentIDs)
	}
	if hasRevisionRequest(stillDone.RevisionRequests, "pr-comment-501") {
		t.Fatalf("non-author comment must not produce revision request: %#v", stillDone.RevisionRequests)
	}
	assertGlobCount(t, filepath.Join(root, "tasks", "queued", "*.yaml"), 0)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "only the pull request author can run Galley from PR comments") {
		t.Fatalf("non-author reply got %q", body)
	}
	for _, fragment := range []string{
		"please re-run for me",
		"comment 501",
		"other-maintainer",
	} {
		if strings.Contains(body, fragment) {
			t.Fatalf("non-author reply must not echo %q: %q", fragment, body)
		}
	}
}

func TestPollPRCommentsRejectsWhenPRAuthorLoginEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, true)
	donePath := filepath.Join(root, "tasks", "done", "task.yaml")
	writeDaemonTask(t, donePath, repo)
	loaded, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = "pr_opened"
	loaded.PR.URL = "https://github.com/example/galley/pull/123"
	loaded.PR.Status = "open"
	// Older task file without persisted PR author: fail closed even for an
	// OWNER-association comment.
	if err := task.Save(donePath, loaded); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "posted")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ] && [ "$3" = "--paginate" ]; then
echo '[[{"id":611,"body":"/galley rerun no author known","html_url":"https://github.com/example/galley/pull/123#issuecomment-611","user":{"login":"someone"}}]]'
elif [ "$1" = "api" ]; then
echo "$*" > `+marker+`
cat >> `+marker+`
echo '{"id":612}'
else
echo unexpected-gh >&2
exit 1
fi
`)
	if err := pollPRComments(context.Background(), Options{Root: root, ReplyPRComments: true, GHBin: ghBin}.withDefaults()); err != nil {
		t.Fatal(err)
	}
	stillDone, err := task.Load(donePath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stillDone.PR.ProcessedCommentIDs, "611") {
		t.Fatalf("unknown-author comment should be marked processed: %#v", stillDone.PR.ProcessedCommentIDs)
	}
	if hasRevisionRequest(stillDone.RevisionRequests, "pr-comment-611") {
		t.Fatalf("unknown-author comment must not produce revision request: %#v", stillDone.RevisionRequests)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "only the pull request author can run Galley from PR comments") {
		t.Fatalf("unknown-author reply got %q", body)
	}
}

func TestPRCommentMatchesPRAuthor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		comment       vcs.PRComment
		prAuthorLogin string
		want          bool
	}{
		{
			name:          "matching PR author is allowed",
			comment:       vcs.PRComment{User: vcs.PRCommentUser{Login: "author"}},
			prAuthorLogin: "author",
			want:          true,
		},
		{
			name:          "different login is rejected",
			comment:       vcs.PRComment{User: vcs.PRCommentUser{Login: "other"}},
			prAuthorLogin: "author",
			want:          false,
		},
		{
			name:          "empty comment author login is rejected",
			comment:       vcs.PRComment{User: vcs.PRCommentUser{Login: ""}},
			prAuthorLogin: "author",
			want:          false,
		},
		{
			name:          "empty PR author login fails closed",
			comment:       vcs.PRComment{User: vcs.PRCommentUser{Login: "author"}},
			prAuthorLogin: "",
			want:          false,
		},
		{
			name:          "login match is case-insensitive",
			comment:       vcs.PRComment{User: vcs.PRCommentUser{Login: "Author"}},
			prAuthorLogin: "author",
			want:          true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := prCommentMatchesPRAuthor(tc.comment, tc.prAuthorLogin); got != tc.want {
				t.Fatalf("author match got %v want %v", got, tc.want)
			}
		})
	}
}

func TestIsActionableForPRCommentPoll(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		task task.Task
		want bool
	}{
		{
			name: "pr_opened task with open PR is actionable",
			task: task.Task{Status: "pr_opened", PR: task.PR{URL: "https://github.com/example/repo/pull/1", Status: "open"}},
			want: true,
		},
		{
			name: "queued task with open PR is actionable for direct callers",
			task: task.Task{Status: "queued", PR: task.PR{URL: "https://github.com/example/repo/pull/2", Status: "open"}},
			want: true,
		},
		{
			name: "running task with open PR is actionable for direct callers",
			task: task.Task{Status: "running", PR: task.PR{URL: "https://github.com/example/repo/pull/3", Status: "open"}},
			want: true,
		},
		{
			name: "needs supervisor review task with open PR is actionable",
			task: task.Task{Status: "needs_supervisor_review", PR: task.PR{URL: "https://github.com/example/repo/pull/11", Status: "open"}},
			want: true,
		},
		{
			name: "missing PR URL is non-actionable",
			task: task.Task{Status: "pr_opened", PR: task.PR{URL: "", Status: "open"}},
			want: false,
		},
		{
			name: "merged task status is non-actionable even with PR URL",
			task: task.Task{Status: "merged", PR: task.PR{URL: "https://github.com/example/repo/pull/4", Status: "merged"}},
			want: false,
		},
		{
			name: "closed task status is non-actionable",
			task: task.Task{Status: "closed", PR: task.PR{URL: "https://github.com/example/repo/pull/5", Status: "closed"}},
			want: false,
		},
		{
			name: "archived task status is non-actionable",
			task: task.Task{Status: "archived", PR: task.PR{URL: "https://github.com/example/repo/pull/6", Status: "open"}},
			want: false,
		},
		{
			name: "accepted task status without PR open is non-actionable",
			task: task.Task{Status: "accepted", PR: task.PR{URL: "https://github.com/example/repo/pull/7", Status: ""}},
			want: false,
		},
		{
			name: "pr_opened task with closed PR is non-actionable",
			task: task.Task{Status: "pr_opened", PR: task.PR{URL: "https://github.com/example/repo/pull/8", Status: "closed"}},
			want: false,
		},
		{
			name: "PR status open is matched case-insensitively",
			task: task.Task{Status: "pr_opened", PR: task.PR{URL: "https://github.com/example/repo/pull/9", Status: "OPEN"}},
			want: true,
		},
		{
			name: "failed task status is non-actionable",
			task: task.Task{Status: "failed", PR: task.PR{URL: "https://github.com/example/repo/pull/10", Status: "open"}},
			want: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isActionableForPRCommentPoll(tc.task); got != tc.want {
				t.Fatalf("isActionableForPRCommentPoll got %v want %v", got, tc.want)
			}
		})
	}
}

// TestPollPRCommentsSkipsHistoricalNonActionableTasks proves that the daemon
// skips merged, closed, PR-less, and non-open PR tasks before touching either
// the repository profile or GitHub. The fake gh binary fails on any
// invocation, and an intentionally invalid environment profile path forces
// loadTaskProfiles to fail if it is ever reached, so any regression that
// re-introduces per-task profile or comment work surfaces as a test error.
func TestPollPRCommentsSkipsHistoricalNonActionableTasks(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not call writeDaemonEnvironmentProfile so the resolved
	// profile path is empty; combined with a forced bogus EnvironmentProfileFile
	// below, profile loading would fail if the eligibility gate did not skip
	// these tasks first.
	cases := []struct {
		name     string
		status   string
		prStatus string
		prURL    string
	}{
		{name: "merged-task", status: "merged", prStatus: "merged", prURL: "https://github.com/example/galley/pull/100"},
		{name: "closed-task", status: "closed", prStatus: "closed", prURL: "https://github.com/example/galley/pull/101"},
		{name: "pr-less-accepted", status: "accepted", prStatus: "", prURL: ""},
		{name: "pr-opened-but-pr-closed", status: "pr_opened", prStatus: "closed", prURL: "https://github.com/example/galley/pull/102"},
		{name: "failed-task-open-pr", status: "failed", prStatus: "open", prURL: "https://github.com/example/galley/pull/103"},
	}
	for _, tc := range cases {
		tc := tc
		donePath := filepath.Join(root, "tasks", "done", "task-"+tc.name+".yaml")
		writeDaemonTask(t, donePath, repo)
		loaded, err := task.Load(donePath)
		if err != nil {
			t.Fatal(err)
		}
		loaded.Status = tc.status
		loaded.PR.URL = tc.prURL
		loaded.PR.Status = tc.prStatus
		loaded.PR.AuthorLogin = "owner"
		if err := task.Save(donePath, loaded); err != nil {
			t.Fatal(err)
		}
	}

	// Any gh invocation is a regression: non-actionable tasks must not fetch
	// PR comments.
	ghBin := writeFakeCommand(t, "gh", `echo "gh must not be called for non-actionable tasks: $*" >&2
exit 1
`)
	// Pointing EnvironmentProfileFile at a non-existent path makes
	// loadTaskProfiles fail if the eligibility gate is bypassed. A passing
	// run proves the gate ran before profile loading.
	missingProfile := filepath.Join(t.TempDir(), "missing-environment.yaml")
	opts := Options{
		Root:                   root,
		GHBin:                  ghBin,
		EnvironmentProfileFile: missingProfile,
	}.withDefaults()
	if err := pollPRComments(context.Background(), opts); err != nil {
		t.Fatalf("non-actionable tasks must skip without error: %v", err)
	}
}

// TestPollPRCommentsActionableOnlyFetchesOpenPRTasks mixes a non-actionable
// done/closed task and an actionable done/pr_opened task with PR.Status="open"
// in the same poll, asserts that gh is called exactly once (for the open PR),
// and confirms that the closed PR is not contacted.
func TestPollPRCommentsActionableOnlyFetchesOpenPRTasks(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	if err := queue.EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	writeDaemonEnvironmentProfile(t, root, repo, true, false)

	closedPath := filepath.Join(root, "tasks", "done", "task-closed.yaml")
	writeDaemonTask(t, closedPath, repo)
	closed, err := task.Load(closedPath)
	if err != nil {
		t.Fatal(err)
	}
	closed.Status = "closed"
	closed.PR.URL = "https://github.com/example/galley/pull/100"
	closed.PR.Status = "closed"
	closed.PR.AuthorLogin = "owner"
	if err := task.Save(closedPath, closed); err != nil {
		t.Fatal(err)
	}

	activePath := filepath.Join(root, "tasks", "done", "task-active.yaml")
	writeDaemonTask(t, activePath, repo)
	active, err := task.Load(activePath)
	if err != nil {
		t.Fatal(err)
	}
	active.Status = "pr_opened"
	active.PR.URL = "https://github.com/example/galley/pull/200"
	active.PR.Status = "open"
	active.PR.AuthorLogin = "owner"
	if err := task.Save(activePath, active); err != nil {
		t.Fatal(err)
	}

	counter := filepath.Join(t.TempDir(), "gh-calls.log")
	ghBin := writeFakeCommand(t, "gh", `if [ "$1" = "api" ]; then
echo "$*" >> `+counter+`
echo '[]'
else
echo unexpected-gh >&2
exit 1
fi
`)

	opts := Options{Root: root, GHBin: ghBin}.withDefaults()
	if err := pollPRComments(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("expected gh to be called for the actionable open PR task: %v", err)
	}
	body := string(data)
	// gh api translates the PR comments endpoint to issues/<n>/comments, so
	// the PR number is the stable substring to assert against the captured
	// argv log.
	if strings.Contains(body, "issues/100/") {
		t.Fatalf("closed PR must not be fetched: %q", body)
	}
	if !strings.Contains(body, "issues/200/") {
		t.Fatalf("actionable open PR must be fetched: %q", body)
	}
	if got := strings.Count(strings.TrimRight(body, "\n"), "\n") + 1; got != 1 {
		t.Fatalf("expected exactly one gh api invocation, got %d in %q", got, body)
	}
}

func writeDaemonEnvironmentProfile(t *testing.T, root, repo string, pollComments, replyComments bool) {
	t.Helper()
	_, _, environmentPath, err := galleyhome.RepoProfilePaths(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `id: "test-env"
cwd: ` + strconv.Quote(repo) + `
commands:
  test: "true"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
pr:
  enabled: true
  base: "main"
  comments:
    enabled: ` + boolYAML(pollComments) + `
    reply: ` + boolYAML(replyComments) + `
worktree:
  cleanup: true
`
	if err := os.WriteFile(environmentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func boolYAML(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
