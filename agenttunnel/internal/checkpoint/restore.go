package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxReportPaths caps the paths a restore reports. The counts are not capped,
// only the lists the response ships.
const maxReportPaths = 500

// RestoreReport says what a restore changed in the worktree.
type RestoreReport struct {
	// Restored are the paths written from the target: files that were missing
	// or differed from it.
	Restored []string `json:"restored"`
	// Deleted are the paths removed because the target does not have them.
	Deleted []string `json:"deleted"`
	// Truncated is set when either list was capped at maxReportPaths.
	Truncated bool `json:"truncated"`
}

// Restore makes the tracked and untracked, non-ignored files of workdir equal
// to targetTree. Ignored files and .git are untouched. Uncommitted edits made
// after the target are overwritten or deleted, so callers take a safety snapshot
// first. If the restore fails part way, the prior state is put back before the
// error returns.
func (s *Store) Restore(ctx context.Context, workdir, targetTree string) (RestoreReport, error) {
	root, current, err := s.snapshotTree(ctx, workdir)
	if err != nil {
		return RestoreReport{}, err
	}
	rep, err := s.report(ctx, root, current, targetTree)
	if err != nil {
		return RestoreReport{}, err
	}
	if err := s.materialize(ctx, root, targetTree); err != nil {
		if rerr := s.materialize(ctx, root, current); rerr != nil {
			return RestoreReport{}, fmt.Errorf("restore failed: %w; putting the prior state back also failed: %v", err, rerr)
		}
		return RestoreReport{}, fmt.Errorf("restore failed, prior state put back: %w", err)
	}
	return rep, nil
}

// report lists what moving the worktree from current to target changes. It runs
// before any file is touched, so the list matches what the restore does.
func (s *Store) report(ctx context.Context, root, current, target string) (RestoreReport, error) {
	restored, err := diffNames(ctx, root, "AM", current, target)
	if err != nil {
		return RestoreReport{}, err
	}
	deleted, err := diffNames(ctx, root, "A", target, current)
	if err != nil {
		return RestoreReport{}, err
	}
	rep := RestoreReport{Restored: restored, Deleted: deleted}
	if len(rep.Restored) > maxReportPaths {
		rep.Restored = rep.Restored[:maxReportPaths]
		rep.Truncated = true
	}
	if len(rep.Deleted) > maxReportPaths {
		rep.Deleted = rep.Deleted[:maxReportPaths]
		rep.Truncated = true
	}
	return rep, nil
}

// materialize makes the worktree equal to tree: it deletes the files the tree
// does not have, then checks the tree out over the rest. It snapshots the
// worktree first, so it works from whatever state a failed step left behind.
func (s *Store) materialize(ctx context.Context, root, tree string) error {
	_, now, err := s.snapshotTree(ctx, root)
	if err != nil {
		return err
	}
	extra, err := diffNames(ctx, root, "A", tree, now)
	if err != nil {
		return err
	}
	for _, name := range extra {
		if err := removeFile(root, name); err != nil {
			return err
		}
	}
	return s.checkout(ctx, root, tree)
}

// checkout writes every file of tree into root through a temporary index, so the
// real index is untouched. checkout-index applies executable bits and symlinks.
func (s *Store) checkout(ctx context.Context, root, tree string) error {
	dir, err := os.MkdirTemp(s.root, "idx-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	env := indexEnv(dir)
	if _, err := git(ctx, root, env, "read-tree", tree); err != nil {
		return fmt.Errorf("restore index: %w", err)
	}
	if _, err := git(ctx, root, env, "checkout-index", "-a", "-f"); err != nil {
		return fmt.Errorf("restore checkout: %w", err)
	}
	return nil
}

// diffNames lists the paths whose status from a to b is one of filter (A added,
// M modified, D deleted), reading Git's NUL-separated output so names are never
// interpolated from user text.
func diffNames(ctx context.Context, root, filter, a, b string) ([]string, error) {
	out, err := git(ctx, root, nil, "diff", "--no-renames", "--name-only", "-z", "--diff-filter="+filter, a, b)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// removeFile deletes one path from the worktree. Only files and symlinks are
// removed; a directory is removed only when empty. Empty parents up to the root
// are pruned, so a file can become a directory and back.
func removeFile(root, name string) error {
	if !filepath.IsLocal(name) {
		return fmt.Errorf("restore refuses path outside the worktree: %q", name)
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if info.IsDir() {
			return fmt.Errorf("restore: directory %s is not empty", name)
		}
		return err
	}
	for dir := filepath.Dir(path); dir != root && strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			break
		}
	}
	return nil
}
