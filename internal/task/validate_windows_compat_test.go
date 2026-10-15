package task

import (
	"runtime"
	"strings"
	"testing"
)

// TestValidateWorktreePathAcceptsSeparatorVariants checks that the validator
// accepts both `/` and `\` variants of the same logical sibling worktree.path
// on every OS; a native `filepath.Clean` + `strings.HasPrefix(clean, "../")`
// check would reject the backslash form on Windows.
func TestValidateWorktreePathAcceptsSeparatorVariants(t *testing.T) {
	t.Parallel()
	siblingPaths := []string{
		"../repo.worktrees/task-test",
		"..\\repo.worktrees\\task-test",
		"../smile.worktrees/task-XXX",
		"..\\smile.worktrees\\task-XXX",
		"../abc",
		"..\\abc",
	}
	for _, p := range siblingPaths {
		p := p
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			task := validTask(t)
			task.Worktree.Path = p
			result := ValidateStructural(task)
			if !result.Valid() {
				t.Fatalf("expected %q to be accepted as a sibling worktree path, got errors %#v", p, result.Errors)
			}
		})
	}
}

// TestValidateWorktreePathRejectsInternalAndDeepParent confirms the same
// validator still rejects repo-internal worktrees and deep parent traversal
// regardless of separator, including the backslash form that a native
// `strings.HasPrefix(clean, "../../")` check would miss on Windows.
func TestValidateWorktreePathRejectsInternalAndDeepParent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want string
	}{
		{path: "worktrees/test", want: "must point to a sibling path outside scope.cwd"},
		{path: "worktrees\\test", want: "must point to a sibling path outside scope.cwd"},
		{path: ".galley-worktrees/task-XXX", want: "must point to a sibling path outside scope.cwd"},
		{path: ".galley-worktrees\\task-XXX", want: "must point to a sibling path outside scope.cwd"},
		{path: "../../worktrees/test", want: `contains parent traversal path "../../worktrees/test"`},
		{path: "..\\..\\worktrees\\test", want: `contains parent traversal path "..\\..\\worktrees\\test"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			task := validTask(t)
			task.Worktree.Path = tc.path
			result := ValidateStructural(task)
			if result.Valid() {
				t.Fatalf("expected %q to be rejected", tc.path)
			}
			if !containsAny(result.Errors, tc.want) {
				t.Fatalf("expected error containing %q, got %#v", tc.want, result.Errors)
			}
		})
	}
}

// TestValidateRelativePathDetectsBackslashParentTraversal covers the AC2
// backslash case: `validateRelativePath` (used by scope.allowed_paths,
// scope.forbidden_paths, files[].source/destination, and
// preflight.acceptance_skeleton paths) must reject parent traversal for both
// `../` and `..\` forms. On Windows `filepath.Clean("..\\foo")` returns
// `..\\foo`, so a `strings.HasPrefix(clean, "../")` check alone would miss the
// backslash form.
func TestValidateRelativePathDetectsBackslashParentTraversal(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Scope.AllowedPaths = []string{"..\\escape"}
	result := ValidateStructural(task)
	if result.Valid() {
		t.Fatalf("expected backslash parent traversal in scope.allowed_paths to be rejected")
	}
	if !containsAny(result.Errors, `contains parent traversal path "..\\escape"`) {
		t.Fatalf("expected parent traversal error, got %#v", result.Errors)
	}
}

// windowsAbsolutePath is a host-foreign absolute form; isLogicalAbsolutePath's
// own test owns the full set of recognized shapes.
const windowsAbsolutePath = "C:\\workspace\\notes.md"

// TestValidateRelativePathRejectsWindowsAbsoluteFormsOnNonWindowsHost proves
// that scope.allowed_paths, scope.forbidden_paths, files[].source,
// files[].destination, and the preflight.acceptance_skeleton paths all reject
// Windows-style absolute forms when the validator runs on a non-Windows host.
func TestValidateRelativePathRejectsWindowsAbsoluteFormsOnNonWindowsHost(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows-native absolute files[].source values are allowed; this regression test is for host-foreign Windows paths on non-Windows hosts")
	}

	type mutator struct {
		name    string
		mutate  func(*Task, string)
		wantSub string
	}

	mutators := []mutator{
		{
			name: "scope.allowed_paths",
			mutate: func(task *Task, p string) {
				task.Scope.AllowedPaths = []string{p}
			},
			wantSub: "scope.allowed_paths contains absolute path",
		},
		{
			name: "scope.forbidden_paths",
			mutate: func(task *Task, p string) {
				task.Scope.ForbiddenPaths = []string{p}
			},
			wantSub: "scope.forbidden_paths contains absolute path",
		},
		{
			name: "files[].source",
			mutate: func(task *Task, p string) {
				task.Files = []InputFile{{Source: p, Destination: "internal/task/note.md"}}
			},
			wantSub: "files[0].source contains absolute path",
		},
		{
			name: "files[].destination",
			mutate: func(task *Task, p string) {
				task.Files = []InputFile{{Source: "plan.md", Destination: p}}
			},
			wantSub: "files[0].destination contains absolute path",
		},
		{
			name: "preflight.acceptance_skeleton.outputs[].path",
			mutate: func(task *Task, p string) {
				task.Preflight = &Preflight{
					AcceptanceSkeleton: &AcceptanceSkeletonConfig{
						Enabled: true,
						Outputs: []AcceptanceSkeletonOutputDef{{
							ACID:    "AC1",
							Path:    p,
							Kind:    "skeleton",
							Purpose: "verify AC1",
						}},
					},
				}
			},
			wantSub: "preflight.acceptance_skeleton.outputs[0].path contains absolute path",
		},
	}

	for _, m := range mutators {
		m := m
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			task := validTask(t)
			m.mutate(&task, windowsAbsolutePath)
			result := ValidateStructural(task)
			if !containsAny(result.Errors, m.wantSub) {
				t.Fatalf("expected error containing %q for %s, got %#v", m.wantSub, m.name, result.Errors)
			}
		})
	}
}

// TestValidateWorktreePathRejectsWindowsAbsoluteForms covers the worktree
// absolute precheck: drive-letter and other host-foreign absolute forms must
// be rejected with the canonical "must be relative" error on non-Windows
// hosts, not fall through to validateWorktreePath's sibling-path check.
func TestValidateWorktreePathRejectsWindowsAbsoluteForms(t *testing.T) {
	t.Parallel()
	task := validTask(t)
	task.Worktree.Path = windowsAbsolutePath
	result := ValidateStructural(task)
	if !containsAny(result.Errors, "worktree.path must be relative") {
		t.Fatalf("expected worktree absolute error, got %#v", result.Errors)
	}
}

// TestIsLogicalAbsolutePathRecognizesWindowsAndPosixForms locks in the helper
// contract so future edits keep covering every host-foreign absolute shape on
// every OS while still accepting genuine relative paths.
func TestIsLogicalAbsolutePathRecognizesWindowsAndPosixForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		// Absolute forms — must be rejected on every host.
		{path: "/abs/path", want: true},
		{path: "\\foo", want: true},
		{path: "\\\\server\\share", want: true},
		{path: "C:\\workspace", want: true},
		{path: "C:/workspace", want: true},
		{path: "d:\\downloads", want: true},
		// Relative forms — must be accepted by the helper.
		{path: "internal/task", want: false},
		{path: "internal\\task", want: false},
		{path: "../repo.worktrees/task-test", want: false},
		{path: "..\\repo.worktrees\\task-test", want: false},
		{path: ".", want: false},
		{path: "./relative", want: false},
		{path: "id:value", want: false},
		{path: "", want: false},
	}
	for _, tc := range cases {
		if got := isLogicalAbsolutePath(tc.path); got != tc.want {
			t.Fatalf("isLogicalAbsolutePath(%q)=%v, want %v", tc.path, got, tc.want)
		}
	}
}

func containsAny(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}
