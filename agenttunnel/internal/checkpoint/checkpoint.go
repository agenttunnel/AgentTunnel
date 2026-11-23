// Package checkpoint snapshots a worktree's files into Git objects and restores
// them. Snapshots never touch the user's index, working tree, or branches: each
// capture builds its tree in a temporary index and anchors it under a ref in
// refs/agenttunnel/, so `git status` and `git log` in the worktree are unchanged.
package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ErrNotGitRepo means the directory is not inside a Git worktree. The turn runs
// without a checkpoint and only conversation rewind is offered.
var ErrNotGitRepo = errors.New("not a git worktree")

// Ref roots. Every ref a checkpoint writes lives under one of these, so a thread
// can be cleaned up by prefix.
const (
	refRoot    = "refs/agenttunnel/checkpoints"
	safetyRoot = "refs/agenttunnel/rewind-safety"
)

// idShape is the rule for ids that become ref path segments.
var idShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Snapshot is one captured workspace state: the commit that holds the tree, and
// the tree itself.
type Snapshot struct {
	Ref  string
	Tree string
}

// Stats is the change between two trees.
type Stats struct {
	Files     int
	Additions int
	Deletions int
}

// Store captures and restores snapshots. Root holds the temporary indexes a
// capture builds; it is created on first use.
type Store struct {
	root string
}

// New returns a store whose temporary indexes live under root.
func New(root string) *Store { return &Store{root: root} }

// CheckpointRef is the ref a checkpoint phase ("baseline" or "result") lives at.
func CheckpointRef(threadID, checkpointID, phase string) (string, error) {
	if !idShape.MatchString(threadID) || !idShape.MatchString(checkpointID) {
		return "", fmt.Errorf("checkpoint ref: invalid id %q/%q", threadID, checkpointID)
	}
	return fmt.Sprintf("%s/%s/%s/%s", refRoot, threadID, checkpointID, phase), nil
}

// SafetyRef is the ref a rewind's pre-restore snapshot lives at.
func SafetyRef(threadID, rewindID string) (string, error) {
	if !idShape.MatchString(threadID) || !idShape.MatchString(rewindID) {
		return "", fmt.Errorf("safety ref: invalid id %q/%q", threadID, rewindID)
	}
	return fmt.Sprintf("%s/%s/%s", safetyRoot, threadID, rewindID), nil
}

// Capture records the workspace of workdir as a commit under ref. The commit is
// unreachable from any branch; it exists only so the tree survives gc.
func (s *Store) Capture(ctx context.Context, workdir, ref string) (Snapshot, error) {
	root, tree, err := s.snapshotTree(ctx, workdir)
	if err != nil {
		return Snapshot{}, err
	}
	args := []string{"commit-tree", tree, "-m", "agenttunnel checkpoint"}
	if head, ok := resolveHead(ctx, root); ok {
		args = append(args, "-p", head)
	}
	commit, err := git(ctx, root, commitEnv(), args...)
	if err != nil {
		return Snapshot{}, fmt.Errorf("checkpoint commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if _, err := git(ctx, root, nil, "update-ref", ref, commit); err != nil {
		return Snapshot{}, fmt.Errorf("checkpoint ref %s: %w", ref, err)
	}
	return Snapshot{Ref: commit, Tree: tree}, nil
}

// Stats reports the files, additions and deletions between two trees. Binary
// files count as a file with no line changes. Renames are not detected, so
// each change is one add or one delete.
func (s *Store) Stats(ctx context.Context, workdir, baseTree, tree string) (Stats, error) {
	out, err := git(ctx, workdir, nil, "diff", "--numstat", "-z", "--no-renames", baseTree, tree)
	if err != nil {
		return Stats{}, err
	}
	return ParseNumstat(out), nil
}

// ParseNumstat reads `git diff --numstat -z` output: "adds\tdels\tpath\0" per
// file, with "-" for both counts on binary files.
func ParseNumstat(out string) Stats {
	var st Stats
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		st.Files++
		if n, err := strconv.Atoi(parts[0]); err == nil {
			st.Additions += n
		}
		if n, err := strconv.Atoi(parts[1]); err == nil {
			st.Deletions += n
		}
	}
	return st
}

