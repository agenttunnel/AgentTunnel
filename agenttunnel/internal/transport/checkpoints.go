package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/checkpoint"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/gitdiff"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

// checkpointTimeout bounds one snapshot. A slow capture skips the checkpoint;
// it never fails the turn it belongs to.
const checkpointTimeout = 30 * time.Second

// turnCheckpoint is one turn's baseline while the turn runs. A nil value means
// the turn has no checkpoint: no store, no declared worktree, or a snapshot
// that could not be taken. Its methods are nil-safe, so callers never branch.
type turnCheckpoint struct {
	d        *Dispatcher
	threadID contracts.ThreadID
	workdir  string
	id       string
	baseRef  string
	baseTree string
}

// beginCheckpoint snapshots the worktree before a turn's anchor message is
// dispatched, and stores the baseline on cmd so the decider records it in the
// same write as the message. Only a thread with a declared worktree is
// captured: a thread on the project path would make file restore rewrite the
// user's own checkout.
func (d *Dispatcher) beginCheckpoint(ctx context.Context, threadID contracts.ThreadID, cmd *contracts.Command) *turnCheckpoint {
	if d.checkpoints == nil {
		return nil
	}
	st, err := d.engine.State(ctx, threadID)
	if err != nil || !st.Exists || st.Worktree == nil || st.Worktree.Path == "" {
		return nil
	}
	id := newCheckpointID("ck")
	ref, err := checkpoint.CheckpointRef(string(threadID), id, contracts.CheckpointBaseline)
	if err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	snap, err := d.checkpoints.Capture(cctx, st.Worktree.Path, ref)
	if err != nil {
		if !errors.Is(err, checkpoint.ErrNotGitRepo) {
			d.auditLog("checkpoint.baseline.failed", map[string]any{"threadId": string(threadID), "error": err.Error()})
		}
		return nil
	}
	cmd.Checkpoint = &contracts.CheckpointCapture{
		CheckpointID: id, Phase: contracts.CheckpointBaseline, Ref: snap.Ref, Tree: snap.Tree,
	}
	return &turnCheckpoint{d: d, threadID: threadID, workdir: st.Worktree.Path, id: id, baseRef: ref, baseTree: snap.Tree}
}

// abandon undoes a baseline whose turn never reached the event log, so no ref
// is left pointing at a checkpoint the thread does not have.
func (c *turnCheckpoint) abandon(ctx context.Context) {
	if c == nil {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointTimeout)
	defer cancel()
	if err := c.d.checkpoints.DropRef(dctx, c.workdir, c.baseRef); err != nil {
		c.d.auditLog("checkpoint.abandon.failed", map[string]any{"threadId": string(c.threadID), "error": err.Error()})
	}
}

// finish records the turn's result snapshot. It runs after the turn has
// stopped, failed or finished, and it runs even when the turn's context was
// cancelled, so a stopped turn that wrote files still gets its diff.
func (c *turnCheckpoint) finish(ctx context.Context) {
	if c == nil {
		return
	}
	base := context.WithoutCancel(ctx)
	cctx, cancel := context.WithTimeout(base, checkpointTimeout)
	defer cancel()
	payload := c.result(cctx)
	if err := c.d.engine.Dispatch(cctx, contracts.Command{ID: c.d.nextID(), Type: contracts.CmdCheckpointCapture, ThreadID: c.threadID, Checkpoint: payload}); err != nil {
		c.d.auditLog("checkpoint.result.failed", map[string]any{"threadId": string(c.threadID), "error": err.Error()})
	}
}

// result snapshots the worktree and reduces it to the event payload. A failed
// snapshot still produces a payload with Error set, so the UI shows the failure
// rather than a turn that never finishes.
func (c *turnCheckpoint) result(ctx context.Context) *contracts.CheckpointCapture {
	payload := &contracts.CheckpointCapture{CheckpointID: c.id, Phase: contracts.CheckpointResult}
	ref, err := checkpoint.CheckpointRef(string(c.threadID), c.id, contracts.CheckpointResult)
	if err != nil {
		payload.Error = err.Error()
		return payload
	}
	snap, err := c.d.checkpoints.Capture(ctx, c.workdir, ref)
	if err != nil {
		payload.Error = err.Error()
		return payload
	}
	stats, err := c.d.checkpoints.Stats(ctx, c.workdir, c.baseTree, snap.Tree)
	if err != nil {
		payload.Error = err.Error()
		return payload
	}
	payload.Ref = snap.Ref
	payload.Tree = snap.Tree
	payload.Files = stats.Files
	payload.Additions = stats.Additions
	payload.Deletions = stats.Deletions
	return payload
}

