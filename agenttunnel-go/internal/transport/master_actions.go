package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// runMaster executes the validated actions of a home-screen reply, in order,
// and appends one result per action after the rejected proposals Send already
// recorded. Each action reuses the path its RPC method uses, so the chat, the
// RPC surface, and the WhatsApp control path agree on what an action does.
func (d *Dispatcher) runMaster(ctx context.Context, res *orchestrator.MasterResult) {
	for _, a := range res.Actions {
		r := d.runMasterAction(ctx, a)
		res.Results = append(res.Results, r)
		if !r.OK {
			res.Error = joinMasterErrors(res.Error, r.Error)
		}
		if a.Type == "start_thread" && r.OK && res.ThreadID == "" {
			res.ThreadID = r.ThreadID
			res.ProjectID = r.ProjectID
		}
	}
}

// joinMasterErrors appends one reason to the reply's error, keeping them in order.
func joinMasterErrors(all, next string) string {
	if all == "" {
		return next
	}
	return all + "; " + next
}

// runMasterAction runs one validated action. Every target is re-checked here,
// since the thread may have changed between the snapshot and now.
func (d *Dispatcher) runMasterAction(ctx context.Context, a orchestrator.MasterAction) orchestrator.MasterActionResult {
	out := orchestrator.MasterActionResult{Type: a.Type, ThreadID: a.ThreadID, ProjectID: a.ProjectID}
	var err error
	switch a.Type {
	case "start_thread":
		out.ThreadID, err = d.startMasterThread(ctx, a)
	case "send_message":
		err = d.sendMasterMessage(ctx, a.ThreadID, a.Prompt)
	case "stop_thread":
		if !d.stopTurn(a.ThreadID) {
			err = errors.New("no turn is running on this thread")
		}
	case "rename_thread":
		err = d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadRename, ThreadID: a.ThreadID, Title: a.Title,
		})
	case "set_peer_mailbox":
		err = d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdPeerMailboxSet, ThreadID: a.ThreadID, PeerMailbox: a.Enabled,
		})
	case "delete_thread":
		// The worktree is never removed from here: that stays an explicit
		// worktree action the user takes.
		err = d.deleteThread(ctx, a.ThreadID, false)
	default:
		err = fmt.Errorf("unsupported action %q", a.Type)
	}
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.OK = true
	return out
}

// startMasterThread creates the thread a home-screen proposal asked for, applies
// its lead-0 runtime, and starts the first turn in the background. It returns as
// soon as the goroutine is dispatched: thread.turn.start blocks for the whole
// turn, so awaiting it here would hold the home chat open.
func (d *Dispatcher) startMasterThread(ctx context.Context, a orchestrator.MasterAction) (contracts.ThreadID, error) {
	if d.engine == nil || d.orch == nil {
		return "", errors.New("orchestrator is unavailable")
	}
	id := d.newThreadID()
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: id,
		Title: a.Title, ProjectID: a.ProjectID,
	}); err != nil {
		return "", err
	}
	if a.Harness != "" || a.Model != "" || a.Variant != "" {
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdLeadRuntimeSet, ThreadID: id,
			LeadRuntime: &contracts.LeadRuntimeSet{
				Index: 0,
				Runtime: contracts.LeadRuntime{
					Harness: a.Harness, Model: a.Model, Variant: a.Variant,
				},
			},
		}); err != nil {
			return "", err
		}
	}

	cmd := contracts.Command{ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: id, Text: a.Prompt}
	// Detach from the request: the client may disconnect or time out, but the
	// turn it started keeps going.
	go func() {
		_, _ = d.runTurn(context.WithoutCancel(ctx), id, a.Prompt, cmd)
	}()
	return id, nil
}

// sendMasterMessage steers the thread when a turn is already running, and
// otherwise starts a turn on the message, detached so the reply does not wait.
func (d *Dispatcher) sendMasterMessage(ctx context.Context, id contracts.ThreadID, text string) error {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("thread %q does not exist", id)
	}
	if d.threadRunning(id) {
		return d.steerThread(ctx, id, text, text, nil, nil)
	}
	_, err = d.startDetachedTurn(ctx, id, text, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: id, Text: text,
	})
	return err
}

// steerThread records a steering message in the transcript, marked as steering,
// then queues its prompt for the running turn's next orchestrator pass to weigh.
// The message id lets that pass report back that it was consumed. Images ride
// both: the refs are recorded in the transcript, the inputs reach the pass.
func (d *Dispatcher) steerThread(ctx context.Context, threadID contracts.ThreadID, text, prompt string, refs []contracts.ImageRef, inputs []providers.ImageInput) error {
	if d.steering == nil {
		return errors.New("steering is unavailable")
	}
	messageID := d.nextID()
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID:       d.nextID(),
		Type:     contracts.CmdMessageAppend,
		ThreadID: threadID,
		Message:  &contracts.MessageSent{MessageID: messageID, Author: "steering", Text: text, Images: refs},
	}); err != nil {
		return err
	}
	d.steering.Post(threadID, messageID, prompt, inputs...)
	return nil
}
