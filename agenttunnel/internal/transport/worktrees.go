package transport

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

// WorktreeView is one managed worktree of a project, with live git facts and
// the threads that own it.
type WorktreeView struct {
	Path      string               `json:"path"`
	Branch    string               `json:"branch"`
	Base      string               `json:"base"`
	Exists    bool                 `json:"exists"`
	Prunable  bool                 `json:"prunable"`
	Dirty     bool                 `json:"dirty"`
	Ahead     int                  `json:"ahead"`
	Behind    int                  `json:"behind"`
	ThreadIDs []contracts.ThreadID `json:"threadIds"`
}

// projectPath returns the checkout a project's worktrees are cut from.
func (d *Dispatcher) projectPath(projectID string) (string, error) {
	if d.settings == nil {
		return "", errors.New("settings are not available")
	}
	project, ok := d.settings.Project(projectID)
	if !ok || project.Path == "" {
		return "", fmt.Errorf("project %q has no path", projectID)
	}
	return project.Path, nil
}

// ensureWorktree is the turn-start gate: it makes the thread's worktree real
// before the turn's agents run. It returns an error only when the thread declares
// a worktree that cannot be restored, which fails the turn. A thread with no
// worktree that cannot get one runs on the project path instead; that fallback is
// audited once per thread, not on every turn.
//
//   - no worktree, top-level, git project: cut and attach the first one.
//   - own worktree, directory gone: cut a fresh one and attach it in its place.
//   - inherited worktree, directory gone: re-point at the parent's live worktree.
//   - live worktree: nothing to do.
func (d *Dispatcher) ensureWorktree(ctx context.Context, id contracts.ThreadID) error {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return err
	}
	if !st.Exists || st.ProjectID == "" {
		return nil
	}
	switch {
	case st.Worktree == nil:
		d.cutFirstWorktree(ctx, id, st.ParentThreadID)
		return nil
	case dirExists(st.Worktree.Path):
		return nil
	case st.Worktree.Inherited:
		return d.refollowParent(ctx, id, st.ParentThreadID)
	default:
		return d.replaceWorktree(ctx, id)
	}
}

// cutFirstWorktree gives a top-level thread its first worktree. A subthread with
// no ref sits in a parent that never got one, so it stays on the project path.
// A failed cut is retried on the next turn, so a project that became a repository
// since is picked up without any restart.
func (d *Dispatcher) cutFirstWorktree(ctx context.Context, id contracts.ThreadID, parent contracts.ThreadID) {
	if parent != "" {
		return
	}
	if _, err := d.attachWorktree(ctx, id); err != nil {
		d.noteWorktreeFailure(id, err)
		return
	}
	d.clearWorktreeFailure(id)
}

// replaceWorktree cuts a fresh worktree for a thread whose own directory is gone
// and swaps it in. The new one is cut before the old ref is cleared, so a failed
// cut leaves the ref in place and the next turn retries; the thread never
// degrades to the project path.
func (d *Dispatcher) replaceWorktree(ctx context.Context, id contracts.ThreadID) error {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return err
	}
	lost := st.Worktree.Path
	repo, err := d.projectPath(st.ProjectID)
	if err != nil {
		return fmt.Errorf("worktree %s is missing and thread %s has no project to recut it from: %w", lost, id, err)
	}
	ref, err := worktree.Create(ctx, repo, st.Title, string(id))
	if err != nil {
		return fmt.Errorf("worktree %s is missing and a new one could not be cut for thread %s: %w", lost, id, err)
	}
	// One event swaps the ref: there is no moment where the thread holds none.
	if err := d.dispatchWorktree(ctx, contracts.Command{Type: contracts.CmdWorktreeReplace, ThreadID: id, Worktree: &ref}); err != nil {
		_ = worktree.Remove(context.WithoutCancel(ctx), repo, ref.Path, false)
		return err
	}
	d.auditLog("worktree.replaced", map[string]any{"threadId": string(id), "lost": lost, "path": ref.Path})
	return nil
}

