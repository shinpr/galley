package inputfiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

const maxInputFileBytes = 10 * 1024 * 1024

// Prepared records an input file copied into an executor worktree.
type Prepared struct {
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Path          string `json:"path"`
	Description   string `json:"description,omitempty"`
	Commit        bool   `json:"commit"`
	ContentSHA256 string `json:"content_sha256"`
	reused        bool
}

// Prepare copies task input files into a worktree without overwriting existing files.
func Prepare(workDir string, files []task.InputFile) ([]Prepared, error) {
	return PrepareReusing(workDir, files, nil)
}

// PrepareReusing preserves existing inputs only when prior run evidence proves placement.
func PrepareReusing(workDir string, files []task.InputFile, prior []Prepared) ([]Prepared, error) {
	prepared := make([]Prepared, 0, len(files))
	for i, file := range files {
		if file.Source == "" || file.Destination == "" {
			_ = CleanupPrepared(prepared)
			return nil, fmt.Errorf("files[%d] source and destination are required", i)
		}
		dst, err := destination(workDir, file.Destination)
		if err != nil {
			_ = CleanupPrepared(prepared)
			return nil, fmt.Errorf("files[%d].destination: %w", i, err)
		}
		if err := ensureExistingParentUnderRoot(workDir, filepath.Dir(dst)); err != nil {
			_ = CleanupPrepared(prepared)
			return nil, fmt.Errorf("files[%d].destination: %w", i, err)
		}
		// Check the existing ancestor before MkdirAll, then the final parent after
		// creation, so a symlinked parent cannot redirect copies outside workDir.
		//nolint:gosec // G301: destination is inside the worktree checkout
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = CleanupPrepared(prepared)
			return nil, fmt.Errorf("create input file dir %s: %w", filepath.Dir(dst), err)
		}
		if err := ensureUnderRoot(workDir, filepath.Dir(dst)); err != nil {
			_ = CleanupPrepared(prepared)
			return nil, fmt.Errorf("files[%d].destination: %w", i, err)
		}
		contentSHA256, reused, err := reusePriorInput(file, dst, prior)
		if err != nil {
			_ = CleanupPrepared(prepared)
			return nil, err
		}
		if !reused {
			contentSHA256, err = copyFileNoOverwrite(file.Source, dst)
			if err != nil {
				_ = CleanupPrepared(prepared)
				return nil, fmt.Errorf("copy input file %s to %s: %w", file.Source, dst, err)
			}
		}
		prepared = append(prepared, Prepared{
			Source:        file.Source,
			Destination:   filepath.Clean(file.Destination),
			Path:          dst,
			Description:   file.Description,
			Commit:        file.Commit,
			ContentSHA256: contentSHA256,
			reused:        reused,
		})
	}
	return prepared, nil
}

// ContractDigest identifies the exact task input bytes placed in the worktree.
func ContractDigest(files []Prepared) string {
	var body bytes.Buffer
	for _, file := range files {
		writeDigestValue(&body, filepath.ToSlash(file.Destination))
		writeDigestValue(&body, file.ContentSHA256)
	}
	sum := sha256.Sum256(body.Bytes())
	return hex.EncodeToString(sum[:])
}

// CleanupPrepared removes files copied during Prepare after a later setup error.
func CleanupPrepared(files []Prepared) error {
	var errs []error
	for _, file := range files {
		if file.reused {
			continue
		}
		if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove prepared input file %s: %w", file.Path, err))
		}
	}
	return errors.Join(errs...)
}

// CleanupNonCommitted removes non-committed input files before committing worktree changes.
func CleanupNonCommitted(workDir string, files []task.InputFile) error {
	for i, file := range files {
		if file.Commit {
			continue
		}
		dst, err := destination(workDir, file.Destination)
		if err != nil {
			return fmt.Errorf("files[%d].destination: %w", i, err)
		}
		if err := ensureExistingParentUnderRoot(workDir, filepath.Dir(dst)); err != nil {
			return fmt.Errorf("files[%d].destination: %w", i, err)
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove non-committed input file %s: %w", dst, err)
		}
		if err := pruneEmptyParents(workDir, filepath.Dir(dst)); err != nil {
			return fmt.Errorf("prune input file parent dirs: %w", err)
		}
	}
	return nil
}

