// Package worktree manages the git worktrees that give a thread its own
// checkout of a project. A thread works on a fresh branch in a worktree; the
// project's own checkout is never switched or edited by an agent.
//
// Worktrees live in a sibling directory, <repo>.worktrees/, rather than inside
// the project tree: the project's git status, file tree and searches stay
// clean, and nothing lands in the daemon's StateDir.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Errors callers match on. Anything else from Create means "no worktree here";
// the caller falls back to the project path.
var (
	ErrNotRepoRoot = errors.New("project path is not a git repository root")
	ErrNoBase      = errors.New("repository has no default branch to start from")
	ErrDirty       = errors.New("worktree has uncommitted changes")
	ErrUnmanaged   = errors.New("path is not a worktree managed for this project")
)

// Info is one managed worktree with its live git facts.
type Info struct {
	Path   string
	Branch string
	Base   string
	// Exists is false when the directory is gone; such a worktree is prunable.
	Exists   bool
	Prunable bool
	Dirty    bool
	// Ahead and Behind count commits relative to Base.
	Ahead  int
	Behind int
}

// Dir is the directory that holds a project's worktrees.
func Dir(repo string) string {
	return filepath.Clean(repo) + ".worktrees"
}

// Resolve picks the directory agents run in: the thread's worktree when it is
// still on disk, otherwise the fallback (the project path).
func Resolve(ref *contracts.WorktreeRef, fallback string) string {
	if ref != nil && ref.Path != "" {
		if fi, err := os.Stat(ref.Path); err == nil && fi.IsDir() {
			return ref.Path
		}
	}
	return fallback
}

// Create cuts a fresh branch off the repository's default branch and checks it
// out in a new worktree for threadID. The repo must be a repository root, and
// the default branch must already have a commit.
func Create(ctx context.Context, repo, title, threadID string) (contracts.WorktreeRef, error) {
	if err := checkRepoRoot(ctx, repo); err != nil {
		return contracts.WorktreeRef{}, err
	}
	base, baseRef, err := defaultBase(ctx, repo)
	if err != nil {
		return contracts.WorktreeRef{}, err
	}
	if err := os.MkdirAll(Dir(repo), 0o755); err != nil {
		return contracts.WorktreeRef{}, err
	}

	name := slug(title) + "-" + shortID(threadID)
	branch, path := "agenttunnel/"+name, filepath.Join(Dir(repo), name)
	for i := 2; exists(ctx, repo, branch) || pathExists(path); i++ {
		branch = fmt.Sprintf("agenttunnel/%s-%d", name, i)
		path = filepath.Join(Dir(repo), fmt.Sprintf("%s-%d", name, i))
	}

	if _, err := git(ctx, repo, "worktree", "add", "-b", branch, path, baseRef); err != nil {
		return contracts.WorktreeRef{}, err
	}
	return contracts.WorktreeRef{Path: path, Branch: branch, Base: base}, nil
}