// refollowParent re-points a subthread at its parent's live worktree. Subthreads
// never cut their own; if the parent has no live worktree, the turn fails until the
// parent's own turn restores it.
func (d *Dispatcher) refollowParent(ctx context.Context, id, parentID contracts.ThreadID) error {
	parent, err := d.engine.State(ctx, parentID)
	if err != nil {
		return fmt.Errorf("subthread %s shares a worktree that is missing, and parent thread %s is unavailable: %w", id, parentID, err)
	}
	if parent.Worktree == nil || !dirExists(parent.Worktree.Path) {
		return fmt.Errorf("subthread %s shares a worktree that is missing, and parent thread %s has no live worktree; run the parent thread's turn first", id, parentID)
	}
	return d.dispatchWorktree(ctx, contracts.Command{Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: inheritWorktree(parent.Worktree)})
}

// detachOwners clears the worktree from every top-level thread that owns path.
// Subthreads keep their inherited ref; the turn-start gate re-points them.
func (d *Dispatcher) detachOwners(ctx context.Context, path string) error {
	ids, err := d.engine.Threads(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		st, err := d.engine.State(ctx, id)
		if err != nil || st.Worktree == nil || st.Worktree.Inherited || !worktree.SamePath(st.Worktree.Path, path) {
			continue
		}
		if err := d.dispatchWorktree(ctx, contracts.Command{Type: contracts.CmdWorktreeDetach, ThreadID: id}); err != nil {
			return err
		}
	}
	return nil
}

// dispatchWorktree sends one worktree command with a fresh id.
func (d *Dispatcher) dispatchWorktree(ctx context.Context, cmd contracts.Command) error {
	cmd.ID = d.nextID()
	return d.engine.Dispatch(ctx, cmd)
}

// noteWorktreeFailure audits a failed first cut, but only when its reason differs
// from the last one recorded for the thread. Every turn retries, so an unchanged
// failure (a plain folder, an empty repo) is logged once, not on every turn.
func (d *Dispatcher) noteWorktreeFailure(id contracts.ThreadID, err error) {
	reason := err.Error()
	d.worktreeMu.Lock()
	if d.worktreeFailures == nil {
		d.worktreeFailures = map[contracts.ThreadID]string{}
	}
	if d.worktreeFailures[id] == reason {
		d.worktreeMu.Unlock()
		return
	}
	d.worktreeFailures[id] = reason
	d.worktreeMu.Unlock()
	d.auditLog("worktree.fallback", map[string]any{"threadId": string(id), "error": reason})
}

// clearWorktreeFailure forgets a thread's recorded failure once its cut succeeds,
// so a later failure is audited again.
func (d *Dispatcher) clearWorktreeFailure(id contracts.ThreadID) {
	d.worktreeMu.Lock()
	defer d.worktreeMu.Unlock()
	delete(d.worktreeFailures, id)
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// attachWorktree cuts a worktree for thread id and records it on the thread.
// The ref is created first and attached through the decider; if the attach is
// refused (the thread gained a worktree meanwhile) the new one is removed again.
func (d *Dispatcher) attachWorktree(ctx context.Context, id contracts.ThreadID) (contracts.WorktreeRef, error) {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return contracts.WorktreeRef{}, err
	}
	if !st.Exists {
		return contracts.WorktreeRef{}, fmt.Errorf("%w: thread %q", domain.ErrThreadNotFound, id)
	}
	repo, err := d.projectPath(st.ProjectID)
	if err != nil {
		return contracts.WorktreeRef{}, err
	}
	ref, err := worktree.Create(ctx, repo, st.Title, string(id))
	if err != nil {
		return contracts.WorktreeRef{}, err
	}
	if err := d.dispatchWorktree(ctx, contracts.Command{Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: &ref}); err != nil {
		_ = worktree.Remove(context.WithoutCancel(ctx), repo, ref.Path, false)
		return contracts.WorktreeRef{}, err
	}
	return ref, nil
}

