package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/strutil"
	"go.yaml.in/yaml/v3"
)

type QueueOptions struct {
	Reason     string
	Root       string
	MoveSource bool
}

type QueueResult struct {
	Task Task   `json:"task"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Queue validates a task and moves it to tasks/queued without overwriting an existing queued task.
func Queue(path string, opts QueueOptions) (QueueResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return QueueResult{}, err
	}
	if loaded.Status != "draft" {
		return QueueResult{}, fmt.Errorf("task %s status %q cannot be queued with task queue; use task requeue for reviewed tasks", loaded.ID, loaded.Status)
	}
	ResolveFileSources(path, &loaded)
	ApplyDefaults(&loaded)
	loaded.Status = "queued"
	validation := Validate(loaded)
	if !validation.Valid() {
		return QueueResult{}, fmt.Errorf("task validation failed: %v", validation.Errors)
	}
	if err := rejectDuplicateTaskID(path, loaded.ID, opts.Root); err != nil {
		return QueueResult{}, err
	}
	loaded.Attempts = append(loaded.Attempts, Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      "not_run",
		SupervisorVerdict: "queued",
		Summary:           strutil.FirstNonEmpty(opts.Reason, "Task queued for daemon execution."),
	})
	nextPath := queuedPathFor(path, opts.Root)
	if nextPath == path {
		if err := Save(path, loaded); err != nil {
			return QueueResult{}, err
		}
	} else {
		removeSource := opts.MoveSource || taskPathUnderRoot(path, opts.Root)
		if err := writeQueuedTask(path, nextPath, loaded, removeSource); err != nil {
			return QueueResult{}, err
		}
	}
	return QueueResult{Task: loaded, From: path, To: nextPath}, nil
}

func rejectDuplicateTaskID(path, id, root string) error {
	root = queueRoot(path, root)
	if root == "" || id == "" {
		return nil
	}
	current, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	matches, err := filepath.Glob(filepath.Join(root, "tasks", "*", "*.y*ml"))
	if err != nil {
		return err
	}
	for _, match := range matches {
		absMatch, err := filepath.Abs(match)
		if err != nil {
			return err
		}
		if absMatch == current {
			continue
		}
		existingID, err := taskIDFromFile(match)
		if err != nil {
			return fmt.Errorf("inspect existing task %s: %w", match, err)
		}
		if existingID == id {
			return fmt.Errorf("task id %q already exists at %s", id, match)
		}
	}
	return nil
}

func taskIDFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var header struct {
		ID string `yaml:"id"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return "", nil
	}
	return header.ID, nil
}

func queueRoot(path, root string) string {
	if root != "" {
		return filepath.Clean(root)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	stateDir := filepath.Dir(absPath)
	tasksDir := filepath.Dir(stateDir)
	if filepath.Base(tasksDir) != "tasks" {
		return ""
	}
	if info, err := os.Stat(tasksDir); err == nil && info.IsDir() {
		return filepath.Dir(tasksDir)
	}
	return ""
}

func queuedPathFor(path, root string) string {
	if root != "" {
		return filepath.Join(root, "tasks", "queued", filepath.Base(path))
	}
	return siblingTaskPath(path, "queued")
}

func taskPathUnderRoot(path, root string) bool {
	if root == "" {
		return true
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	tasksRoot := filepath.Join(filepath.Clean(absRoot), "tasks")
	rel, err := filepath.Rel(tasksRoot, filepath.Clean(absPath))
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
