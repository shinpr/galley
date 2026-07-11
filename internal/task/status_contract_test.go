package task

import (
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

func TestWorkflowStateForStatus(t *testing.T) {
	t.Parallel()
	tests := map[string]WorkflowState{
		StatusDraft: WorkflowStateDraft, StatusQueued: WorkflowStateQueued,
		StatusRunning: WorkflowStateRunning, StatusAccepted: WorkflowStateDone,
		StatusPROpened: WorkflowStateDone, StatusMerged: WorkflowStateDone,
		StatusClosed: WorkflowStateDone, StatusFailed: WorkflowStateFailed,
		StatusNeedsSupervisorReview: WorkflowStateFailed, StatusArchived: WorkflowStateArchived,
	}
	for status, want := range tests {
		got, err := WorkflowStateForStatus(status)
		if err != nil || got != want {
			t.Fatalf("WorkflowStateForStatus(%q) = %q, %v; want %q", status, got, err, want)
		}
	}
	if _, err := WorkflowStateForStatus("unknown"); err == nil {
		t.Fatal("unknown status must fail")
	}
}

func TestCanonicalListsAreStableDefensiveCopies(t *testing.T) {
	statuses := AllStatuses()
	wantStatuses := []string{"draft", "queued", "running", "needs_supervisor_review", "accepted", "pr_opened", "failed", "closed", "merged", "archived"}
	if !reflect.DeepEqual(statuses, wantStatuses) {
		t.Fatalf("statuses = %v; want %v", statuses, wantStatuses)
	}
	statuses[0] = "changed"
	if AllStatuses()[0] != StatusDraft {
		t.Fatal("AllStatuses returned shared storage")
	}

	states := AllWorkflowStates()
	wantStates := []WorkflowState{WorkflowStateDraft, WorkflowStateQueued, WorkflowStateRunning, WorkflowStateDone, WorkflowStateFailed, WorkflowStateArchived}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("states = %v; want %v", states, wantStates)
	}
	states[0] = WorkflowStateFailed
	if AllWorkflowStates()[0] != WorkflowStateDraft {
		t.Fatal("AllWorkflowStates returned shared storage")
	}
}

func TestWorkflowStateForTransition(t *testing.T) {
	got, err := WorkflowStateForTransition(StatusQueued, StatusRunning)
	if err != nil || got != WorkflowStateRunning {
		t.Fatalf("queued -> running = %q, %v", got, err)
	}
	if _, err := WorkflowStateForTransition(StatusRunning, StatusQueued); err == nil {
		t.Fatal("unregistered directory-ahead transition must fail")
	}
}

func TestProductionTaskPathsUseWorkflowStateContract(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	states := map[string]bool{
		"draft": true, "queued": true, "running": true,
		"done": true, "failed": true, "archived": true,
	}
	err := filepath.WalkDir(filepath.Join(repoRoot, "internal"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || path == filepath.Join(repoRoot, "internal", "task", "status.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isFilepathJoin(call.Fun) {
				return true
			}
			for i := 0; i+1 < len(call.Args); i++ {
				if stringLiteral(call.Args[i]) == "tasks" && states[stringLiteral(call.Args[i+1])] {
					t.Errorf("%s constructs a task workflow path with literal %q; use TaskStateDir or TaskStatePath", path, stringLiteral(call.Args[i+1]))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isFilepathJoin(fun ast.Expr) bool {
	selector, ok := fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Join" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "filepath"
}

func stringLiteral(expr ast.Expr) string {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, _ := strconv.Unquote(literal.Value)
	return value
}