// newCheckpointID mints a checkpoint or safety id: the prefix and 8 random bytes
// in lowercase hex, the only shape the decider and the ref layout accept.
func newCheckpointID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// checkpointList returns a thread's checkpoints, never nil, so the wire shape is
// always a JSON array.
func (d *Dispatcher) checkpointList(ctx context.Context, threadID contracts.ThreadID) ([]contracts.Checkpoint, error) {
	st, err := d.engine.State(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if !st.Exists {
		return nil, fmt.Errorf("thread %q does not exist", threadID)
	}
	out := make([]contracts.Checkpoint, len(st.Checkpoints))
	copy(out, st.Checkpoints)
	return out, nil
}

// checkpointDiff diffs one checkpoint's recorded trees. The trees come from the
// event log, never from the client, so a request can only read what the thread
// captured.
func (d *Dispatcher) checkpointDiff(ctx context.Context, threadID contracts.ThreadID, checkpointID string) (gitdiff.Diff, error) {
	st, err := d.engine.State(ctx, threadID)
	if err != nil {
		return gitdiff.Diff{}, err
	}
	var cp *contracts.Checkpoint
	for i := range st.Checkpoints {
		if st.Checkpoints[i].ID == checkpointID {
			cp = &st.Checkpoints[i]
			break
		}
	}
	if cp == nil {
		return gitdiff.Diff{}, fmt.Errorf("checkpoint %q is not on thread %q", checkpointID, threadID)
	}
	switch {
	case cp.Error != "":
		return gitdiff.Diff{}, fmt.Errorf("snapshot failed: %s", cp.Error)
	case !cp.Ready:
		return gitdiff.Diff{}, errors.New("This turn did not finish; no diff to show.")
	}
	workdir := d.stateWorkdir(st)
	if workdir == "" {
		return gitdiff.Diff{}, errors.New("thread has no workdir")
	}
	return gitdiff.RangeDiff(ctx, workdir, cp.BaselineTree, cp.Tree)
}

// rewindParams is the thread.rewind request.
type rewindParams struct {
	ThreadID     contracts.ThreadID `json:"threadId"`
	ToMessageID  string             `json:"toMessageId"`
	RestoreFiles bool               `json:"restoreFiles"`
}

// RewindResult reports a rewind. Prompt is the removed anchor message, so the
// client can return it to the composer.
type RewindResult struct {
	OK           bool                      `json:"ok"`
	Turn         int                       `json:"turn"`
	CheckpointID string                    `json:"checkpointId,omitempty"`
	SafetyRef    string                    `json:"safetyRef,omitempty"`
	Prompt       *RewindPrompt             `json:"prompt,omitempty"`
	Restored     *checkpoint.RestoreReport `json:"restoredFiles,omitempty"`
}

// RewindPrompt is the text and images of the message a rewind removed.
type RewindPrompt struct {
	Text   string               `json:"text"`
	Images []contracts.ImageRef `json:"images,omitempty"`
}

// rewindThread rewinds a thread's conversation to before an anchor message,
// and with restoreFiles also restores the worktree to that turn's baseline. The
// order is fixed: validate, snapshot the current files as a safety ref, restore,
// then record the rewind. A failure before the record leaves the conversation
// untouched; a failure after the file restore puts the files back.
func (d *Dispatcher) rewindThread(ctx context.Context, p rewindParams) (RewindResult, error) {
	if p.ThreadID == "" || p.ToMessageID == "" {
		return RewindResult{}, errors.New("threadId and toMessageId are required")
	}
	// The turn slot refuses a rewind while a turn runs, and holds off any turn
	// that starts until the rewind lands.
	_, done, err := d.beginTurn(ctx, p.ThreadID)
	if err != nil {
		return RewindResult{}, err
	}
	defer done()

	st, err := d.engine.State(ctx, p.ThreadID)
	if err != nil {
		return RewindResult{}, err
	}
	// Dry run on a copy of the state: whatever the decider would refuse must
	// refuse before any file is touched.
	probe := contracts.Command{
		ID: "rewind-probe", Type: contracts.CmdThreadRewind, ThreadID: p.ThreadID,
		Rewind: &contracts.RewindRequest{ToMessageID: p.ToMessageID, RestoreFiles: p.RestoreFiles, SafetyRef: "probe"},
	}
	if _, err := domain.Decide(probe, st); err != nil {
		return RewindResult{}, err
	}
	target, ok := messageByID(st, p.ToMessageID)
	if !ok {
		return RewindResult{}, fmt.Errorf("message %q is not in the transcript", p.ToMessageID)
	}
	result := RewindResult{OK: true, Turn: target.Turn, Prompt: &RewindPrompt{Text: target.Text, Images: target.Images}}

	var restore *rewindRestore
	if p.RestoreFiles {
		restore, err = d.prepareFileRestore(ctx, st, target.Turn)
		if err != nil {
			return RewindResult{}, err
		}
		result.CheckpointID = restore.checkpointID
		result.SafetyRef = restore.safetyRef
		if err := restore.apply(ctx); err != nil {
			return RewindResult{}, err
		}
		result.Restored = &restore.report
	}

	dctx := context.WithoutCancel(ctx)
	err = d.engine.Dispatch(dctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdThreadRewind, ThreadID: p.ThreadID,
		Rewind: &contracts.RewindRequest{ToMessageID: p.ToMessageID, RestoreFiles: p.RestoreFiles, SafetyRef: result.SafetyRef},
	})
	if err != nil {
		if restore != nil {
			restore.undo(dctx)
		}
		return RewindResult{}, err
	}
	d.auditLog("thread.rewound", map[string]any{
		"threadId": string(p.ThreadID), "turn": target.Turn, "restoreFiles": p.RestoreFiles,
	})
	return result, nil
}

