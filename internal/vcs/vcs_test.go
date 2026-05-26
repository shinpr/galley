package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodePRCommentsSlurpPages(t *testing.T) {
	t.Parallel()
	comments, err := DecodePRComments(`[[{"id":1,"body":"first"}],[{"id":2,"body":"second"}]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("comments got %#v", comments)
	}
}

func TestFetchPRCommentsReadsFullPaginatedResponse(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	binDir := t.TempDir()
	fakeGH := filepath.Join(binDir, "gh")
	longBody := strings.Repeat("x", 70*1024)
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
printf '%s' '[[{"id":1,"body":"`+longBody+`","html_url":"https://github.com/example/galley/pull/1#issuecomment-1","user":{"login":"owner"}}]]'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	comments, err := FetchPRComments(t.Context(), Binaries{}, t.TempDir(), "https://github.com/example/galley/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != longBody {
		t.Fatal("large PR comment response was not decoded intact")
	}
}

func TestPostPRCommentSendsJSONBodyOnStdin(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	binDir := t.TempDir()
	bodyPath := filepath.Join(t.TempDir(), "body.json")
	fakeGH := filepath.Join(binDir, "gh")
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
cat > `+bodyPath+`
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := PostPRComment(t.Context(), Binaries{}, t.TempDir(), "https://github.com/example/galley/pull/1", "@owner please check"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"body":"@owner please check"`) {
		t.Fatalf("body was not posted as JSON stdin: %s", body)
	}
}

func TestFetchPRURLForCurrentBranchReturnsURL(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	binDir := t.TempDir()
	fakeGH := filepath.Join(binDir, "gh")
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
printf '%s\n' 'https://github.com/example/galley/pull/42'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	url, err := FetchPRURLForCurrentBranch(t.Context(), Binaries{}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/example/galley/pull/42" {
		t.Fatalf("url got %q", url)
	}
}

// TestFetchPRURLForCurrentBranchReturnsEmptyWhenNoPR pins the recovery
// contract: when gh exits non-zero because no PR exists for the current
// branch, the function returns ("", nil) so the caller surfaces the
// original create-failure rather than the probe error.
func TestFetchPRURLForCurrentBranchReturnsEmptyWhenNoPR(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	binDir := t.TempDir()
	fakeGH := filepath.Join(binDir, "gh")
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
echo 'no pull requests found for branch "agent/x"' >&2
exit 1
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	url, err := FetchPRURLForCurrentBranch(t.Context(), Binaries{}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("expected nil error for missing PR, got %v", err)
	}
	if url != "" {
		t.Fatalf("url got %q, want empty", url)
	}
}

func TestFetchPRURLForCurrentBranchReturnsErrorForOtherFailures(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	cases := []struct {
		name   string
		stderr string
	}{
		{name: "network", stderr: "HTTP 502 bad gateway"},
		{name: "auth", stderr: "authentication failed: refresh gh auth"},
		{name: "incidental text", stderr: "error: no pull requests found while reading cached metadata"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			fakeGH := filepath.Join(binDir, "gh")
			if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
echo '`+tc.stderr+`' >&2
exit 1
`), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if _, err := FetchPRURLForCurrentBranch(t.Context(), Binaries{}, t.TempDir(), t.TempDir()); err == nil {
				t.Fatal("expected error for non-missing-PR failure")
			}
		})
	}
}

func TestFetchPRStateReadsFullResponse(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	binDir := t.TempDir()
	fakeGH := filepath.Join(binDir, "gh")
	padding := strings.Repeat(" ", 70*1024)
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
printf '%s' '{"state":"open","merged":false,"padding":"`+padding+`"}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	state, err := FetchPRState(t.Context(), Binaries{}, t.TempDir(), "https://github.com/example/galley/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "open" || state.Merged {
		t.Fatalf("state got %#v", state)
	}
}

func TestFetchPRAuthorLoginReturnsLoginOnSuccess(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	binDir := t.TempDir()
	fakeGH := filepath.Join(binDir, "gh")
	if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
printf '%s' '{"user":{"login":"pr-author"}}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	login, err := FetchPRAuthorLogin(t.Context(), Binaries{}, t.TempDir(), "https://github.com/example/galley/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if login != "pr-author" {
		t.Fatalf("login got %q, want %q", login, "pr-author")
	}
}

// TestFetchPRAuthorLoginRejectsEmptyLogin guards the fail-closed behavior for
// PR comment authorization: if GitHub responds with a payload that has no
// user.login (missing user, empty login, or null login), FetchPRAuthorLogin
// must return a non-nil error so callers record the pr-author-lookup risk
// and downstream /galley PR comment trust checks fall back to rejection
// instead of silently accepting an empty author.
func TestFetchPRAuthorLoginRejectsEmptyLogin(t *testing.T) {
	skipPOSIXFakeGHOnWindows(t)
	cases := []struct {
		name    string
		payload string
	}{
		{name: "empty login", payload: `{"user":{"login":""}}`},
		{name: "missing login field", payload: `{"user":{}}`},
		{name: "missing user", payload: `{}`},
		{name: "null user", payload: `{"user":null}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			fakeGH := filepath.Join(binDir, "gh")
			if err := os.WriteFile(fakeGH, []byte(`#!/bin/sh
printf '%s' '`+tc.payload+`'
`), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			login, err := FetchPRAuthorLogin(t.Context(), Binaries{}, t.TempDir(), "https://github.com/example/galley/pull/1")
			if err == nil {
				t.Fatalf("expected error for empty login payload %q, got login=%q", tc.payload, login)
			}
			if login != "" {
				t.Fatalf("expected empty login on failure, got %q", login)
			}
			if !strings.Contains(err.Error(), "empty user.login") {
				t.Fatalf("error should mention empty user.login, got %v", err)
			}
		})
	}
}

func skipPOSIXFakeGHOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell fake gh executable")
	}
}

func TestStagePathsForReviewTreatsPathspecMagicAsLiteral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain ':'")
	}
	repo := t.TempDir()
	runDir := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "a@example.test")
	runGit(t, repo, "config", "user.name", "A")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, ":(glob)*"), []byte("magic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := StagePathsForReview(t.Context(), Binaries{}, repo, runDir, []string{":(glob)*"}); err != nil {
		t.Fatal(err)
	}

	got := strings.TrimSpace(string(runGitOutput(t, repo, "diff", "--cached", "--name-only")))
	if got != ":(glob)*" {
		t.Fatalf("staged paths got %q, want only literal magic filename", got)
	}
}

func TestAddPathsTreatsPathspecMagicAsLiteral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain ':'")
	}
	repo := t.TempDir()
	runDir := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "a@example.test")
	runGit(t, repo, "config", "user.name", "A")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, ":(glob)*"), []byte("magic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AddPaths(t.Context(), Binaries{}, repo, runDir, []string{":(glob)*"}); err != nil {
		t.Fatal(err)
	}

	got := strings.TrimSpace(string(runGitOutput(t, repo, "diff", "--cached", "--name-only")))
	if got != ":(glob)*" {
		t.Fatalf("staged paths got %q, want only literal magic filename", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

func TestExtractFirstHTTPSURLTrimsTrailingPunctuation(t *testing.T) {
	t.Parallel()
	got := ExtractFirstHTTPSURL("created (https://github.com/example/galley/pull/123).")
	want := "https://github.com/example/galley/pull/123"
	if got != want {
		t.Fatalf("url got %q, want %q", got, want)
	}
}

func TestParseGitHubPRURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "valid", raw: "https://github.com/example/galley/pull/123"},
		{name: "missing pull", raw: "https://github.com/example/galley/issues/123", wantErr: true},
		{name: "short", raw: "https://github.com/example/galley", wantErr: true},
		{name: "wrong host", raw: "https://attacker.example/example/galley/pull/123", wantErr: true},
		{name: "non numeric", raw: "https://github.com/example/galley/pull/abc", wantErr: true},
		{name: "invalid url", raw: "://bad", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := ParseGitHubPRURL(tt.raw)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestExtractFirstHTTPSURL(t *testing.T) {
	t.Parallel()
	stdout := "warning: something happened\nhttps://github.com/example/repo/pull/123\n"
	url := ExtractFirstHTTPSURL(stdout)
	if url != "https://github.com/example/repo/pull/123" {
		t.Fatalf("url got %q", url)
	}
}
