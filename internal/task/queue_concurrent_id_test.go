package task

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestQueueConcurrentSameIDPublishesOnce(t *testing.T) {
	root := t.TempDir()
	loaded, err := Load(writeTaskYAML(t, "loop_budget: 3"))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = StatusDraft
	var paths []string
	for i := 0; i < 12; i++ {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("task-%d.yaml", i))
		if err := Save(path, loaded); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	start := make(chan struct{})
	results := make(chan error, len(paths))
	var wg sync.WaitGroup
	for _, path := range paths {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := Queue(path, QueueOptions{Root: root}); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("published %d tasks with same ID", success)
	}
	loaded.ID = "another-id"
	path := filepath.Join(t.TempDir(), "another.yaml")
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := Queue(path, QueueOptions{Root: root}); err != nil {
		t.Fatalf("other ID blocked: %v", err)
	}
}
