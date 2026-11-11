package domain

import (
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Project applies a single event to the read model. It is pure and idempotent
// with respect to event ordering: replaying the same log yields the same state.
func Project(s *ThreadState, e contracts.Event) error {
	switch p := e.Payload.(type) {
	case contracts.ThreadCreated:
		s.Exists = true
		s.Title = p.Title
		s.ProjectID = p.ProjectID
		s.ParentThreadID = p.ParentThreadID
		s.Worktree = p.Worktree

	case contracts.WorktreeAttached:
		ref := p.Worktree
		s.Worktree = &ref

	case contracts.WorktreeDetached:
		s.Worktree = nil

	case contracts.WorktreeReplaced:
		ref := p.Worktree
		s.Worktree = &ref

	case contracts.ThreadRenamed:
		s.Title = p.Title

	case contracts.ThreadDeleted:
		s.Exists = false
		s.Deleted = true

	case contracts.MessageSent:
		s.Messages = append(s.Messages, Message{ID: p.MessageID, Author: p.Author, Text: p.Text, At: e.At})
		// A user message opens a turn; steering and reports stay in the current one.
		if p.Author == "user" {
			s.Turn++
			s.turnFor(s.Turn).At = e.At
		}

	case contracts.SteeringConsumed:
		consumed := make(map[string]bool, len(p.MessageIDs))
		for _, id := range p.MessageIDs {
			consumed[id] = true
		}
		for i := range s.Messages {
			if consumed[s.Messages[i].ID] {
				s.Messages[i].Consumed = true
			}
		}

	case contracts.ModelSet:
		s.Model = p.Selection

	case contracts.AutoModeSet:
		s.AutoMode = p.Enabled

	case contracts.ArchiveSet:
		s.Archived = p.Archived

	case contracts.PeerMailboxSet:
		s.PeerMailbox = p.Enabled

	case contracts.LeadsSet:
		s.Leads = p.Count

	case contracts.PRTracked:
		if !s.HasPullRequest(p.PullRequest.URL) {
			s.PullRequests = append(s.PullRequests, p.PullRequest)
		}

	case contracts.LeadRuntimeSet:
		for len(s.LeadRuntimes) <= p.Index {
			s.LeadRuntimes = append(s.LeadRuntimes, contracts.LeadRuntime{})
		}
		s.LeadRuntimes[p.Index] = p.Runtime

	case contracts.RoleRuntimeSet:
		if s.RoleRuntimes == nil {
			s.RoleRuntimes = make(map[string]contracts.LeadRuntime)
		}
		s.RoleRuntimes[string(p.Role)] = p.Runtime

	case contracts.SubthreadSpawned:
		s.Subthreads = append(s.Subthreads, p.ThreadID)

	case contracts.TaskAdded:
		s.Tasks = append(s.Tasks, TaskItem{Name: p.Name, Title: p.Title, Status: contracts.TaskTodo})

	case contracts.TaskStatusChanged:
		if i := s.taskIndex(p.Name); i >= 0 {
			s.Tasks[i].Status = p.Status
		}

	case contracts.TaskDispatched:
		if _, exists := s.Agents[p.AgentID]; !exists {
			s.AgentOrder = append(s.AgentOrder, p.AgentID)
		}
		s.Agents[p.AgentID] = contracts.Agent{
			ID:            p.AgentID,
			ParentAgentID: p.ParentAgentID,
			Name:          p.Name,
			ThreadID:      e.ThreadID,
			Role:          p.Role,
			Harness:       p.Harness,
			Task:          p.Task,
			Model:         p.Model,
			PipelineID:    p.PipelineID,
			DependsOn:     p.DependsOn,
			Status:        contracts.AgentPending,
			StartedAt:     e.At,
		}

	case contracts.AgentStatusChanged:
		a, ok := s.Agents[p.AgentID]
		if !ok {
			return fmt.Errorf("project: status for unknown agent %q", p.AgentID)
		}
		a.Status = p.Status
		if p.Status.Terminal() {
			t := e.At
			a.EndedAt = &t
			// A lead finishing is the team finishing the turn; keep the latest one.
			if a.IsOrchestrator() && s.Turn > 0 {
				turn := s.turnFor(s.Turn)
				turn.EndedAt = e.At
				if p.Status != contracts.AgentSucceeded {
					turn.Failed = true
				}
			}
		}
		s.Agents[p.AgentID] = a

	case contracts.UsageRecorded:
		cur := s.Usage[p.AgentID]
		cur.AgentID = p.AgentID
		cur.Role = p.Role
		cur.Usage = cur.Usage.Add(p.Usage)
		s.Usage[p.AgentID] = cur

		turn := s.turnFor(p.Turn)
		if _, seen := turn.Agents[p.AgentID]; !seen {
			turn.AgentOrder = append(turn.AgentOrder, p.AgentID)
		}
		turn.Agents[p.AgentID] = turn.Agents[p.AgentID].Add(p.Usage)

		// Keep the individual model steps for the agent's drill-in, bounded so a
		// long session cannot grow without limit.
		steps := append(s.Steps[p.AgentID], UsageStep{At: e.At, Usage: p.Usage})
		if len(steps) > 200 {
			steps = steps[len(steps)-200:]
		}
		s.Steps[p.AgentID] = steps

	case contracts.SharedContextSet:
		if _, exists := s.Shared[p.Key]; !exists {
			s.SharedOrder = append(s.SharedOrder, p.Key)
		}
		s.Shared[p.Key] = p.Value

	case contracts.MailboxPosted:
		s.Mailbox = append(s.Mailbox, MailboxMessage{From: p.From, To: p.To, Kind: p.Kind, Body: p.Body, At: e.At, Turn: s.Turn})
		if overflow := len(s.Mailbox) - maxMailboxEntries; overflow > 0 {
			s.Mailbox = append([]MailboxMessage(nil), s.Mailbox[overflow:]...)
		}

	case contracts.ReviewCommentAdded:
		if p.EndLine == 0 {
			p.EndLine = p.Line
		}
		s.ReviewComments = append(s.ReviewComments, ReviewComment{ReviewCommentAdded: p})

	case contracts.ReviewCommentsSubmitted:
		sent := make(map[string]bool, len(p.CommentIDs))
		for _, id := range p.CommentIDs {
			sent[id] = true
		}
		for i := range s.ReviewComments {
			if sent[s.ReviewComments[i].ID] {
				s.ReviewComments[i].Submitted = true
			}
		}

	case contracts.ReviewThreadOpened:
		c := p.ReviewCommentAdded
		if c.EndLine == 0 {
			c.EndLine = c.Line
		}
		s.ReviewThreads = append(s.ReviewThreads, ReviewThread{
			ID: c.ID, File: c.File, Line: c.Line, EndLine: c.EndLine, Side: c.Side,
			Body: c.Body, AuthorID: c.AuthorID, Status: contracts.ReviewStatusOpen,
			OpenedAt: c.CreatedAt, UpdatedAt: c.CreatedAt,
		})

	case contracts.ReviewThreadReplied:
		i := s.reviewThreadIndex(p.ThreadID)
		if i < 0 {
			return fmt.Errorf("project: reply to unknown review thread %q", p.ThreadID)
		}
		t := &s.ReviewThreads[i]
		t.Replies = append(t.Replies, ReviewReply{AuthorID: p.AuthorID, Body: p.Body, At: p.CreatedAt})
		t.UpdatedAt = p.CreatedAt

	case contracts.ReviewThreadStatusChanged:
		i := s.reviewThreadIndex(p.ThreadID)
		if i < 0 {
			return fmt.Errorf("project: status for unknown review thread %q", p.ThreadID)
		}
		t := &s.ReviewThreads[i]
		t.Status = p.Status
		t.UpdatedAt = p.CreatedAt
		if body := strings.TrimSpace(p.Body); body != "" {
			t.Replies = append(t.Replies, ReviewReply{AuthorID: p.AuthorID, Body: p.Body, At: p.CreatedAt})
		}

	case contracts.QuestionAsked:
		s.Questions = append(s.Questions, Question{QuestionAsked: p, Status: QuestionPending, At: e.At})
		s.trimQuestions()

	case contracts.QuestionClosed:
		q := s.question(p.QuestionID)
		if q == nil {
			return fmt.Errorf("project: close for unknown question %q", p.QuestionID)
		}
		q.Status = QuestionClosedStatus
		if p.Answered {
			q.Status = QuestionAnswered
		}
		q.Selected = p.Selected
		q.Text = p.Text
		q.Reason = p.Reason

	default:
		return fmt.Errorf("project: unknown event payload %T", e.Payload)
	}
	s.UpdatedAt = e.At
	s.Version = e.Seq
	return nil
}