func destination(workDir, dst string) (string, error) {
	if dst == "" {
		return "", fmt.Errorf("empty path")
	}
	if !filepath.IsLocal(dst) {
		return "", fmt.Errorf("must be a local relative path: %s", dst)
	}
	return filepath.Join(workDir, filepath.Clean(dst)), nil
}

func copyFileNoOverwrite(src, dst string) (string, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("source must not be a symlink: %s", src)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("source must be a regular file: %s", src)
	}
	if info.Size() > maxInputFileBytes {
		return "", fmt.Errorf("source exceeds %d bytes: %s", maxInputFileBytes, src)
	}
	source, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = source.Close() }()
	openedInfo, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("stat opened source %s: %w", src, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("source must be a regular file after open: %s", src)
	}
	if openedInfo.Size() > maxInputFileBytes {
		return "", fmt.Errorf("source exceeds %d bytes after open: %s", maxInputFileBytes, src)
	}
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("destination already exists: %s", dst)
		}
		return "", fmt.Errorf("create %s: %w", dst, err)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(source, maxInputFileBytes+1))
	if err != nil {
		_ = file.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("write %s: %w", dst, err)
	}
	if written > maxInputFileBytes {
		_ = file.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("source exceeds %d bytes while copying: %s", maxInputFileBytes, src)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("write %s: %w", dst, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeDigestValue(body *bytes.Buffer, value string) {
	body.WriteString(strconv.Itoa(len(value)))
	body.WriteByte(':')
	body.WriteString(value)
}

func ensureUnderRoot(root, path string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve workdir %s: %w", root, err)
	}
	pathReal, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve destination parent %s: %w", path, err)
	}
	rel, err := filepath.Rel(rootReal, pathReal)
	if err != nil {
		return fmt.Errorf("compare destination parent %s to workdir %s: %w", path, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("destination parent resolves outside workdir: %s", path)
	}
	return nil
}

func ensureExistingParentUnderRoot(root, dir string) error {
	for {
		if _, err := os.Stat(dir); err == nil {
			return ensureUnderRoot(root, dir)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect destination parent %s: %w", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("destination parent has no existing ancestor: %s", dir)
		}
		dir = parent
	}
}

func pruneEmptyParents(root, dir string) error {
	root = filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == root || dir == "." || dir == string(filepath.Separator) {
			return nil
		}
		rel, err := filepath.Rel(root, dir)
		//nolint:nilerr // leaving the root stops pruning; that is the safe end state
		if err != nil || rel == ".." || filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, "../") {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read dir %s: %w", dir, err)
		}
		if len(entries) > 0 {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("remove empty dir %s: %w", dir, err)
		}
		dir = filepath.Dir(dir)
	}
}

// reusePriorInput returns the digest of an input a prior run already placed when
// source, destination, and commit match; reused=false means the caller copies it.
func reusePriorInput(file task.InputFile, dst string, prior []Prepared) (string, bool, error) {
	for _, known := range prior {
		if known.Source != file.Source || known.Destination != filepath.Clean(file.Destination) || known.Commit != file.Commit || filepath.Clean(known.Path) != filepath.Clean(dst) {
			continue
		}
		info, statErr := os.Lstat(dst)
		if os.IsNotExist(statErr) {
			return "", false, nil
		}
		if statErr != nil {
			return "", false, fmt.Errorf("reused input must be a regular file: %s: %w", dst, statErr)
		}
		if !info.Mode().IsRegular() {
			return "", false, fmt.Errorf("reused input must be a regular file: %s", dst)
		}
		data, readErr := os.ReadFile(dst)
		if readErr != nil {
			return "", false, fmt.Errorf("read reused input %s: %w", dst, readErr)
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]), true, nil
	}
	return "", false, nil
}
