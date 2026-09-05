package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shinpr/galley/internal/proc"
)

// HashSkeletonFiles computes sha256 hashes for each worktree-relative path.
// Results are sorted by path so the baseline is reproducible.
func HashSkeletonFiles(workDir string, paths []string) ([]SkeletonHash, error) {
	out := make([]SkeletonHash, 0, len(paths))
	sorted := append([]string{}, paths...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		full := filepath.Join(workDir, rel)
		if err := ensureRealPathInsideWorktree(workDir, full); err != nil {
			return nil, fmt.Errorf("hash skeleton %s: %w", rel, err)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("hash skeleton %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		out = append(out, SkeletonHash{Path: rel, SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

type preflightFileFingerprint struct {
	Kind string
	Mode os.FileMode
	Size int64
	Hash string
}

func snapshotPreflightFiles(ctx context.Context, root, excludeRoot, gitBin string) (map[string]preflightFileFingerprint, error) {
	files := map[string]preflightFileFingerprint{}
	if root == "" {
		return files, fmt.Errorf("workdir is required for preflight snapshot")
	}
	excludeRel := cleanContainedRel(root, excludeRoot)
	result, err := proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: root,
		Argv:    proc.GitArgs(gitBin, "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z"),
	}, proc.RunOptions{TailBytes: -1})
	if err != nil {
		return nil, fmt.Errorf("list git-visible preflight files: %w", err)
	}
	for _, raw := range strings.Split(result.Stdout, "\x00") {
		if raw == "" {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(raw))
		if excludeRel != "" && (rel == excludeRel || strings.HasPrefix(rel, excludeRel+string(filepath.Separator))) {
			continue
		}
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("snapshot preflight file %s: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if err := ensureRealPathInsideWorktree(root, path); err != nil {
				return nil, fmt.Errorf("snapshot preflight file %s: %w", rel, err)
			}
		}
		fp := preflightFileFingerprint{Mode: info.Mode(), Size: info.Size()}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("snapshot preflight file %s: %w", rel, err)
			}
			sum := sha256.Sum256(data)
			fp.Kind = "file"
			fp.Hash = hex.EncodeToString(sum[:])
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return nil, fmt.Errorf("snapshot preflight file %s: %w", rel, err)
			}
			fp.Kind = "symlink"
			fp.Hash = target
		default:
			fp.Kind = info.Mode().String()
		}
		files[rel] = fp
	}
	return files, nil
}

func cleanContainedRel(root, path string) string {
	if root == "" || path == "" {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return rel
}

func diffPreflightSnapshots(before, after map[string]preflightFileFingerprint) []string {
	changed := map[string]bool{}
	for path, afterFP := range after {
		beforeFP, ok := before[path]
		if !ok || beforeFP != afterFP {
			changed[path] = true
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed[path] = true
		}
	}
	out := make([]string, 0, len(changed))
	for path := range changed {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// HashesMatchBaseline reports whether the workdir contents at every baseline
// path still match the recorded hashes. Used by the progress detector to
// decide whether a clean diff is genuinely a no-progress attempt or whether
// the executor changed at least one skeleton.
func HashesMatchBaseline(workDir string, baseline Baseline) (bool, error) {
	if len(baseline.SkeletonHashes) == 0 {
		return true, nil
	}
	for _, h := range baseline.SkeletonHashes {
		fullPath := filepath.Join(workDir, h.Path)
		if err := ensureRealPathInsideWorktree(workDir, fullPath); err != nil {
			return false, nil
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			// A missing skeleton means the executor altered baseline state;
			// treat that as progress so the no-diff invariant cannot trigger.
			return false, nil
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != h.SHA256 {
			return false, nil
		}
	}
	return true, nil
}
