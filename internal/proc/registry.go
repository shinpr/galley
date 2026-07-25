package proc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
)

// ChildRecord identifies a single Galley-started child subprocess so that
// galley daemon stop --force can SIGKILL outstanding process groups even if
// the daemon itself has been killed and can no longer clean them up.
type ChildRecord struct {
	PID       int    `json:"pid"`
	PGID      int    `json:"pgid"`
	Argv0     string `json:"argv0,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
	StartedAt string `json:"started_at"`
}

// ChildRegistry tracks active child subprocess process groups in a file-backed
// JSON document so the same daemon root can be inspected from the separate
// `galley daemon stop --force` CLI process.
//
// Tracking is intentionally limited to subprocess process groups that
// RunCommand itself started via cmd.Start(); generic OS PIDs are never added.
// Records are removed once RunCommand observes the child exit, and List prunes
// records whose process group is no longer alive so stop --force never targets
// a reused process group.
type ChildRegistry struct {
	path string
	mu   sync.Mutex
}

// NewChildRegistry returns a registry that persists records under path. An
// empty path disables registration; that keeps the registry off in unit tests
// and CLI subcommands that don't run subprocesses.
func NewChildRegistry(path string) *ChildRegistry {
	return &ChildRegistry{path: path}
}

// Path returns the on-disk registry path. An empty value disables the
// registry.
func (r *ChildRegistry) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

var (
	defaultChildRegistryMu sync.Mutex
	defaultChildRegistry   *ChildRegistry
)

// SetDefaultChildRegistry configures the package-level registry used by
// RunCommand. Pass nil (or an empty path) to disable. The daemon installs the
// registry early in Run so executor and supervisor subprocesses are tracked
// for the lifetime of the daemon process.
func SetDefaultChildRegistry(reg *ChildRegistry) {
	defaultChildRegistryMu.Lock()
	defer defaultChildRegistryMu.Unlock()
	defaultChildRegistry = reg
}

// DefaultChildRegistry returns the registry currently installed via
// SetDefaultChildRegistry, or nil when none has been installed.
func DefaultChildRegistry() *ChildRegistry {
	defaultChildRegistryMu.Lock()
	defer defaultChildRegistryMu.Unlock()
	return defaultChildRegistry
}

// ChildRegistryPath returns the conventional registry path under a daemon
// root. Both the runner package (writer) and daemonctl (reader) use this
// helper so the file location stays in sync without a new configuration knob.
func ChildRegistryPath(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, "runtime", "children.json")
}

type childRegistryFile struct {
	Children []ChildRecord `json:"children"`
}

func (r *ChildRegistry) load() ([]ChildRecord, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var doc childRegistryFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", r.path, err)
	}
	return doc.Children, nil
}

func (r *ChildRegistry) save(records []ChildRecord) error {
	if records == nil {
		records = []ChildRecord{}
	}
	return jsonio.Write(r.path, childRegistryFile{Children: records})
}

// Register adds a child record to the registry, replacing any prior entry
// with the same PID. The PID/PGID identifies the process group RunCommand
// just started.
func (r *ChildRegistry) Register(rec ChildRecord) error {
	if r == nil || r.path == "" || rec.PID <= 0 || rec.PGID <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	records, err := r.load()
	if err != nil {
		return err
	}
	// Drop any stale entry that shares the same PID. The PID identity check
	// is bounded to records this Galley daemon registered itself, so a reused
	// PID from before the current daemon start cannot survive here.
	filtered := records[:0]
	for _, existing := range records {
		if existing.PID == rec.PID {
			continue
		}
		filtered = append(filtered, existing)
	}
	if rec.StartedAt == "" {
		rec.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	filtered = append(filtered, rec)
	return r.save(filtered)
}

// Unregister removes the record whose PID matches pid. Missing entries are
// silently ignored so a normal subprocess exit does not return an error.
func (r *ChildRegistry) Unregister(pid int) error {
	if r == nil || r.path == "" || pid <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	records, err := r.load()
	if err != nil {
		return err
	}
	filtered := records[:0]
	removed := false
	for _, existing := range records {
		if existing.PID == pid {
			removed = true
			continue
		}
		filtered = append(filtered, existing)
	}
	if !removed {
		return nil
	}
	return r.save(filtered)
}

// ChildAliveFunc reports whether a registered child is still alive. Callers
// receive the whole record so they can probe the child's process group rather
// than only the (possibly already-reaped) leader PID: a force-killed or reaped
// group leader can still have live descendants in the same pgid, and those must
// stay tracked so stop --force can target them. Allowing callers to inject the
// probe keeps cross-platform behavior (and daemonctl-side cleanup tests)
// decoupled from runner-package internals.
type ChildAliveFunc func(rec ChildRecord) (bool, error)

// List returns the active child records. Records that alive reports dead are
// removed from the registry as a side effect so the file does not accumulate
// stale entries between daemon restarts. alive should probe process-group
// liveness (not just the leader PID) so a record is not dropped while its
// process group still has surviving members. When alive is nil, every record
// is returned without pruning.
func (r *ChildRegistry) List(alive ChildAliveFunc) ([]ChildRecord, error) {
	if r == nil || r.path == "" {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	records, err := r.load()
	if err != nil {
		return nil, err
	}
	if alive == nil {
		return append([]ChildRecord(nil), records...), nil
	}
	kept := make([]ChildRecord, 0, len(records))
	dropped := false
	for _, rec := range records {
		ok, err := alive(rec)
		if err != nil {
			// Treat probe errors as "still tracked" so an unknown OS error
			// does not silently shrink the registry.
			kept = append(kept, rec)
			continue
		}
		if ok {
			kept = append(kept, rec)
			continue
		}
		dropped = true
	}
	if dropped {
		if err := r.save(kept); err != nil {
			return kept, err
		}
	}
	return append([]ChildRecord(nil), kept...), nil
}

// Clear empties the registry. Used by daemonctl after force stop confirms all
// known child process groups are gone.
func (r *ChildRegistry) Clear() error {
	if r == nil || r.path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
