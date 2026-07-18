package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/supervisor"
	"github.com/shinpr/galley/internal/task"
)

func TestTerminalVerdictPreservesCurrentAddressedRevisionState(t *testing.T) {
	for _, status := range []string{"hard_stop", "needs_supervisor_review"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			runningPath := filepath.Join(root, "tasks", "running", "task.yaml")
			if err := os.MkdirAll(filepath.Dir(runningPath), 0o755); err != nil {
				t.Fatal(err)
			}
			loaded := task.Task{
				ID:     "terminal-revision",
				Status: "running",
				RevisionRequests: []task.RevisionRequest{{
					ID: "supervisor-attempt-1-finding-1", Source: "supervisor", Text: "fix it", Status: "addressed", Evidence: "fixed",
				}},
			}
			if err := task.Save(runningPath, loaded); err != nil {
				t.Fatal(err)
			}
			_, done, err := applySupervisorVerdict(context.Background(), context.Background(), verdictApplication{
				Opts: Options{Root: root}, RunningPath: runningPath, Loaded: &loaded,
				Verdict: supervisor.Verdict{Status: status},
			})
			if err != nil || !done {
				t.Fatalf("terminal verdict: done=%v err=%v", done, err)
			}
			terminalPath := filepath.Join(root, "tasks", "failed", "task.yaml")
			persisted, err := task.Load(terminalPath)
			if err != nil {
				t.Fatal(err)
			}
			if request := persisted.RevisionRequests[0]; request.Status != "addressed" || request.Evidence != "fixed" {
				t.Fatalf("terminal task regressed addressed revision: %#v", request)
			}
		})
	}
}

func TestNextSupervisorRevisionIncludesFindingsOnly(t *testing.T) {
	verdict := supervisor.Verdict{
		Status:         "needs_revision",
		AcceptanceGaps: []string{"AC2"},
		Findings: []supervisor.Finding{
			{Severity: "high", Category: "acceptance", File: "internal/daemon/loop.go", Summary: "The retry loses the original task contract.", BlocksAcceptance: true},
			{Severity: "low", Category: "comment-quality", File: "internal/daemon/loop_test.go", Summary: "A comment restates the assertion.", BlocksAcceptance: true},
		},
		NextWorkOrder: "Preserve the base work order and address every listed revision.",
	}

	revision := nextSupervisorRevision(supervisorRevision{}, 2, verdict)
	if got, want := len(revision.Requests), 2; got != want {
		t.Fatalf("revision request count got %d, want %d: %#v", got, want, revision.Requests)
	}
	if revision.Guidance != verdict.NextWorkOrder || revision.SourceAttempt != 2 {
		t.Fatalf("revision metadata got %#v", revision)
	}

	joined := revisionText(revision.Requests)
	for _, request := range revision.Requests {
		if !strings.HasPrefix(request.ID, "supervisor-attempt-2-finding-") {
			t.Fatalf("non-finding revision request got %#v", request)
		}
	}
	for _, want := range []string{
		"category=acceptance file=internal/daemon/loop.go: The retry loses the original task contract.",
		"category=comment-quality file=internal/daemon/loop_test.go: A comment restates the assertion.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("revision requests missing %q: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"acceptance gap", "severity=", "blocks_acceptance"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("revision requests contain unexpected metadata %q: %s", unwanted, joined)
		}
	}
}

func TestNextSupervisorRevisionPreservesAddressedHistoryAndPendingRequests(t *testing.T) {
	loaded := task.Task{RevisionRequests: []task.RevisionRequest{
		{ID: "supervisor-attempt-1-finding-1", Source: "supervisor", Text: "first", Status: "pending"},
		{ID: "supervisor-attempt-1-finding-2", Source: "supervisor", Text: "second", Status: "pending"},
	}}
	verdict := supervisor.Verdict{
		Status: "needs_revision",
		AcceptanceEvidence: []supervisor.AcceptanceEvidence{
			{ACID: "revision:supervisor-attempt-1-finding-1", Evidence: []string{"fixed in loop.go"}},
		},
		Findings: []supervisor.Finding{
			{Category: "regression", Summary: "A new boundary case is missing."},
		},
		NextWorkOrder: "Cover the remaining boundary.",
	}
	supervisor.ApplyReviewProgress(&loaded, profile.Bundle{}, verdict)
	previous := supervisorRevisionFromTask(loaded)

	revision := nextSupervisorRevision(previous, 2, verdict)
	if got, want := len(revision.Requests), 3; got != want {
		t.Fatalf("revision request count got %d, want %d: %#v", got, want, revision.Requests)
	}
	if request := revision.Requests[0]; request.ID != "supervisor-attempt-1-finding-1" || request.Status != "addressed" || request.Evidence != "fixed in loop.go" {
		t.Fatalf("resolved request audit got %#v", request)
	}
	if request := revision.Requests[1]; request.ID != "supervisor-attempt-1-finding-2" || request.Status != "pending" {
		t.Fatalf("unresolved request got %#v", request)
	}
	if request := revision.Requests[2]; request.ID != "supervisor-attempt-2-finding-1" || request.Status != "pending" {
		t.Fatalf("new request got %#v", request)
	}

	persisted := revision.applyToTask(loaded)
	if got, want := len(persisted.RevisionRequests), 3; got != want {
		t.Fatalf("persisted revision request count got %d, want %d: %#v", got, want, persisted.RevisionRequests)
	}
	workOrder := task.RenderWorkOrder(persisted)
	if strings.Contains(workOrder, "`supervisor-attempt-1-finding-1`") {
		t.Fatalf("work order contains addressed request:\n%s", workOrder)
	}
	for _, pendingID := range []string{"supervisor-attempt-1-finding-2", "supervisor-attempt-2-finding-1"} {
		if !strings.Contains(workOrder, "`"+pendingID+"`") {
			t.Fatalf("work order missing pending request %q:\n%s", pendingID, workOrder)
		}
	}
}

func revisionText(requests []task.RevisionRequest) string {
	var values []string
	for _, request := range requests {
		values = append(values, request.Text)
	}
	return strings.Join(values, "\n")
}

func assertSupervisorRevisionState(t *testing.T, value task.Task, requestID, finding, guidance string) {
	t.Helper()
	var requestFound bool
	for _, request := range value.RevisionRequests {
		if request.ID == requestID && strings.Contains(request.Text, finding) && request.Status == "pending" {
			requestFound = true
			break
		}
	}
	if !requestFound {
		t.Fatalf("supervisor revision request missing id=%q finding=%q: %#v", requestID, finding, value.RevisionRequests)
	}
	var guidanceFound bool
	for _, risk := range value.Risks {
		if strings.HasPrefix(risk.ID, "requeue-supervisor-attempt-") && risk.Detail == guidance {
			guidanceFound = true
			break
		}
	}
	if !guidanceFound {
		t.Fatalf("supervisor guidance missing %q: %#v", guidance, value.Risks)
	}
}
