package daemon

import "testing"

func TestPreflightPreservesExplicitZeroPerRepoLimit(t *testing.T) {
	t.Parallel()
	preflighted, err := Preflight(Options{
		Root:                 t.TempDir(),
		Supervisor:           "codex",
		MaxConcurrentPerRepo: 0,
		Explicit:             ExplicitOptions{MaxConcurrentPerRepo: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preflighted.MaxConcurrentPerRepo != 0 {
		t.Fatalf("Preflight must preserve max_concurrent_per_repo=0; got %d", preflighted.MaxConcurrentPerRepo)
	}
}