// DropRef removes one ref, used to undo a baseline whose turn never started.
// The commit is left for git gc, like every other unreachable checkpoint.
func (s *Store) DropRef(ctx context.Context, workdir, ref string) error {
	root, err := toplevel(ctx, workdir)
	if err != nil {
		return err
	}
	_, err = git(ctx, root, nil, "update-ref", "-d", ref)
	return err
}

// DeleteThread removes every checkpoint and safety ref for threadID. Refs are
// shared by every worktree of the repository, so any path inside it will do.
// A directory that is not a Git worktree has no refs to remove.
func (s *Store) DeleteThread(ctx context.Context, workdir, threadID string) error {
	if !idShape.MatchString(threadID) {
		return fmt.Errorf("checkpoint delete: invalid thread id %q", threadID)
	}
	root, err := toplevel(ctx, workdir)
	if errors.Is(err, ErrNotGitRepo) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, prefix := range []string{refRoot + "/" + threadID + "/", safetyRoot + "/" + threadID + "/"} {
		out, err := git(ctx, root, nil, "for-each-ref", "--format=%(refname)", prefix)
		if err != nil {
			return err
		}
		for _, ref := range strings.Fields(out) {
			if _, err := git(ctx, root, nil, "update-ref", "-d", ref); err != nil {
				return fmt.Errorf("delete %s: %w", ref, err)
			}
		}
	}
	return nil
}

// snapshotTree builds the worktree's tree with a temporary index: HEAD's tree
// (or the empty tree before the first commit) plus every non-ignored change.
func (s *Store) snapshotTree(ctx context.Context, workdir string) (string, string, error) {
	root, err := toplevel(ctx, workdir)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return "", "", err
	}
	dir, err := os.MkdirTemp(s.root, "idx-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	env := indexEnv(dir)
	if head, ok := resolveHead(ctx, root); ok {
		_, err = git(ctx, root, env, "read-tree", head)
	} else {
		_, err = git(ctx, root, env, "read-tree", "--empty")
	}
	if err != nil {
		return "", "", fmt.Errorf("checkpoint index: %w", err)
	}
	if _, err := git(ctx, root, env, "add", "-A", "--", "."); err != nil {
		return "", "", fmt.Errorf("checkpoint add: %w", err)
	}
	tree, err := git(ctx, root, env, "write-tree")
	if err != nil {
		return "", "", fmt.Errorf("checkpoint tree: %w", err)
	}
	return root, strings.TrimSpace(tree), nil
}

// toplevel resolves the worktree root. Any failure means workdir is not a
// usable worktree, so the caller records no checkpoint.
func toplevel(ctx context.Context, workdir string) (string, error) {
	if workdir == "" {
		return "", ErrNotGitRepo
	}
	out, err := git(ctx, workdir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotGitRepo, err)
	}
	return strings.TrimSpace(out), nil
}

// resolveHead returns HEAD's commit, or false before the first commit.
func resolveHead(ctx context.Context, root string) (string, bool) {
	out, err := git(ctx, root, nil, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// git runs one git command in dir. env entries are added to the inherited
// environment; the repository-locating variables are stripped first so a
// daemon launched from inside another repository's hook cannot be redirected.
func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cleanEnv(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="),
			strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_INDEX_FILE="):
			continue
		}
		env = append(env, kv)
	}
	return env
}

func indexEnv(dir string) []string {
	return []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
}

// commitEnv gives checkpoint commits a fixed identity, so they never depend on
// the user's git config and never carry the user's name.
func commitEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=AgentTunnel",
		"GIT_AUTHOR_EMAIL=checkpoints@agenttunnel.local",
		"GIT_COMMITTER_NAME=AgentTunnel",
		"GIT_COMMITTER_EMAIL=checkpoints@agenttunnel.local",
	}
}