// List reports every worktree of the project that lives in Dir(repo), with
// live git facts. Worktrees git knows about but that sit elsewhere are left
// alone: they are not ours to show or remove. A project that is not a
// repository root has none, so it lists as empty.
func List(ctx context.Context, repo string) ([]Info, error) {
	if err := checkRepoRoot(ctx, repo); err != nil {
		return nil, nil
	}
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	base, _, _ := defaultBase(ctx, repo)
	dir := canonical(Dir(repo))

	var infos []Info
	for _, rec := range parsePorcelain(out) {
		if !within(canonical(rec.path), dir) {
			continue
		}
		info := Info{Path: rec.path, Branch: rec.branch, Base: base, Prunable: rec.prunable}
		if fi, err := os.Stat(rec.path); err == nil && fi.IsDir() {
			info.Exists = true
		} else {
			info.Prunable = true
		}
		if info.Exists {
			status, _ := git(ctx, rec.path, "status", "--porcelain")
			info.Dirty = strings.TrimSpace(status) != ""
			if base != "" && rec.branch != "" {
				counts, err := git(ctx, rec.path, "rev-list", "--left-right", "--count", base+"..."+rec.branch)
				if err == nil {
					info.Behind, info.Ahead = parseCounts(counts)
				}
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Remove deletes a managed worktree. A dirty worktree is refused unless force is
// set. The branch is deleted only when git considers it merged, so unmerged
// work is never lost with the directory.
func Remove(ctx context.Context, repo, path string, force bool) error {
	infos, err := List(ctx, repo)
	if err != nil {
		return err
	}
	var target *Info
	for i := range infos {
		if canonical(infos[i].Path) == canonical(path) {
			target = &infos[i]
			break
		}
	}
	if target == nil {
		return ErrUnmanaged
	}
	if !target.Exists {
		if _, err := git(ctx, repo, "worktree", "prune"); err != nil {
			return err
		}
	} else {
		if target.Dirty && !force {
			return ErrDirty
		}
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		if _, err := git(ctx, repo, append(args, target.Path)...); err != nil {
			return err
		}
	}
	if target.Branch != "" {
		// Best effort: git refuses to delete an unmerged branch, which is the
		// point. The branch stays so the work is still reachable.
		_, _ = git(ctx, repo, "branch", "-d", target.Branch)
	}
	return nil
}

// Prune drops administrative records of worktrees whose directories are gone.
func Prune(ctx context.Context, repo string) error {
	_, err := git(ctx, repo, "worktree", "prune")
	return err
}

// checkRepoRoot requires repo to be the top of a git repository: a subdirectory
// would put the agent in a different place than the project path names.
func checkRepoRoot(ctx context.Context, repo string) error {
	top, err := git(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return ErrNotRepoRoot
	}
	if canonical(strings.TrimSpace(top)) != canonical(repo) {
		return ErrNotRepoRoot
	}
	return nil
}

// defaultBase returns the default branch's name and the ref to cut from. It
// prefers origin's HEAD, then local main or master.
func defaultBase(ctx context.Context, repo string) (name, ref string, err error) {
	if out, err := git(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if n := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); n != "" {
			if exists(ctx, repo, n) {
				return n, n, nil
			}
			if remoteExists(ctx, repo, n) {
				return n, "origin/" + n, nil
			}
		}
	}
	for _, n := range []string{"main", "master"} {
		if exists(ctx, repo, n) {
			return n, n, nil
		}
	}
	return "", "", ErrNoBase
}

// exists reports whether a local branch named branch has a commit.
func exists(ctx context.Context, repo, branch string) bool {
	_, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func remoteExists(ctx context.Context, repo, branch string) bool {
	_, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

// git runs one git command in dir and returns its stdout. Errors carry stderr.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

type porcelainRecord struct {
	path     string
	branch   string
	prunable bool
}

// parsePorcelain reads `git worktree list --porcelain`: blank-line separated
// records, each starting with a "worktree <path>" line.
func parsePorcelain(out string) []porcelainRecord {
	var recs []porcelainRecord
	var cur *porcelainRecord
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			recs = append(recs, porcelainRecord{path: strings.TrimPrefix(line, "worktree ")})
			cur = &recs[len(recs)-1]
		case cur == nil:
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.branch = strings.TrimPrefix(line, "branch refs/heads/")
		case strings.HasPrefix(line, "prunable"):
			cur.prunable = true
		}
	}
	return recs
}

// parseCounts reads `git rev-list --left-right --count base...branch`, which
// prints "<commits only in base>\t<commits only in branch>".
func parseCounts(out string) (behind, ahead int) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0
	}
	behind, _ = strconv.Atoi(fields[0])
	ahead, _ = strconv.Atoi(fields[1])
	return behind, ahead
}

// canonical resolves symlinks so /var and /private/var compare equal. A path
// that does not exist yet is compared as given.
func canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// The directory may not exist yet; resolve its parent so the prefix still
	// matches what git prints.
	parent, base := filepath.Split(abs)
	if real, err := filepath.EvalSymlinks(filepath.Clean(parent)); err == nil {
		return filepath.Join(real, base)
	}
	return abs
}

// SamePath reports whether two paths name the same directory, through symlinks.
func SamePath(a, b string) bool {
	return canonical(a) == canonical(b)
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// slug turns a thread title into a branch-safe fragment.
func slug(title string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "thread"
	}
	return s
}

// shortID keeps the random part of a thread id (th_<hex>) for the branch name.
func shortID(id string) string {
	id = strings.TrimPrefix(id, "th_")
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}