// createWorktreeRef cuts a worktree for a thread that does not exist yet, so the
// ref can ride its creation event. Nil means the project is not a git checkout
// or the cut failed; the thread then runs on the project path.
func (d *Dispatcher) createWorktreeRef(ctx context.Context, projectID, title string, id contracts.ThreadID) *contracts.WorktreeRef {
	repo, err := d.projectPath(projectID)
	if err != nil {
		return nil
	}
	ref, err := worktree.Create(ctx, repo, title, string(id))
	if err != nil {
		d.auditLog("worktree.fallback", map[string]any{"threadId": string(id), "error": err.Error()})
		return nil
	}
	return &ref
}

// inheritWorktree is the parent's worktree as a subthread sees it: the same
// directory, never owned by the subthread.
func inheritWorktree(parent *contracts.WorktreeRef) *contracts.WorktreeRef {
	if parent == nil {
		return nil
	}
	ref := *parent
	ref.Inherited = true
	return &ref
}

// threadRunning reports whether a turn holds the thread's slot.
func (d *Dispatcher) threadRunning(id contracts.ThreadID) bool {
	d.turnsMu.Lock()
	defer d.turnsMu.Unlock()
	_, running := d.turns[id]
	return running
}

// worktreeCreate gives an existing thread its worktree on request. Unlike the
// automatic path, failures are returned to the caller.
func (d *Dispatcher) worktreeCreate(ctx context.Context, id contracts.ThreadID) (any, error) {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return nil, err
	}
	if st.Worktree != nil {
		return nil, fmt.Errorf("thread %q already has a worktree", id)
	}
	if st.ParentThreadID != "" {
		return nil, fmt.Errorf("subthread %q shares its parent's worktree", id)
	}
	ref, err := d.attachWorktree(ctx, id)
	if err != nil {
		return nil, err
	}
	return ref, nil
}

// worktreeList reports a project's managed worktrees. A project that is not a
// git checkout has none, so it lists as empty rather than failing.
func (d *Dispatcher) worktreeList(ctx context.Context, projectID string) (any, error) {
	repo, err := d.projectPath(projectID)
	if err != nil {
		return nil, err
	}
	infos, err := worktree.List(ctx, repo)
	if err != nil {
		return nil, err
	}

	owners := map[string][]contracts.ThreadID{}
	if ids, err := d.engine.Threads(ctx); err == nil {
		for _, id := range ids {
			st, err := d.engine.State(ctx, id)
			if err != nil || st.ProjectID != projectID || st.Worktree == nil || st.Worktree.Inherited {
				continue
			}
			owners[st.Worktree.Branch] = append(owners[st.Worktree.Branch], id)
		}
	}

	out := make([]WorktreeView, 0, len(infos))
	for _, info := range infos {
		ids := owners[info.Branch]
		if ids == nil {
			ids = []contracts.ThreadID{}
		}
		out = append(out, WorktreeView{
			Path: info.Path, Branch: info.Branch, Base: info.Base,
			Exists: info.Exists, Prunable: info.Prunable, Dirty: info.Dirty,
			Ahead: info.Ahead, Behind: info.Behind, ThreadIDs: ids,
		})
	}
	return out, nil
}

// worktreeRemove deletes a managed worktree. It is refused while a thread that
// works in it runs a turn, and a dirty worktree is refused unless force is set.
func (d *Dispatcher) worktreeRemove(ctx context.Context, projectID, path string, force bool) (any, error) {
	repo, err := d.projectPath(projectID)
	if err != nil {
		return nil, err
	}
	if ids, err := d.engine.Threads(ctx); err == nil {
		for _, id := range ids {
			st, err := d.engine.State(ctx, id)
			if err != nil || st.Worktree == nil || !worktree.SamePath(st.Worktree.Path, path) {
				continue
			}
			if d.threadRunning(id) {
				return nil, fmt.Errorf("thread %q is running in this worktree; stop it first", id)
			}
		}
	}
	if err := worktree.Remove(ctx, repo, path, force); err != nil {
		return nil, err
	}
	if err := d.detachOwners(ctx, path); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// worktreePrune drops records of worktrees whose directories were deleted by hand.
func (d *Dispatcher) worktreePrune(ctx context.Context, projectID string) (any, error) {
	repo, err := d.projectPath(projectID)
	if err != nil {
		return nil, err
	}
	if err := worktree.Prune(ctx, repo); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}
