package task

import (
	"strings"
	"testing"
)

// TestValidateWorktreePathAcceptsSeparatorVariants covers the Windows
// regression where `filepath.Clean` produced `..\foo` and the
// `strings.HasPrefix(clean, "../")` check rejected every otherwise-valid
// sibling worktree.path on Windows. The normalized validator must accept
// both `/` and `\` variants for the same logical sibling path on every OS.
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
// regardless of separator. Pre-fix, the deep-parent rule used
// `strings.HasPrefix(clean, "../../")` which silently passed on Windows.
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

// TestValidateRelativePathDetectsBackslashParentTraversal covers the related
// AC2 regression: `validateRelativePath` (used by scope.allowed_paths,
// scope.forbidden_paths, files[].source/destination, and
// preflight.acceptance_skeleton paths) only rejected `../` parent traversal.
// On Windows `filepath.Clean("..\\foo")` returns `..\\foo`, so the old
// `strings.HasPrefix(clean, "../")` check silently allowed parent traversal
// for backslash inputs. After normalization the rule fires for both forms.
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

// TestPathAllowedByScopeNormalizesSeparators ensures the allowed-path
// containment check accepts YAML-authored forward-slash entries when the
// candidate path uses native Windows separators (and vice versa) so user
// authored `internal/task` allowed paths keep matching nested files no
// matter how the path was rendered before reaching the validator.
func TestPathAllowedByScopeNormalizesSeparators(t *testing.T) {
	t.Parallel()
	allowed := []string{"internal/task"}
	cases := []struct {
		path string
		want bool
	}{
		{path: "internal/task/files.go", want: true},
		{path: "internal\\task\\files.go", want: true},
		{path: "internal/task", want: true},
		{path: "internal/task-extra/file.go", want: false}, // same-root sibling must not match.
		{path: "internal-task/file.go", want: false},
	}
	for _, tc := range cases {
		if got := pathAllowedByScope(tc.path, allowed); got != tc.want {
			t.Fatalf("pathAllowedByScope(%q, %v) = %v, want %v", tc.path, allowed, got, tc.want)
		}
	}
}

// TestPathForbiddenByScopeNormalizesSeparators mirrors the allowed-path test
// for the forbidden side so duplicate-destination protection on scope edges
// is consistent across separator variants.
func TestPathForbiddenByScopeNormalizesSeparators(t *testing.T) {
	t.Parallel()
	forbidden := []string{".env"}
	if !pathForbiddenByScope(".env", forbidden) {
		t.Fatalf("expected pathForbiddenByScope to match exact .env")
	}
	if !pathForbiddenByScope(".env\\local", forbidden) == false {
		// .env/local should not match because forbidden is just .env file;
		// keep behavior consistent: ".env/local" extends boundary so it
		// must still be reported as inside.
	}
	if !pathForbiddenByScope(".env/local", forbidden) {
		t.Fatalf("expected pathForbiddenByScope to match .env/local")
	}
	if !pathForbiddenByScope(".env\\local", forbidden) {
		t.Fatalf("expected pathForbiddenByScope to match .env\\local via separator normalization")
	}
}

// windowsAbsoluteForms enumerates the host-foreign absolute path shapes that
// must be rejected on every OS. `filepath.IsAbs` does not recognize these on
// non-Windows hosts, so without isLogicalAbsolutePath a YAML author could
// smuggle absolute paths through scope/files/preflight fields when the
// daemon ran on Linux or macOS. The list intentionally covers:
//   - slash-rooted Windows forms (`\foo`, `\\server\share`) that survive
//     backslash→slash normalization as `/...`;
//   - drive-qualified absolutes in both separator styles (`C:\foo`, `C:/foo`,
//     lowercase drive `d:\bar`).
var windowsAbsoluteForms = []string{
	"C:\\workspace\\notes.md",
	"C:/workspace/notes.md",
	"d:\\downloads\\file.txt",
	"\\\\server\\share\\file",
	"\\foo",
}

// TestValidateRelativePathRejectsWindowsAbsoluteFormsOnNonWindowsHost proves
// that scope.allowed_paths, scope.forbidden_paths, files[].source,
// files[].destination, and the preflight.acceptance_skeleton paths all reject
// Windows-style absolute forms when the validator runs on a non-Windows host.
// Before isLogicalAbsolutePath the only Windows form caught here was the UNC
// `\\server\share` shape (because its slash form starts with `/`); drive
// letter forms slipped past entirely on Unix.
func TestValidateRelativePathRejectsWindowsAbsoluteFormsOnNonWindowsHost(t *testing.T) {
	t.Parallel()

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
			name: "preflight.acceptance_skeleton.allowed_paths",
			mutate: func(task *Task, p string) {
				task.Preflight = &Preflight{
					AcceptanceSkeleton: &AcceptanceSkeletonConfig{
						Enabled:      true,
						AllowedPaths: []string{p},
					},
				}
			},
			wantSub: "preflight.acceptance_skeleton.allowed_paths[0] contains absolute path",
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
		for _, p := range windowsAbsoluteForms {
			p := p
			t.Run(m.name+"/"+p, func(t *testing.T) {
				t.Parallel()
				task := validTask(t)
				m.mutate(&task, p)
				result := ValidateStructural(task)
				if result.Valid() {
					t.Fatalf("expected %s field with Windows absolute %q to be rejected on this host, got valid result", m.name, p)
				}
				if !containsAny(result.Errors, m.wantSub) {
					t.Fatalf("expected error containing %q for %s = %q, got %#v", m.wantSub, m.name, p, result.Errors)
				}
			})
		}
	}
}

// TestValidateWorktreePathRejectsWindowsAbsoluteForms covers the worktree
// absolute precheck. `filepath.IsAbs` used to gate this check, which missed
// drive-letter absolutes on non-Windows hosts and let them fall through to
// validateWorktreePath where they produced the misleading "must point to a
// sibling path" error instead of the canonical "must be relative" error.
func TestValidateWorktreePathRejectsWindowsAbsoluteForms(t *testing.T) {
	t.Parallel()
	for _, p := range windowsAbsoluteForms {
		p := p
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			task := validTask(t)
			task.Worktree.Path = p
			result := ValidateStructural(task)
			if result.Valid() {
				t.Fatalf("expected worktree.path %q to be rejected as absolute on this host", p)
			}
			if !containsAny(result.Errors, "worktree.path must be relative") {
				t.Fatalf("expected worktree absolute error for %q, got %#v", p, result.Errors)
			}
		})
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
