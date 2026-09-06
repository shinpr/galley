package proc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

func TestProductionGitInvocationsUseGitArgs(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	fset := token.NewFileSet()

	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return skipVendoredDir(entry)
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		if !isScannedProductionGoFile(path, rel) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		violations = append(violations, gitArgvViolations(fset, file, rel)...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production git invocations: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Galley-owned git invocations must use proc.GitArgs:\n%s", strings.Join(violations, "\n"))
	}
}

func gitExecArgIndex(call *ast.CallExpr) int {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return -1
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return -1
	}
	switch selector.Sel.Name {
	case "Command":
		if len(call.Args) > 0 && isGitExpr(call.Args[0]) {
			return 0
		}
	case "CommandContext":
		if len(call.Args) > 1 && isGitExpr(call.Args[1]) {
			return 1
		}
	}
	return -1
}

func stringSliceStartsWithGit(lit *ast.CompositeLit) bool {
	if len(lit.Elts) == 0 {
		return false
	}
	array, ok := lit.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	elt, ok := array.Elt.(*ast.Ident)
	if !ok || elt.Name != "string" {
		return false
	}
	return isGitExpr(lit.Elts[0])
}

func isGitExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(e.Value)
		return err == nil && value == "git"
	case *ast.CallExpr:
		selector, ok := e.Fun.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == "git"
	default:
		return false
	}
}

func skipVendoredDir(entry os.DirEntry) error {
	switch entry.Name() {
	case ".git", ".claude", "node_modules", "vendor":
		return filepath.SkipDir
	}
	return nil
}

// isScannedProductionGoFile excludes tests and proc/git.go itself, which owns
// the sanctioned git argv construction.
func isScannedProductionGoFile(path, rel string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	return rel != "internal/proc/git.go"
}

// gitArgvViolations reports every direct git invocation that bypasses GitArgs.
func gitArgvViolations(fset *token.FileSet, file *ast.File, rel string) []string {
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if gitExecArgIndex(n) >= 0 {
				violations = append(violations, rel+":"+fset.Position(n.Pos()).String())
			}
		case *ast.CompositeLit:
			if stringSliceStartsWithGit(n) {
				violations = append(violations, rel+":"+fset.Position(n.Pos()).String())
			}
		}
		return true
	})
	return violations
}
