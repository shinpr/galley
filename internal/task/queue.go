package task

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/fileutil"
	"go.yaml.in/yaml/v3"
)

// Bound retries so continuous task movement cannot block queue registration.
const duplicateScanMaxAttempts = 5

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
	return queue(path, opts, taskIDFromFile)
}

func queue(path string, opts QueueOptions, readTaskID func(string) (string, error)) (QueueResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return QueueResult{}, err
	}
	// Resolve omitted status before checking queue eligibility.
	ApplyDefaults(&loaded)
	if !CanQueue(loaded.Status) {
		return QueueResult{}, fmt.Errorf("task %s status %q cannot be queued with task queue; use task requeue for reviewed tasks", loaded.ID, loaded.Status)
	}
	ResolveFileSources(path, &loaded)
	loaded.Status = StatusQueued
	loaded.ReviewProgress = nil
	for i := range loaded.AcceptanceCriteria {
		loaded.AcceptanceCriteria[i].Status = "pending"
	}
	validation := Validate(loaded)
	if !validation.Valid() {
		return QueueResult{}, fmt.Errorf("task validation failed: %v", validation.Errors)
	}
	registrationRoot := filepath.Dir(filepath.Dir(queuedPathFor(path, opts.Root)))
	lockPath := filepath.Join(registrationRoot, ".registration", fmt.Sprintf("%x.lock", sha256.Sum256([]byte(loaded.ID))))
	unlock, err := fileutil.TryLock(lockPath)
	if err != nil {
		return QueueResult{}, fmt.Errorf("task id %q registration in progress; retry: %w", loaded.ID, err)
	}
	defer unlock()
	if err := rejectDuplicateTaskID(path, loaded.ID, opts.Root, readTaskID); err != nil {
		return QueueResult{}, err
	}
	appendLifecycleAttempt(&loaded, "queued", opts.Reason, "Task queued for daemon execution.", time.Now())
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

func rejectDuplicateTaskID(path, id, root string, readTaskID func(string) (string, error)) error {
	root = queueRoot(path, root)
	if root == "" || id == "" {
		return nil
	}
	current, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < duplicateScanMaxAttempts; attempt++ {
		duplicatePath, moved, err := scanForDuplicateTaskID(root, current, id, readTaskID)
		if err != nil {
			return err
		}
		if moved {
			continue
		}
		if duplicatePath != "" {
			return fmt.Errorf("task id %q already exists at %s", id, duplicatePath)
		}
		return nil
	}
	return fmt.Errorf("queue registration failed after %d scans: task state kept changing; source preserved and not queued; retry the command", duplicateScanMaxAttempts)
}

// scanForDuplicateTaskID requests a rescan when an enumerated task has moved.
func scanForDuplicateTaskID(root, current, id string, readTaskID func(string) (string, error)) (string, bool, error) {
	var matches []string
	for _, state := range AllWorkflowStates() {
		stateMatches, err := YAMLFiles(TaskStateDir(root, state))
		if err != nil {
			return "", false, err
		}
		matches = append(matches, stateMatches...)
	}
	for _, match := range matches {
		absMatch, err := filepath.Abs(match)
		if err != nil {
			return "", false, err
		}
		if absMatch == current {
			continue
		}
		existingID, err := readTaskID(match)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", true, nil
			}
			return "", false, fmt.Errorf("inspect existing task %s: %w", match, err)
		}
		if existingID == id {
			return match, false, nil
		}
	}
	return "", false, nil
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
		return TaskStatePath(root, WorkflowStateQueued, filepath.Base(path))
	}
	return siblingTaskPath(path, WorkflowStateQueued)
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
