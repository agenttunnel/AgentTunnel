package contracts

import "time"

// MaxCheckpoints bounds the checkpoint read model. The event log and the Git
// refs keep every checkpoint; thread.get ships only the newest of them.
const MaxCheckpoints = 200

// Checkpoint is one turn's workspace snapshots. BaselineRef and Ref are commits
// under refs/agenttunnel/checkpoints/<thread>/<checkpoint>; they are not on any
// branch and never appear in git log.
type Checkpoint struct {
	ID              string    `json:"id"`
	Turn            int       `json:"turn"`
	AnchorMessageID string    `json:"anchorMessageId"`
	BaselineRef     string    `json:"baselineRef,omitempty"`
	BaselineTree    string    `json:"baselineTree,omitempty"`
	Ref             string    `json:"ref,omitempty"`
	Tree            string    `json:"tree,omitempty"`
	Files           int       `json:"files"`
	Additions       int       `json:"additions"`
	Deletions       int       `json:"deletions"`
	Ready           bool      `json:"ready"`
	Error           string    `json:"error,omitempty"`
	CapturedAt      time.Time `json:"capturedAt,omitempty"`
}

// Checkpoint capture phases.
const (
	CheckpointBaseline = "baseline"
	CheckpointResult   = "result"
)

// CheckpointCapture carries one phase's snapshot on a command. The decider turns
// it into a CheckpointCaptured event; it never trusts the anchor or turn fields,
// which it derives itself.
type CheckpointCapture struct {
	CheckpointID string `json:"checkpointId"`
	Phase        string `json:"phase"`
	Ref          string `json:"ref,omitempty"`
	Tree         string `json:"tree,omitempty"`
	Files        int    `json:"files,omitempty"`
	Additions    int    `json:"additions,omitempty"`
	Deletions    int    `json:"deletions,omitempty"`
	Error        string `json:"error,omitempty"`
}

// RewindRequest asks the decider to remove the anchor message toMessageID and
// everything after it. SafetyRef names the snapshot taken before any file
// restore, so the rewind stays reversible.
type RewindRequest struct {
	ToMessageID  string `json:"toMessageId"`
	RestoreFiles bool   `json:"restoreFiles,omitempty"`
	SafetyRef    string `json:"safetyRef,omitempty"`
}
