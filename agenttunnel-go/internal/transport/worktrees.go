package transport

import (
	"context"
	"errors"
	"fmt"

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

// ensureWorktree gives a top-level thread in a git project its own worktree
// before its first turn runs. Subthreads share their parent's worktree and
// never get one. A failure leaves the thread on the project path: a worktree
// is an isolation nicety, never a reason to refuse a turn.
func (d *Dispatcher) ensureWorktree(ctx context.Context, id contracts.ThreadID) {
	st, err := d.engine.State(ctx, id)
	if err != nil || st.Worktree != nil || st.ParentThreadID != "" || st.ProjectID == "" {
		return
	}
	if _, err := d.attachWorktree(ctx, id); err != nil {
		d.auditLog("worktree.fallback", map[string]any{"threadId": string(id), "error": err.Error()})
	}
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
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: &ref,
	}); err != nil {
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
