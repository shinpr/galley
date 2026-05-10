package daemon

import (
	"strings"
	"testing"
)

// TestWorkOrderPreflightIncludesRuntimeSkeletonPathsAndObligations proves the
// executor work order is augmented with the runtime preflight result: concrete
// skeleton paths, AC bindings, kinds, checkpoint commands, and the completion
// obligation that every implementation_required skeleton must have a passing
// checkpoint before acceptance.
func TestWorkOrderPreflightIncludesRuntimeSkeletonPathsAndObligations(t *testing.T) {
	t.Parallel()
	base := "## Original work order body\n"
	res := &AcceptanceSkeletonResult{
		Status: "completed",
		Outputs: []AcceptanceSkeletonOutput{{
			ACID:                   "AC1",
			Path:                   "internal/foo/foo_test.go",
			Kind:                   "go-test",
			Purpose:                "verify foo behavior",
			ImplementationRequired: true,
			CheckpointCommand:      "go test ./internal/foo/ -run Foo",
		}},
	}
	got := appendPreflightObligations(base, res)
	if !strings.HasPrefix(got, base) {
		t.Fatalf("original work order body not preserved:\n%s", got)
	}
	for _, want := range []string{
		"Acceptance Skeleton Obligations (Runtime)",
		"AC `AC1` -> `internal/foo/foo_test.go`",
		"kind=go-test",
		"implementation_required=true",
		"verify foo behavior",
		"go test ./internal/foo/ -run Foo",
		"every implementation_required skeleton above must have a passing checkpoint",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("work order missing %q:\n%s", want, got)
		}
	}
}

func TestWorkOrderPreflightSurfacesPreflightFailure(t *testing.T) {
	t.Parallel()
	res := &AcceptanceSkeletonResult{
		Status: "failed",
		Error:  &AcceptanceSkeletonError{Phase: "acceptance_skeleton_creator", Message: "creator command exited 1"},
	}
	got := appendPreflightObligations("body\n", res)
	if !strings.Contains(got, "Acceptance skeleton preflight failed") || !strings.Contains(got, "creator command exited 1") {
		t.Fatalf("work order missing preflight failure surface:\n%s", got)
	}
}

func TestWorkOrderPreflightUnchangedWhenNoPreflightResult(t *testing.T) {
	t.Parallel()
	body := "## Untouched work order\n"
	if got := appendPreflightObligations(body, nil); got != body {
		t.Fatalf("work order changed without a preflight result:\n%s", got)
	}
}
