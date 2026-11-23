package domain

import (
	"fmt"
	"regexp"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// checkpointIDShape is the only checkpoint ID the decider accepts. The daemon
// mints them as ck_<hex>; anything else is a client forging a checkpoint.
var checkpointIDShape = regexp.MustCompile(`^ck_[a-z0-9]+$`)

// decideTurnBaseline turns a turn's baseline capture into its event. It runs
// after the turn's MessageSent in the same write, so the anchor message and its
// baseline are never durable apart. The turn is the one the message opens.
func decideTurnBaseline(cmd contracts.Command, s *ThreadState) (contracts.EventPayload, error) {
	c := cmd.Checkpoint
	if c.Phase != contracts.CheckpointBaseline {
		return nil, fmt.Errorf("%w: a turn carries a baseline, not a %q capture", ErrInvalidCommand, c.Phase)
	}
	if err := validateCheckpointCapture(c); err != nil {
		return nil, err
	}
	if c.Ref == "" || c.Tree == "" {
		return nil, fmt.Errorf("%w: a baseline needs a ref and a tree", ErrInvalidCommand)
	}
	if _, taken := findCheckpoint(s, c.CheckpointID); taken {
		return nil, fmt.Errorf("%w: checkpoint %q already exists", ErrInvalidCommand, c.CheckpointID)
	}
	return contracts.CheckpointCaptured{
		CheckpointID:    c.CheckpointID,
		Turn:            s.Turn + 1, // the turn's own MessageSent opens it
		AnchorMessageID: cmd.ID,
		Phase:           contracts.CheckpointBaseline,
		Ref:             c.Ref,
		Tree:            c.Tree,
	}, nil
}

// decideCheckpointCapture records a turn's result snapshot. The turn and anchor
// come from the checkpoint the baseline created, never from the client. Events
// are immutable, so a checkpoint takes exactly one result.
func decideCheckpointCapture(cmd contracts.Command, s *ThreadState) ([]contracts.EventPayload, error) {
	if !s.Exists {
		return nil, ErrThreadNotFound
	}
	c := cmd.Checkpoint
	if c == nil {
		return nil, fmt.Errorf("%w: checkpoint payload is required", ErrInvalidCommand)
	}
	if c.Phase != contracts.CheckpointResult {
		return nil, fmt.Errorf("%w: only a result can be captured after its turn started", ErrInvalidCommand)
	}
	if err := validateCheckpointCapture(c); err != nil {
		return nil, err
	}
	cp, ok := findCheckpoint(s, c.CheckpointID)
	if !ok {
		return nil, fmt.Errorf("%w: checkpoint %q has no baseline", ErrInvalidCommand, c.CheckpointID)
	}
	if cp.Ready || cp.Error != "" {
		return nil, fmt.Errorf("%w: checkpoint %q already has its result", ErrInvalidCommand, c.CheckpointID)
	}
	return []contracts.EventPayload{contracts.CheckpointCaptured{
		CheckpointID:    cp.ID,
		Turn:            cp.Turn,
		AnchorMessageID: cp.AnchorMessageID,
		Phase:           contracts.CheckpointResult,
		Ref:             c.Ref,
		Tree:            c.Tree,
		Files:           c.Files,
		Additions:       c.Additions,
		Deletions:       c.Deletions,
		Error:           c.Error,
	}}, nil
}

// decideRewind removes an anchor message and every later message. Restoring
// files is the transport's job and happens before this event is dispatched; the
// decider only checks that the target is a valid anchor and the thread is idle.
func decideRewind(cmd contracts.Command, s *ThreadState) ([]contracts.EventPayload, error) {
	if !s.Exists {
		return nil, ErrThreadNotFound
	}
	r := cmd.Rewind
	if r == nil || r.ToMessageID == "" {
		return nil, fmt.Errorf("%w: a rewind names the message to rewind to", ErrInvalidCommand)
	}
	for id, a := range s.Agents {
		if !a.Status.Terminal() {
			return nil, fmt.Errorf("%w: agent %q is still %s", ErrInvalidCommand, id, a.Status)
		}
	}
	cut := -1
	for i, m := range s.Messages {
		if m.ID == r.ToMessageID {
			cut = i
			break
		}
	}
	if cut < 0 {
		return nil, fmt.Errorf("%w: message %q is not in the transcript", ErrInvalidCommand, r.ToMessageID)
	}
	target := s.Messages[cut]
	if !opensTurn(s, target) {
		return nil, fmt.Errorf("%w: message %q did not open a turn", ErrInvalidCommand, r.ToMessageID)
	}
	if r.RestoreFiles {
		if !hasBaseline(s, target.Turn) {
			return nil, fmt.Errorf("%w: checkpoint unavailable for this turn; rewind the conversation only", ErrInvalidCommand)
		}
		if r.SafetyRef == "" {
			return nil, fmt.Errorf("%w: restoring files needs a safety ref", ErrInvalidCommand)
		}
	}
	dropped := make([]string, 0, len(s.Messages)-cut)
	for _, m := range s.Messages[cut:] {
		dropped = append(dropped, m.ID)
	}
	return []contracts.EventPayload{contracts.ThreadRewound{
		ToMessageID:       target.ID,
		Turn:              target.Turn,
		RestoreFiles:      r.RestoreFiles,
		SafetyRef:         r.SafetyRef,
		DroppedMessageIDs: dropped,
	}}, nil
}

// opensTurn reports whether m is a turn-opening message: the user's, or the
// controller's relaunch. Steering and reports never open a turn.
func opensTurn(s *ThreadState, m Message) bool {
	if m.Turn < 1 || m.Turn > s.Turn {
		return false
	}
	return m.Author == "user" || (s.ControlledBy != "" && m.Author == string(s.ControlledBy))
}

// hasBaseline reports whether the turn has a checkpoint whose baseline was taken.
// A checkpoint with no baseline cannot be restored to.
func hasBaseline(s *ThreadState, turn int) bool {
	for _, c := range s.Checkpoints {
		if c.Turn == turn && c.BaselineTree != "" {
			return true
		}
	}
	return false
}

func findCheckpoint(s *ThreadState, id string) (contracts.Checkpoint, bool) {
	for _, c := range s.Checkpoints {
		if c.ID == id {
			return c, true
		}
	}
	return contracts.Checkpoint{}, false
}

func validateCheckpointCapture(c *contracts.CheckpointCapture) error {
	if !checkpointIDShape.MatchString(c.CheckpointID) {
		return fmt.Errorf("%w: checkpoint id %q is not ck_<hex>", ErrInvalidCommand, c.CheckpointID)
	}
	return nil
}
