package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinpr/galley/internal/workspace"
)

func sha256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestProgressBaselineReportsCleanWhenSkeletonUnchanged verifies that
// across repeated attempts, when the dirty diff contains only baseline-
// matching skeleton files, hasNonSkeletonProgress reports no progress so the
// existing consecutive-no-diff escalation can fire (AC-014).
func TestProgressBaselineReportsCleanWhenSkeletonUnchanged(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	body := []byte("// galley skeleton placeholder\n")
	rel := "internal/foo/foo_test.go"
	if err := os.MkdirAll(filepath.Join(work, "internal/foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, rel), body, 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := workspace.Snapshot{
		Dirty:           true,
		StatusPorcelain: "?? " + rel + "\n",
	}
	preflight := &AcceptanceSkeletonResult{
		Baseline: AcceptanceSkeletonBaseline{
			SkeletonHashes: []SkeletonHash{{Path: rel, SHA256: sha256Hex(t, body)}},
		},
	}
	got, err := hasNonSkeletonProgress(snapshot, work, preflight)
	if err != nil {
		t.Fatalf("hasNonSkeletonProgress error: %v", err)
	}
	if got {
		t.Fatalf("hasNonSkeletonProgress = true; want false for skeleton-only baseline-matching diff")
	}
}

// TestProgressBaselineDetectsChangedSkeletonContent verifies that when
// the executor edits a skeleton file (so its content no longer matches the
// post-preflight baseline hash), the attempt is reported as progress (AC-013).
func TestProgressBaselineDetectsChangedSkeletonContent(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	rel := "internal/foo/foo_test.go"
	if err := os.MkdirAll(filepath.Join(work, "internal/foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("// galley skeleton placeholder\n")
	edited := []byte("// galley skeleton placeholder\nfunc TestThing(t *testing.T) {}\n")
	if err := os.WriteFile(filepath.Join(work, rel), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := workspace.Snapshot{
		Dirty:           true,
		StatusPorcelain: " M " + rel + "\n",
	}
	preflight := &AcceptanceSkeletonResult{
		Baseline: AcceptanceSkeletonBaseline{
			SkeletonHashes: []SkeletonHash{{Path: rel, SHA256: sha256Hex(t, original)}},
		},
	}
	got, err := hasNonSkeletonProgress(snapshot, work, preflight)
	if err != nil {
		t.Fatalf("hasNonSkeletonProgress error: %v", err)
	}
	if !got {
		t.Fatalf("hasNonSkeletonProgress = false; want true for changed skeleton content")
	}
}

// TestProgressBaselineDetectsNonSkeletonFile verifies that any change to
// a non-skeleton file counts as progress regardless of the baseline.
func TestProgressBaselineDetectsNonSkeletonFile(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	skel := "internal/foo/foo_test.go"
	if err := os.MkdirAll(filepath.Join(work, "internal/foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("// skeleton\n")
	if err := os.WriteFile(filepath.Join(work, skel), body, 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := workspace.Snapshot{
		Dirty:           true,
		StatusPorcelain: " M internal/foo/foo.go\n?? " + skel + "\n",
	}
	preflight := &AcceptanceSkeletonResult{
		Baseline: AcceptanceSkeletonBaseline{
			SkeletonHashes: []SkeletonHash{{Path: skel, SHA256: sha256Hex(t, body)}},
		},
	}
	got, err := hasNonSkeletonProgress(snapshot, work, preflight)
	if err != nil {
		t.Fatalf("hasNonSkeletonProgress error: %v", err)
	}
	if !got {
		t.Fatalf("hasNonSkeletonProgress = false; want true for non-skeleton diff")
	}
}

// TestProgressBaselineNoDiffReportsClean verifies that a clean snapshot
// remains clean — the helper must not fabricate progress when nothing changed.
func TestProgressBaselineNoDiffReportsClean(t *testing.T) {
	t.Parallel()
	got, err := hasNonSkeletonProgress(workspace.Snapshot{Dirty: false}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("hasNonSkeletonProgress error: %v", err)
	}
	if got {
		t.Fatalf("hasNonSkeletonProgress = true on clean snapshot")
	}
}
