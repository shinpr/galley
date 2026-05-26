package runner

// GitArgs builds the argv prefix for a Galley-owned git invocation.
//
// AC7/AC8: every Galley-owned git invocation that may operate on a worktree
// path must include `core.longpaths=true` so worktree creation, status, add,
// diff, commit, push, and cleanup operations succeed on Windows hosts where
// task-generated paths can exceed the default MAX_PATH limit. Routing every
// argv through this helper guarantees the longpaths flag is applied without
// requiring each call site to remember the `-c core.longpaths=true` prefix.
//
// gitBin is the executable name or absolute path (callers fall back to
// "git" when their configured Binaries/Options leave it empty). The
// returned slice is intended to be passed as runner.Command.Argv.
//
// Example:
//
//	cmd := runner.Command{
//	    WorkDir: workDir,
//	    Argv:    runner.GitArgs(gitBin, "status", "--porcelain"),
//	}
//
// The `-c core.longpaths=true` flag is benign on macOS and Linux: git
// accepts the config override on every platform and the underlying
// filesystems do not impose the Windows MAX_PATH limit, so the flag is a
// no-op for non-Windows runs. Keeping the flag unconditional avoids
// reasoning about host OS at every call site and ensures that runs which
// cross host boundaries (e.g. tasks created on Windows and inspected on
// Unix) see consistent argv shapes in run evidence.
func GitArgs(gitBin string, args ...string) []string {
	bin := gitBin
	if bin == "" {
		bin = "git"
	}
	out := make([]string, 0, len(args)+3)
	out = append(out, bin, "-c", "core.longpaths=true")
	out = append(out, args...)
	return out
}
