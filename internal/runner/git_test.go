package runner

import (
	"reflect"
	"testing"
)

// TestGitArgsAppliesLongpathsFlag covers AC7/AC8: every Galley-owned git
// invocation built through the shared wrapper carries
// `-c core.longpaths=true` immediately after the binary name, regardless of
// the subcommand. New call sites get the flag automatically.
func TestGitArgsAppliesLongpathsFlag(t *testing.T) {
	cases := []struct {
		name   string
		gitBin string
		args   []string
		want   []string
	}{
		{
			name:   "status",
			gitBin: "git",
			args:   []string{"status", "--porcelain"},
			want:   []string{"git", "-c", "core.longpaths=true", "status", "--porcelain"},
		},
		{
			name:   "worktree_add",
			gitBin: "git",
			args:   []string{"-C", "/repo", "worktree", "add", "-b", "agent/task", "/work"},
			want:   []string{"git", "-c", "core.longpaths=true", "-C", "/repo", "worktree", "add", "-b", "agent/task", "/work"},
		},
		{
			name:   "worktree_remove",
			gitBin: "git",
			args:   []string{"-C", "/repo", "worktree", "remove", "--force", "/work"},
			want:   []string{"git", "-c", "core.longpaths=true", "-C", "/repo", "worktree", "remove", "--force", "/work"},
		},
		{
			name:   "add_pathspecs_from_stdin",
			gitBin: "git",
			args:   []string{"add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"},
			want:   []string{"git", "-c", "core.longpaths=true", "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"},
		},
		{
			name:   "diff_cached_binary",
			gitBin: "git",
			args:   []string{"diff", "--cached", "--binary"},
			want:   []string{"git", "-c", "core.longpaths=true", "diff", "--cached", "--binary"},
		},
		{
			name:   "commit_message",
			gitBin: "git",
			args:   []string{"commit", "-m", "msg"},
			want:   []string{"git", "-c", "core.longpaths=true", "commit", "-m", "msg"},
		},
		{
			name:   "push_origin_head",
			gitBin: "git",
			args:   []string{"push", "-u", "origin", "HEAD"},
			want:   []string{"git", "-c", "core.longpaths=true", "push", "-u", "origin", "HEAD"},
		},
		{
			name:   "custom_bin",
			gitBin: "/usr/local/bin/git",
			args:   []string{"status"},
			want:   []string{"/usr/local/bin/git", "-c", "core.longpaths=true", "status"},
		},
		{
			name:   "default_bin_when_empty",
			gitBin: "",
			args:   []string{"status"},
			want:   []string{"git", "-c", "core.longpaths=true", "status"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := GitArgs(tc.gitBin, tc.args...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("GitArgs(%q, %v) = %v, want %v", tc.gitBin, tc.args, got, tc.want)
			}
		})
	}
}

// TestGitArgsLongpathsFlagIsImmediatelyAfterBinary documents the invariant
// that the longpaths config override appears before any subcommand or
// `-C <path>` selector, which is required by git's argv parser: top-level
// `-c key=value` overrides must precede the subcommand.
func TestGitArgsLongpathsFlagIsImmediatelyAfterBinary(t *testing.T) {
	got := GitArgs("git", "-C", "/repo", "worktree", "remove", "--force", "/work")
	if len(got) < 4 {
		t.Fatalf("GitArgs produced too few args: %v", got)
	}
	if got[0] != "git" {
		t.Errorf("expected git binary first, got %q", got[0])
	}
	if got[1] != "-c" || got[2] != "core.longpaths=true" {
		t.Errorf("expected -c core.longpaths=true after binary, got %v", got[1:3])
	}
	if got[3] != "-C" {
		t.Errorf("expected subcommand selector after longpaths flag, got %q", got[3])
	}
}
