package daemon

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/task"
)

// finalizeRevisionSource marks revision requests Galley itself raised from a
// failed finalization, so they stay distinct from supervisor findings.
const finalizeRevisionSource = "finalize"

// finalizeOutputBudget bounds the captured command output Galley copies into
// the revision request text, which is rendered into the next work order.
const finalizeOutputBudget = 2000

// finalizeFailure carries a finalization error from the accept path back to
// the verdict path, which routes it into the existing revision loop.
type finalizeFailure struct{ Err error }

func (e *finalizeFailure) Error() string {
	if e == nil || e.Err == nil {
		return "finalization failed"
	}
	return e.Err.Error()
}

func (e *finalizeFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func asFinalizeFailure(err error) (*finalizeFailure, bool) {
	var failure *finalizeFailure
	if errors.As(err, &failure) {
		return failure, true
	}
	return nil, false
}

// finalizeRevisionRequest renders the pending revision request Galley persists
// for one failed finalization attempt.
func finalizeRevisionRequest(id, runDir string, err error) task.RevisionRequest {
	var b strings.Builder
	b.WriteString("Galley could not finalize this accepted task: ")
	b.WriteString(boundedFinalizeText(err.Error()))
	if output := finalizeCommandOutput(err); output != "" {
		b.WriteString("\nCommand output: ")
		b.WriteString(output)
	}
	fmt.Fprintf(&b, "\nRun artifacts (command argv, stdout, stderr, exit code): %s", runDir)
	b.WriteString("\nRepair the cause of this failure in the workspace so Galley's own commit, push, and PR creation succeeds on the next attempt. Do not bypass or disable Git hooks, and do not run the commit, push, or PR creation yourself; Galley reruns finalization after this attempt is accepted.")
	return task.RevisionRequest{
		ID:     id,
		Source: finalizeRevisionSource,
		Text:   b.String(),
		Status: "pending",
	}
}

// finalizeCommandOutput extracts the bounded stderr/stdout tail captured for
// the failed git or gh command, which the error string alone does not carry.
func finalizeCommandOutput(err error) string {
	var commandErr *proc.CommandError
	if !errors.As(err, &commandErr) {
		return ""
	}
	output := strings.TrimSpace(commandErr.Result.Stderr)
	if output == "" {
		output = strings.TrimSpace(commandErr.Result.Stdout)
	}
	return boundedFinalizeText(output)
}

// boundedFinalizeText keeps the tail of text, which holds the actionable end
// of a hook or transport failure, within finalizeOutputBudget bytes.
func boundedFinalizeText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= finalizeOutputBudget {
		return text
	}
	return "[truncated]" + text[len(text)-finalizeOutputBudget:]
}

// recordFinalizeRevision persists the pending finalization revision request so
// the next loop attempt receives it as an ordinary revision request.
func recordFinalizeRevision(req verdictApplication, failure *finalizeFailure) error {
	id := finalizeRevisionID(req.Loaded.RevisionRequests, req.Attempt)
	upsertFinalizeRevision(req.Loaded, finalizeRevisionRequest(id, req.RunDir, failure.Err))
	return task.Save(req.RunningPath, *req.Loaded)
}

// finalizeRevisionID derives this attempt's request identity, skipping any ID a
// request Galley does not own already holds.
func finalizeRevisionID(requests []task.RevisionRequest, attempt int) string {
	base := fmt.Sprintf("finalize-attempt-%d", attempt)
	id := base
	for suffix := 2; foreignRevisionHoldsID(requests, id); suffix++ {
		id = fmt.Sprintf("%s-%d", base, suffix)
	}
	return id
}

// foreignRevisionHoldsID reports whether a request from another source holds
// id, so its text, provenance, status, and evidence must survive untouched.
func foreignRevisionHoldsID(requests []task.RevisionRequest, id string) bool {
	for _, request := range requests {
		if request.ID == id && request.Source != finalizeRevisionSource {
			return true
		}
	}
	return false
}

// upsertFinalizeRevision reopens Galley's own request for this attempt, so a
// repeated failure is pending again without replacing another source's request.
func upsertFinalizeRevision(loaded *task.Task, request task.RevisionRequest) {
	for i := range loaded.RevisionRequests {
		existing := &loaded.RevisionRequests[i]
		if existing.ID == request.ID && existing.Source == finalizeRevisionSource {
			*existing = request
			return
		}
	}
	loaded.RevisionRequests = append(loaded.RevisionRequests, request)
}

// markFinalizeRevisionsAddressed closes pending finalization requests once the
// same finalize operation succeeded, which is the evidence they asked for.
func markFinalizeRevisionsAddressed(loaded *task.Task) {
	for i := range loaded.RevisionRequests {
		request := &loaded.RevisionRequests[i]
		if request.Source != finalizeRevisionSource || request.Status == "addressed" {
			continue
		}
		request.Status = "addressed"
		request.Evidence = "Galley reran the same finalize operation and it succeeded."
	}
}

// hasPendingFinalizeRevision reports whether a finalization failure is still
// waiting to be repaired for this task.
func hasPendingFinalizeRevision(loaded task.Task) bool {
	for _, request := range loaded.RevisionRequests {
		if request.Source == finalizeRevisionSource && request.Status != "addressed" {
			return true
		}
	}
	return false
}
