package task

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shinpr/galley/internal/strutil"
	"go.yaml.in/yaml/v3"
)

// ArchiveOptions controls how a task is archived.
type ArchiveOptions struct {
	Reason string
}

// ArchiveResult describes the archived task move.
type ArchiveResult struct {
	Task Task   `json:"task"`
	From string `json:"from"`
	To   string `json:"to"`
	// Mode records which archive path actually ran. Operators consume it to
	// understand why an archived strict-decode-incompatible task may be missing the audit
	// append-attempt or a freshly normalized status line.
	//   - "current_schema": strict load + append-attempt path (default).
	//   - "lenient_status_edit": lenient YAML round-trip that only updates the
	//     top-level status field while preserving unknown fields.
	//   - "move_unreadable_unchanged": fallback that copies the YAML bytes
	//     unchanged when even safe status editing is unsafe.
	Mode    string `json:"mode,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// Archive moves a completed or reviewed task into tasks/archived without
// overwriting an existing destination. Archive prioritizes removing the file
// from normal operational scans whenever the move is safe:
//
//  1. When the task YAML loads under the current strict schema, archive keeps
//     the historical behavior: it sets status to "archived", appends an
//     audit attempt, and writes the updated YAML at the archived path.
//  2. When strict loading fails because of unknown fields but the file still
//     parses as a YAML mapping, archive falls back to a yaml.Node round-trip
//     that only updates the top-level `status` field. Unknown fields are
//     retained instead of being dropped; no attempt is appended because the
//     append would require re-marshalling the full Task struct and could
//     silently drop fields the current schema does not know about.
//  3. When even safe status editing is unsafe (the YAML cannot be parsed as a
//     mapping or the top-level status value is not a scalar), archive moves
//     the file unchanged to the archived path and surfaces a warning.
//
// Archive fails only when the destination move itself cannot proceed safely,
// such as a duplicate destination or filesystem error. Strict-decode-
// incompatible task YAML is never migrated, normalized, or rewritten through
// the current Task struct.
func Archive(path string, opts ArchiveOptions) (ArchiveResult, error) {
	loaded, strictErr := Load(path)
	if strictErr == nil {
		return archiveCurrentSchema(path, loaded, opts)
	}
	return archiveStrictDecodeIncompatible(path, opts, strictErr)
}

func archiveCurrentSchema(path string, loaded Task, opts ArchiveOptions) (ArchiveResult, error) {
	if !CanArchive(loaded.Status) {
		return ArchiveResult{}, fmt.Errorf("task %s status %q cannot be archived", loaded.ID, loaded.Status)
	}
	loaded.Status = StatusArchived
	loaded.Attempts = append(loaded.Attempts, Attempt{
		Number:            len(loaded.Attempts) + 1,
		StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CompletedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ClaudeStatus:      "not_run",
		SupervisorVerdict: "archived",
		Summary:           strutil.FirstNonEmpty(opts.Reason, "Task archived."),
	})
	nextPath := siblingTaskPath(path, "archived")
	if nextPath == path {
		if err := Save(path, loaded); err != nil {
			return ArchiveResult{}, err
		}
	} else {
		if err := WriteMovedTask(path, nextPath, loaded); err != nil {
			return ArchiveResult{}, err
		}
	}
	return ArchiveResult{Task: loaded, From: path, To: nextPath, Mode: "current_schema"}, nil
}

func archiveStrictDecodeIncompatible(path string, opts ArchiveOptions, strictErr error) (ArchiveResult, error) {
	_ = opts // reason is not appended; no struct round-trip is safe.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return ArchiveResult{}, fmt.Errorf("read %s: %w", path, readErr)
	}
	nextPath := siblingTaskPath(path, "archived")

	// Try the safe top-level status edit via a yaml.Node round-trip. This
	// preserves every other top-level key (including unknown ones) exactly as
	// authored.
	if updated, ok, editErr := editTopLevelStatus(data, "archived"); ok && editErr == nil {
		if nextPath == path {
			// In-place lenient edit: overwrite the existing file. Lossy struct
			// round-tripping is avoided because updated[] came from a YAML
			// node mutation rather than yaml.Marshal(Task).
			if err := writeFileAtomic(path, updated, 0o600); err != nil {
				return ArchiveResult{}, err
			}
		} else {
			if err := moveYAMLNoOverwrite(path, nextPath, updated); err != nil {
				return ArchiveResult{}, err
			}
		}
		summary, _ := lenientHeader(updated)
		summary.Status = "archived"
		return ArchiveResult{
			Task:    summary,
			From:    path,
			To:      nextPath,
			Mode:    "lenient_status_edit",
			Warning: fmt.Sprintf("strict load failed (%v); archived with safe top-level status edit only", strictErr),
		}, nil
	} else if editErr != nil {
		// editTopLevelStatus reported an editing-unsafe condition (not a YAML
		// parse error). Fall through to the move-unchanged path so archive
		// can still evacuate the file from normal scans.
		_ = editErr
	}

	// Fall back: move the file unchanged so it leaves normal scans without
	// touching its bytes. Destination conflicts and filesystem errors still
	// surface so operators can resolve them.
	if nextPath == path {
		// Cannot improve on the current location; report the strict error so
		// the operator knows the file remained where it was.
		return ArchiveResult{}, fmt.Errorf("archive %s: cannot move strict-decode-incompatible file to a separate archived directory and status editing is unsafe: %w", path, strictErr)
	}
	if err := moveYAMLNoOverwrite(path, nextPath, data); err != nil {
		return ArchiveResult{}, err
	}
	summary, _ := lenientHeader(data)
	return ArchiveResult{
		Task:    summary,
		From:    path,
		To:      nextPath,
		Mode:    "move_unreadable_unchanged",
		Warning: fmt.Sprintf("strict load failed (%v); archived unchanged because safe status editing was not possible", strictErr),
	}, nil
}

// editTopLevelStatus updates (or inserts) the top-level `status` mapping
// entry in raw task YAML bytes and returns the re-serialized bytes. It uses
// yaml.Node so fields the current Task struct does not declare are retained,
// although YAML formatting may be normalized by reserialization.
//
// Returns (updated, true, nil) when the edit succeeded; (nil, false, nil)
// when the document could not be parsed at all (so the caller can fall back
// to moving the file unchanged); and (nil, false, err) when parsing
// succeeded but the document shape made the edit unsafe (for example, the
// top-level node is a sequence or scalar rather than a mapping, or the
// existing status value is not a scalar).
func editTopLevelStatus(data []byte, value string) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, nil
	}
	var mapping *yaml.Node
	switch {
	case doc.Kind == yaml.DocumentNode && len(doc.Content) == 1:
		mapping = doc.Content[0]
	case doc.Kind == yaml.MappingNode:
		mapping = &doc
	default:
		return nil, false, fmt.Errorf("top-level yaml document is not a single mapping")
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("top-level yaml is not a mapping")
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key == nil || key.Kind != yaml.ScalarNode {
			continue
		}
		if key.Value != "status" {
			continue
		}
		val := mapping.Content[i+1]
		if val == nil || val.Kind != yaml.ScalarNode {
			return nil, false, fmt.Errorf("top-level status is not a scalar")
		}
		val.Tag = "!!str"
		val.Value = value
		val.Style = yaml.DoubleQuotedStyle
		buf, err := yaml.Marshal(&doc)
		if err != nil {
			return nil, false, err
		}
		return buf, true, nil
	}
	// status was not present at the top level. Add it so a follow-up tolerant
	// scan can see the archived state without rewriting the rest of the file.
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "status"}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}
	mapping.Content = append(mapping.Content, keyNode, valNode)
	buf, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, err
	}
	return buf, true, nil
}

// lenientHeader returns a best-effort decoded task summary from raw YAML
// bytes. It tolerates unknown fields. Callers use it to populate the
// ArchiveResult Task field with at least the visible identifier and status
// when strict decoding is not possible.
func lenientHeader(data []byte) (Task, error) {
	var t Task
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// moveYAMLNoOverwrite writes data to dst through the queue's no-overwrite
// atomic primitive and removes src on success. It is the archive counterpart
// of WriteMovedTask for files that cannot be safely represented as the current
// Task struct.
func moveYAMLNoOverwrite(src, dst string, data []byte) error {
	if err := writeFileNoOverwriteAtomic(dst, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := os.Remove(src); err != nil {
		rollbackErr := os.Remove(dst)
		if rollbackErr != nil {
			return errors.Join(
				fmt.Errorf("remove moved task %s: %w", src, err),
				fmt.Errorf("rollback archived task %s: %w", dst, rollbackErr),
			)
		}
		return fmt.Errorf("remove moved task %s: %w", src, err)
	}
	return nil
}
