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
		// Peer messaging ships on for new threads: agent coordination and the
		// graph view both rely on the traffic it records. Existing threads stay
		// as they were, and `thread.setPeerMailbox` remains the override.
		return []contracts.EventPayload{
			contracts.ThreadCreated{Title: title, ProjectID: cmd.ProjectID, ParentThreadID: cmd.ParentThreadID, Worktree: cmd.Worktree},
			contracts.PeerMailboxSet{Enabled: true},
		}, nil

	case contracts.CmdWorktreeAttach:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Worktree == nil || cmd.Worktree.Path == "" {
			return nil, fmt.Errorf("%w: worktree path is required", ErrInvalidCommand)
		}
		// A subthread only ever holds its parent's worktree, so it may attach (or
		// refresh) an inherited ref and nothing else. A top-level thread owns at
		// most one worktree, fixed once attached.
		if s.ParentThreadID != "" {
			if !cmd.Worktree.Inherited {
				return nil, fmt.Errorf("%w: a subthread shares its parent's worktree", ErrInvalidCommand)
			}
		} else {
			if cmd.Worktree.Inherited {
				return nil, fmt.Errorf("%w: only a subthread inherits a worktree", ErrInvalidCommand)
			}
			if s.Worktree != nil {
				return nil, fmt.Errorf("%w: thread already has a worktree", ErrInvalidCommand)
			}
		}
		return []contracts.EventPayload{contracts.WorktreeAttached{Worktree: *cmd.Worktree}}, nil

	case contracts.CmdWorktreeDetach:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if s.Worktree == nil || s.Worktree.Inherited {
			return nil, fmt.Errorf("%w: thread owns no worktree to detach", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.WorktreeDetached{}}, nil

	case contracts.CmdWorktreeReplace:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Worktree == nil || cmd.Worktree.Path == "" {
			return nil, fmt.Errorf("%w: worktree path is required", ErrInvalidCommand)
		}
		if s.Worktree == nil {
			return nil, fmt.Errorf("%w: thread has no worktree to replace", ErrInvalidCommand)
		}
		// Same ownership rules as attach: a subthread swaps in an inherited ref,
		// a top-level thread swaps in one it owns.
		if (s.ParentThreadID != "") != cmd.Worktree.Inherited {
			return nil, fmt.Errorf("%w: replacement must match the thread's worktree ownership", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.WorktreeReplaced{Worktree: *cmd.Worktree}}, nil

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
		images, err := validateImages(cmd.Images)
		if err != nil {
			return nil, err
		}
		return []contracts.EventPayload{
			contracts.MessageSent{MessageID: cmd.ID, Author: "user", Text: cmd.Text, Images: images},
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

	case contracts.CmdPRTrack:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		ref := cmd.PullRequest
		if ref == nil || ref.URL == "" || ref.Number <= 0 {
			return nil, fmt.Errorf("%w: pull request number and url are required", ErrInvalidCommand)
		}
		if s.HasPullRequest(ref.URL) {
			return nil, nil // already tracked: nothing to record
		}
		return []contracts.EventPayload{contracts.PRTracked{PullRequest: *ref}}, nil

	case contracts.CmdPeerMailboxSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.PeerMailbox == nil {
			return nil, fmt.Errorf("%w: peer mailbox value is required", ErrInvalidCommand)
		}
		return []contracts.EventPayload{contracts.PeerMailboxSet{Enabled: *cmd.PeerMailbox}}, nil

	case contracts.CmdLeadRuntimeSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.LeadRuntime == nil {
			return nil, fmt.Errorf("%w: lead runtime is required", ErrInvalidCommand)
		}
		set := *cmd.LeadRuntime
		// The team's leads, or the next slot to append one: the index can never
		// run past the team by more than one.
		if set.Index < 0 || set.Index > s.LeadCount() {
			return nil, fmt.Errorf("%w: lead index must be between 0 and %d", ErrInvalidCommand, s.LeadCount())
		}
		if set.Runtime.Harness != "" && !set.Runtime.Harness.Valid() {
			return nil, fmt.Errorf("%w: unknown harness %q", ErrInvalidCommand, set.Runtime.Harness)
		}
		return []contracts.EventPayload{set}, nil

	case contracts.CmdLeadsSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Leads == nil {
			return nil, fmt.Errorf("%w: leads count is required", ErrInvalidCommand)
		}
		if *cmd.Leads < 1 {
			return nil, fmt.Errorf("%w: leads must be at least 1", ErrInvalidCommand)
		}
		events := []contracts.EventPayload{contracts.LeadsSet{Count: *cmd.Leads}}
		// A team of leads agrees through peer messages, so a multi-lead thread
		// turns the peer mailbox on with it.
		if *cmd.Leads > 1 && !s.PeerMailbox {
			events = append(events, contracts.PeerMailboxSet{Enabled: true})
		}
		return events, nil

	case contracts.CmdSubthreadSpawn:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.Subthread == nil || cmd.Subthread.ThreadID == "" {
			return nil, fmt.Errorf("%w: subthread id is required", ErrInvalidCommand)
		}
		if s.ParentThreadID != "" {
			return nil, fmt.Errorf("%w: a subthread cannot start subthreads", ErrInvalidCommand)
		}
		if len(s.Subthreads) >= contracts.MaxSubthreads {
			return nil, fmt.Errorf("%w: a thread starts at most %d subthreads", ErrInvalidCommand, contracts.MaxSubthreads)
		}
		// Subthreads report back through the peer mailbox; without it the
		// summary would have nowhere to go, so refuse up front.
		if !s.PeerMailbox {
			return nil, fmt.Errorf("%w: peer mailbox is disabled for this thread", ErrInvalidCommand)
		}
		for _, id := range s.Subthreads {
			if id == cmd.Subthread.ThreadID {
				return nil, fmt.Errorf("%w: subthread %q already exists", ErrInvalidCommand, id)
			}
		}
		return []contracts.EventPayload{*cmd.Subthread}, nil

	case contracts.CmdTaskAdd:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.TaskAdd == nil {
			return nil, fmt.Errorf("%w: task payload is required", ErrInvalidCommand)
		}
		name := strings.TrimSpace(cmd.TaskAdd.Name)
		title := strings.TrimSpace(cmd.TaskAdd.Title)
		if name == "" || title == "" {
			return nil, fmt.Errorf("%w: task name and title are required", ErrInvalidCommand)
		}
		if s.taskIndex(name) >= 0 {
			return nil, fmt.Errorf("%w: task %q already exists", ErrInvalidCommand, name)
		}
		if len(s.Tasks) >= maxTasks {
			return nil, fmt.Errorf("%w: a thread holds at most %d tasks", ErrInvalidCommand, maxTasks)
		}
		return []contracts.EventPayload{contracts.TaskAdded{Name: name, Title: title}}, nil

	case contracts.CmdTaskStatusSet:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.TaskStatus == nil {
			return nil, fmt.Errorf("%w: task status payload is required", ErrInvalidCommand)
		}
		name := strings.TrimSpace(cmd.TaskStatus.Name)
		if s.taskIndex(name) < 0 {
			return nil, fmt.Errorf("%w: no task named %q", ErrInvalidCommand, name)
		}
		if !cmd.TaskStatus.Status.Valid() {
			return nil, fmt.Errorf("%w: unknown task status %q", ErrInvalidCommand, cmd.TaskStatus.Status)
		}
		return []contracts.EventPayload{contracts.TaskStatusChanged{Name: name, Status: cmd.TaskStatus.Status}}, nil

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
		images, err := validateImages(cmd.Message.Images)
		if err != nil {
			return nil, err
		}
		msg := *cmd.Message
		msg.Images = images
		return []contracts.EventPayload{msg}, nil

	case contracts.CmdReviewCommentAdd:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.ReviewComment == nil ||
			strings.TrimSpace(cmd.ReviewComment.File) == "" ||
			strings.TrimSpace(cmd.ReviewComment.Body) == "" {
			return nil, fmt.Errorf("%w: review comment requires file and body", ErrInvalidCommand)
		}
		c := cmd.ReviewComment
		if c.EndLine != 0 && c.EndLine < c.Line {
			return nil, fmt.Errorf("%w: review comment range ends before it starts", ErrInvalidCommand)
		}
		if c.Side != "" && c.Side != "old" && c.Side != "new" {
			return nil, fmt.Errorf("%w: review comment side must be old or new", ErrInvalidCommand)
		}
		return []contracts.EventPayload{*cmd.ReviewComment}, nil

	case contracts.CmdReviewSubmit:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		if cmd.ReviewSubmit == nil || len(cmd.ReviewSubmit.CommentIDs) == 0 || strings.TrimSpace(cmd.Text) == "" {
			return nil, fmt.Errorf("%w: review submit requires comments and text", ErrInvalidCommand)
		}
		pending := make(map[string]bool, len(s.ReviewComments))
		for _, c := range s.ReviewComments {
			if !c.Submitted {
				pending[c.ID] = true
			}
		}
		for _, id := range cmd.ReviewSubmit.CommentIDs {
			if !pending[id] {
				return nil, fmt.Errorf("%w: review comment %q is not pending", ErrInvalidCommand, id)
			}
		}
		images, err := validateImages(cmd.Images)
		if err != nil {
			return nil, err
		}
		return []contracts.EventPayload{
			contracts.MessageSent{MessageID: cmd.ID, Author: "user", Text: cmd.Text, Images: images},
			*cmd.ReviewSubmit,
		}, nil

	case contracts.CmdReviewThreadOpen:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		o := cmd.ReviewOpen
		if o == nil || strings.TrimSpace(o.ID) == "" ||
			strings.TrimSpace(o.File) == "" || strings.TrimSpace(o.Body) == "" {
			return nil, fmt.Errorf("%w: review thread requires id, file and body", ErrInvalidCommand)
		}
		if o.EndLine != 0 && o.EndLine < o.Line {
			return nil, fmt.Errorf("%w: review thread range ends before it starts", ErrInvalidCommand)
		}
		if o.Side != "" && o.Side != "old" && o.Side != "new" {
			return nil, fmt.Errorf("%w: review thread side must be old or new", ErrInvalidCommand)
		}
		if a, ok := s.Agents[o.AuthorID]; !ok || a.Role != contracts.RoleReviewer {
			return nil, fmt.Errorf("%w: only a reviewer opens review threads", ErrInvalidCommand)
		}
		if s.reviewThreadIndex(o.ID) >= 0 {
			return nil, fmt.Errorf("%w: review thread %q already exists", ErrInvalidCommand, o.ID)
		}
		if len(s.ReviewThreads) >= maxReviewThreads {
			return nil, fmt.Errorf("%w: a thread holds at most %d review threads", ErrInvalidCommand, maxReviewThreads)
		}
		return []contracts.EventPayload{*o}, nil

	case contracts.CmdReviewThreadReply:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		r := cmd.ReviewReply
		if r == nil || strings.TrimSpace(r.Body) == "" {
			return nil, fmt.Errorf("%w: review reply requires a thread and body", ErrInvalidCommand)
		}
		if _, ok := s.Agents[r.AuthorID]; !ok {
			return nil, fmt.Errorf("%w: only an agent replies on a review thread", ErrInvalidCommand)
		}
		i := s.reviewThreadIndex(r.ThreadID)
		if i < 0 {
			return nil, fmt.Errorf("%w: no review thread %q", ErrInvalidCommand, r.ThreadID)
		}
		if len(s.ReviewThreads[i].Replies) >= maxReviewReplies {
			return nil, fmt.Errorf("%w: a review thread holds at most %d replies", ErrInvalidCommand, maxReviewReplies)
		}
		return []contracts.EventPayload{*r}, nil

	case contracts.CmdReviewThreadStatus:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		st := cmd.ReviewStatus
		if st == nil {
			return nil, fmt.Errorf("%w: review thread status is required", ErrInvalidCommand)
		}
		i := s.reviewThreadIndex(st.ThreadID)
		if i < 0 {
			return nil, fmt.Errorf("%w: no review thread %q", ErrInvalidCommand, st.ThreadID)
		}
		if st.Status != contracts.ReviewStatusOpen && st.Status != contracts.ReviewStatusResolved {
			return nil, fmt.Errorf("%w: unknown review status %q", ErrInvalidCommand, st.Status)
		}
		thread := s.ReviewThreads[i]
		if st.AuthorID != thread.AuthorID {
			return nil, fmt.Errorf("%w: only the reviewer who opened the thread changes its status", ErrInvalidCommand)
		}
		// A non-empty status note is recorded as a reply, so it needs room too.
		if strings.TrimSpace(st.Body) != "" && len(thread.Replies) >= maxReviewReplies {
			return nil, fmt.Errorf("%w: a review thread holds at most %d replies", ErrInvalidCommand, maxReviewReplies)
		}
		return []contracts.EventPayload{*st}, nil

	case contracts.CmdQuestionAsk:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		q := cmd.QuestionAsk
		if q == nil {
			return nil, fmt.Errorf("%w: question payload is required", ErrInvalidCommand)
		}
		text := strings.TrimSpace(q.Question)
		if text == "" || len(text) > contracts.MaxQuestionLength {
			return nil, fmt.Errorf("%w: question must be 1 to %d characters", ErrInvalidCommand, contracts.MaxQuestionLength)
		}
		if !q.Kind.Valid() {
			return nil, fmt.Errorf("%w: unknown question kind %q", ErrInvalidCommand, q.Kind)
		}
		if q.QuestionID == "" {
			return nil, fmt.Errorf("%w: question id is required", ErrInvalidCommand)
		}
		if s.question(q.QuestionID) != nil {
			return nil, fmt.Errorf("%w: question %q already exists", ErrInvalidCommand, q.QuestionID)
		}
		options, err := normalizeOptions(q.Kind, q.Options)
		if err != nil {
			return nil, err
		}
		if s.pendingQuestions() >= contracts.MaxQuestionPending {
			return nil, fmt.Errorf("%w: a thread holds at most %d unanswered questions", ErrInvalidCommand, contracts.MaxQuestionPending)
		}
		return []contracts.EventPayload{contracts.QuestionAsked{
			QuestionID: q.QuestionID,
			AgentID:    q.AgentID,
			Kind:       q.Kind,
			Question:   text,
			Options:    options,
			Notify:     strings.TrimSpace(q.Notify),
		}}, nil

	case contracts.CmdQuestionClose:
		if !s.Exists {
			return nil, ErrThreadNotFound
		}
		c := cmd.QuestionClose
		if c == nil {
			return nil, fmt.Errorf("%w: question close payload is required", ErrInvalidCommand)
		}
		q := s.question(c.QuestionID)
		if q == nil {
			return nil, fmt.Errorf("%w: no question %q", ErrInvalidCommand, c.QuestionID)
		}
		// Only one close can win: an answer, a dismissal and a timeout racing
		// for the same question resolve to the first one recorded.
		if q.Status != QuestionPending {
			return nil, fmt.Errorf("%w: question %q is already %s", ErrInvalidCommand, c.QuestionID, q.Status)
		}
		if !c.Answered {
			reason := strings.TrimSpace(c.Reason)
			if reason == "" {
				return nil, fmt.Errorf("%w: a question closed without an answer needs a reason", ErrInvalidCommand)
			}
			return []contracts.EventPayload{contracts.QuestionClosed{QuestionID: c.QuestionID, Reason: reason}}, nil
		}
		selected, text, err := validateAnswer(q.QuestionAsked, c.Selected, c.Text)
		if err != nil {
			return nil, err
		}
		return []contracts.EventPayload{contracts.QuestionClosed{
			QuestionID: c.QuestionID, Answered: true, Selected: selected, Text: text,
		}}, nil

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

// validateImages checks a message's image refs against the shared caps and
// id/mime shapes. The store already validated them at upload; this is the
// decisive gate before a turn records them, so a replayed or foreign command
// cannot smuggle more than the caps allow.
func validateImages(images []contracts.ImageRef) ([]contracts.ImageRef, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > contracts.MaxTurnImages {
		return nil, fmt.Errorf("%w: a turn carries at most %d images", ErrInvalidCommand, contracts.MaxTurnImages)
	}
	var total int64
	for _, img := range images {
		if !contracts.ValidImageID(img.ID) {
			return nil, fmt.Errorf("%w: image id %q is not an attachment id", ErrInvalidCommand, img.ID)
		}
		if !contracts.ValidImageMime(img.Mime) {
			return nil, fmt.Errorf("%w: image %q has unsupported type %q", ErrInvalidCommand, img.ID, img.Mime)
		}
		if img.Size <= 0 || img.Size > contracts.MaxImageBytes {
			return nil, fmt.Errorf("%w: image %q exceeds the %d MiB per-image limit", ErrInvalidCommand, img.ID, contracts.MaxImageBytes>>20)
		}
		total += img.Size
	}
	if total > contracts.MaxTurnImageBytes {
		return nil, fmt.Errorf("%w: images exceed the %d MiB per-turn limit", ErrInvalidCommand, contracts.MaxTurnImageBytes>>20)
	}
	return images, nil
}
