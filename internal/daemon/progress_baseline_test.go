package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/workspace"
)

func sha256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Only diffs beyond baseline-matching skeleton files count as progress, so the
// consecutive-no-diff escalation still fires on skeleton-only attempts (AC-013, AC-014).
func TestHasNonSkeletonProgress(t *testing.T) {
	t.Parallel()
	const skeleton = "internal/foo/foo_test.go"
	placeholder := "// galley skeleton placeholder\n"
	for _, tc := range []struct {
		name         string
		worktreeBody string
		baselineBody string
		snapshot     workspace.Snapshot
		want         bool
	}{
		{
			name:         "skeleton unchanged from baseline",
			worktreeBody: placeholder,
			baselineBody: placeholder,
			snapshot:     workspace.Snapshot{Dirty: true, StatusPorcelain: "?? " + skeleton + "\n"},
			want:         false,
		},
		{
			name:         "skeleton content edited",
			worktreeBody: placeholder + "func TestThing(t *testing.T) {}\n",
			baselineBody: placeholder,
			snapshot:     workspace.Snapshot{Dirty: true, StatusPorcelain: " M " + skeleton + "\n"},
			want:         true,
		},
		{
			name:         "non-skeleton file changed",
			worktreeBody: placeholder,
			baselineBody: placeholder,
			snapshot:     workspace.Snapshot{Dirty: true, StatusPorcelain: " M internal/foo/foo.go\n?? " + skeleton + "\n"},
			want:         true,
		},
		{
			name:     "clean snapshot",
			snapshot: workspace.Snapshot{Dirty: false},
			want:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			var preflight *skeletonpreflight.Result
			if tc.baselineBody != "" {
				if err := os.MkdirAll(filepath.Join(work, filepath.Dir(skeleton)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, skeleton), []byte(tc.worktreeBody), 0o644); err != nil {
					t.Fatal(err)
				}
				preflight = &skeletonpreflight.Result{Baseline: skeletonpreflight.Baseline{
					SkeletonHashes: []skeletonpreflight.SkeletonHash{{Path: skeleton, SHA256: sha256Hex(t, []byte(tc.baselineBody))}},
				}}
			}
			got, err := hasNonSkeletonProgress(tc.snapshot, work, preflight)
			if err != nil {
				t.Fatalf("hasNonSkeletonProgress error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("hasNonSkeletonProgress = %v, want %v", got, tc.want)
			}
		})
	}
}