// rewindRestore is a file restore that has been planned and safety-snapshotted
// but not yet applied.
type rewindRestore struct {
	d            *Dispatcher
	threadID     contracts.ThreadID
	workdir      string
	checkpointID string
	safetyRef    string
	safetyTree   string
	targetTree   string
	report       checkpoint.RestoreReport
}

// prepareFileRestore checks that the turn can be restored to and takes the
// safety snapshot. It refuses a worktree another thread shares: restoring under
// a second live thread would rewrite files that thread is working on.
func (d *Dispatcher) prepareFileRestore(ctx context.Context, st *domain.ThreadState, turn int) (*rewindRestore, error) {
	if d.checkpoints == nil || st.Worktree == nil {
		return nil, errors.New("this thread has no worktree; rewind the conversation only")
	}
	var cp *contracts.Checkpoint
	for i := range st.Checkpoints {
		if st.Checkpoints[i].Turn == turn && st.Checkpoints[i].BaselineTree != "" {
			cp = &st.Checkpoints[i]
			break
		}
	}
	if cp == nil {
		return nil, errors.New("checkpoint unavailable for this turn; rewind the conversation only")
	}
	if other, err := d.sharedWorktreeThread(ctx, st.ID, st.Worktree.Path); err != nil {
		return nil, err
	} else if other != "" {
		return nil, fmt.Errorf("the worktree is shared with thread %s; rewind the conversation only", other)
	}
	rewindID := newCheckpointID("rw")
	safetyRef, err := checkpoint.SafetyRef(string(st.ID), rewindID)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	safety, err := d.checkpoints.Capture(cctx, st.Worktree.Path, safetyRef)
	if err != nil {
		return nil, fmt.Errorf("safety snapshot: %w", err)
	}
	return &rewindRestore{
		d: d, threadID: st.ID, workdir: st.Worktree.Path, checkpointID: cp.ID,
		safetyRef: safetyRef, safetyTree: safety.Tree, targetTree: cp.BaselineTree,
	}, nil
}

// apply writes the target tree into the worktree.
func (r *rewindRestore) apply(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	rep, err := r.d.checkpoints.Restore(rctx, r.workdir, r.targetTree)
	if err != nil {
		return fmt.Errorf("restore files: %w", err)
	}
	r.report = rep
	return nil
}

// undo puts the safety tree back after a rewind that could not be recorded.
func (r *rewindRestore) undo(ctx context.Context) {
	if _, err := r.d.checkpoints.Restore(ctx, r.workdir, r.safetyTree); err != nil {
		r.d.auditLog("checkpoint.undo.failed", map[string]any{"threadId": string(r.threadID), "error": err.Error()})
	}
}

// sharedWorktreeThread returns another live thread whose worktree is path, or
// "" when the worktree is this thread's alone. Subthreads inherit their parent's
// worktree, so a parent and its subthreads count as sharing it.
func (d *Dispatcher) sharedWorktreeThread(ctx context.Context, self contracts.ThreadID, path string) (contracts.ThreadID, error) {
	ids, err := d.engine.Threads(ctx)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if id == self {
			continue
		}
		st, err := d.engine.State(ctx, id)
		if err != nil || !st.Exists || st.Worktree == nil {
			continue
		}
		if worktree.SamePath(st.Worktree.Path, path) {
			return id, nil
		}
	}
	return "", nil
}

func messageByID(st *domain.ThreadState, id string) (domain.Message, bool) {
	for _, m := range st.Messages {
		if m.ID == id {
			return m, true
		}
	}
	return domain.Message{}, false
}

// dropCheckpointRefs removes a thread's checkpoint and safety refs once its
// worktree has been removed. The refs live in the repository, so the project
// path is the place to delete them from. Failure is audited, not returned: the
// worktree is already gone and the deletion should still complete.
func (d *Dispatcher) dropCheckpointRefs(ctx context.Context, st *domain.ThreadState, id contracts.ThreadID) {
	if d.checkpoints == nil {
		return
	}
	repo, err := d.projectPath(st.ProjectID)
	if err != nil {
		d.auditLog("checkpoint.refs.failed", map[string]any{"threadId": string(id), "error": err.Error()})
		return
	}
	if err := d.checkpoints.DeleteThread(ctx, repo, string(id)); err != nil {
		d.auditLog("checkpoint.refs.failed", map[string]any{"threadId": string(id), "error": err.Error()})
	}
}
