package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

var (
	ErrThreadExists   = errors.New("thread already exists")
	ErrThreadNotFound = errors.New("thread not found")
	ErrInvalidCommand = errors.New("invalid command")
)

// Decide is a pure function: given a command and the current state, it returns
// the events that command should produce, or an error if the command is invalid
// for that state. It performs no I/O.
func Decide(cmd contracts.Command, s *ThreadState) ([]contracts.EventPayload, error) {
	switch cmd.Type {
	case contracts.CmdThreadCreate:
		if s.Exists {
			return nil, ErrThreadExists
		}
		title := strings.TrimSpace(cmd.Title)
		if title == "" {
			title = contracts.DefaultThreadTitle
		}
		return []contracts.EventPayload{contracts.ThreadCreated{Title: title, ProjectID: cmd.ProjectID}}, nil

	case contracts.CmdThreadRename:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		title := strings.TrimSpace(cmd.Title)
		if title == "" {
			return nil, fmt.Errorf("%w: title is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.ThreadRenamed{Title: title}}, nil

	case contracts.CmdThreadDelete:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		// A thread with live agents would keep writing to a tombstoned log;
		// make the caller stop the turn first.
		for id, a := range s.Agents {
			if !a.Status.Terminal() {
				return nil, fmt.Errorf("%w: agent %q is still %s", ErrInvalidCommand, id, a.Status)
			}
		}
		return []contracts.EventPayload{contracts.ThreadDeleted{}}, nil

	case contracts.CmdTurnStart:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if strings.TrimSpace(cmd.Text) == "" {
			return nil, fmt.Errorf("%w: turn text is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{
			contracts.MessageSent{MessageID: cmd.ID, Author: "user", Text: cmd.Text},
		}, nil

	case contracts.CmdModelSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.ModelSelection == nil {
			return nil, fmt.Errorf("%w: model selection is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.ModelSet{Selection: *cmd.ModelSelection}}, nil

	case contracts.CmdAutoModeSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.AutoMode == nil {
			return nil, fmt.Errorf("%w: auto mode value is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.AutoModeSet{Enabled: *cmd.AutoMode}}, nil

	case contracts.CmdPeerMailboxSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.PeerMailbox == nil {
			return nil, fmt.Errorf("%w: peer mailbox value is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.PeerMailboxSet{Enabled: *cmd.PeerMailbox}}, nil

	case contracts.CmdTaskDispatch:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Dispatch == nil {
			return nil, fmt.Errorf("%w: dispatch payload is required", ErrInvalidCommand)
		}
		d := cmd.Dispatch
		if d.AgentID == "" {
			return nil, fmt.Errorf("%w: dispatch agent id is required", ErrInvalidCommand)
		}
		role := d.Role
		if role == "" {
			role = contracts.RoleExecutor
		}
		if d.Harness != "" && !d.Harness.Valid() {
			return nil, fmt.Errorf("%w: unknown harness %q", ErrInvalidCommand, d.Harness)
		}
		return []contracts.EventPayload{contracts.TaskDispatched{
			AgentID:       d.AgentID,
			ParentAgentID: d.ParentAgentID,
			Name:          d.Name,
			Role:          role,
			Task:          d.Task,
			Harness:       d.Harness,
			Model:         d.Model,
			PipelineID:    d.Pipeline,
			DependsOn:     d.DependsOn,
		}}, nil

	case contracts.CmdAgentStatusSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Status == nil {
			return nil, fmt.Errorf("%w: status payload is required", ErrInvalidCommand)
		}
		if _, ok := s.Agents[cmd.Status.AgentID]; !ok {
			return nil, fmt.Errorf("%w: unknown agent %q", ErrInvalidCommand, cmd.Status.AgentID)
		}
		return []contracts.EventPayload{*cmd.Status}, nil

	case contracts.CmdSharedContextSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.SharedContext == nil {
			return nil, fmt.Errorf("%w: shared context payload is required", ErrInvalidCommand)
		}
		if strings.TrimSpace(cmd.SharedContext.Key) == "" {
			return nil, fmt.Errorf("%w: shared context key is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{*cmd.SharedContext}, nil

	case contracts.CmdMailboxPost:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Mailbox == nil {
			return nil, fmt.Errorf("%w: mailbox payload is required", ErrInvalidCommand)
		}
		if !s.PeerMailbox {
			return nil, fmt.Errorf("%w: peer mailbox is disabled for this thread", ErrInvalidCommand)
		}
		return []contracts.EventPayload{*cmd.Mailbox}, nil

	case contracts.CmdMessageAppend:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Message == nil || strings.TrimSpace(cmd.Message.Text) == "" {
			return nil, fmt.Errorf("%w: message payload is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{*cmd.Message}, nil

	case contracts.CmdReviewCommentAdd:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.ReviewComment == nil ||
			strings.TrimSpace(cmd.ReviewComment.File) == "" ||
			strings.TrimSpace(cmd.ReviewComment.Body) == "" {
			return nil, fmt.Errorf("%w: review comment requires file and body", ErrInvalidCommand)
		}
		return []contracts.EventPayload{*cmd.ReviewComment}, nil

	case contracts.CmdUsageRecord:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Usage == nil {
			return nil, fmt.Errorf("%w: usage payload is required", ErrInvalidCommand)
		}
		u := *cmd.Usage
		if u.AgentID == "" {
			return nil, fmt.Errorf("%w: usage agent id is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{u}, nil

	default:
		return nil, fmt.Errorf("%w: unknown command type %q", ErrInvalidCommand, cmd.Type)
	}
}
