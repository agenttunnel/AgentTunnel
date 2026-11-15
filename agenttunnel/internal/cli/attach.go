package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func init() {
	Register(Command{
		Name:    "attach",
		Summary: "Watch a thread live and steer it from the terminal",
		Usage:   "agenttunnel attach <thread>",
		Run:     runAttach,
	})
}

// attachAction is what a line typed at the attach prompt asks for.
type attachAction int

const (
	attachSteer attachAction = iota
	attachStop
	attachDetach
	attachHelp
	attachAnswer
)

// parsedAttachLine is one typed line, classified.
type parsedAttachLine struct {
	action attachAction
	text   string
}

// parseAttachCommand classifies one stdin line. Anything that is not a slash
// command is steering text, including a line that starts with "/" but names no
// known command — a URL fragment should not be swallowed as a typo.
func parseAttachCommand(line string) parsedAttachLine {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/stop":
		return parsedAttachLine{action: attachStop}
	case "/detach":
		return parsedAttachLine{action: attachDetach}
	case "/help":
		return parsedAttachLine{action: attachHelp}
	}
	return parsedAttachLine{action: attachSteer, text: trimmed}
}

// attachHelpText lists the interactive commands.
const attachHelpText = `commands:
  <text>      steer the agents with this message
  /stop       stop the running turn
  /detach     leave without stopping (also Ctrl-C)
  /help       show this list`

// questionPromptEntry is a pending question shown at the prompt.
type questionPromptEntry struct {
	id       string
	kind     contracts.QuestionKind
	question string
	options  []string
}

func runAttach(ctx context.Context, env *Env, args []string) error {
	var ref string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "-"):
			return usageError("unknown flag %q (usage: agenttunnel attach <thread>)", arg)
		case ref != "":
			return usageError("agenttunnel attach takes one thread reference")
		default:
			ref = arg
		}
	}
	if ref == "" {
		return usageError("agenttunnel attach requires a thread id, id prefix, or title")
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	row, err := resolveThreadRow(ctx, c, ref)
	if err != nil {
		return err
	}

	ctx, stop := withInterrupt(ctx)
	defer stop()
	return attachSession(ctx, env, c, row, os.Stdin)
}

// attachSession is the interactive loop: header, backlog, then live events and
// typed input side by side. It returns when the user detaches, when the turn
// it watches ends, or when the daemon goes away.
func attachSession(ctx context.Context, env *Env, c *Client, row threadListRow, in io.Reader) error {
	if err := printAttachHeader(ctx, env, c, row); err != nil {
		return detachOnCancel(ctx, env, err)
	}
	if err := subscribeThread(ctx, c, row.ID); err != nil {
		return detachOnCancel(ctx, env, err)
	}
	backlog, err := fetchEntries(ctx, c, row.ID, "")
	if err != nil {
		return detachOnCancel(ctx, env, err)
	}
	early := bufferedFrames(c, row.ID, backlog)
	if len(backlog) > logsDefaultLines {
		backlog = backlog[len(backlog)-logsDefaultLines:]
	}
	for _, entry := range backlog {
		writeLogEntry(env.Out, entry)
	}
	fmt.Fprintln(env.Out, "\n-- attached; /help for commands, /detach or Ctrl-C to leave --")

	// requireActivity: an attach to a thread whose turn already finished waits
	// for the next turn instead of leaving at once.
	statuses, err := fetchAgentStatuses(ctx, c, row.ID)
	if err != nil {
		statuses = map[string]contracts.AgentStatus{}
	}
	tracker := newTurnTracker(statuses, true)
	var pending []questionPromptEntry
	handle := func(ev Event) bool {
		asked, answeredID := handleAttachEvent(env, ev, row.ID, tracker)
		if asked != nil {
			pending = append(pending, *asked)
			printQuestion(env, *asked)
		}
		if answeredID != "" {
			pending = dropQuestion(pending, answeredID)
		}
		return tracker.ended()
	}
	for _, ev := range early {
		if handle(ev) {
			return turnFinished(env)
		}
	}

	lines := readStdinLines(ctx, in)
	poll := time.NewTicker(followPollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return detachOnCancel(ctx, env, nil)

		case line, ok := <-lines:
			if !ok {
				// Stdin closed (a pipe or a file): keep watching, just take
				// no more input.
				lines = nil
				continue
			}
			done, next, err := handleAttachLine(ctx, env, c, row, line, pending)
			if err != nil {
				// A line cut short by Ctrl-C is a detach, not an error.
				if ctx.Err() == nil {
					fmt.Fprintln(env.Err, "agenttunnel:", err)
				}
				continue
			}
			pending = next
			if done {
				return nil
			}

		case <-poll.C:
			// Status events are the fast path; a poll catches a turn that ended
			// without an event this session saw.
			if fresh, err := fetchAgentStatuses(ctx, c, row.ID); err == nil {
				tracker.update(fresh)
			}
			if tracker.ended() {
				return turnFinished(env)
			}

		case ev, ok := <-c.Events():
			if !ok {
				if err := c.Err(); err != nil && ctx.Err() == nil {
					return fmt.Errorf("lost the connection to the daemon: %w", err)
				}
				return nil
			}
			if handle(ev) {
				return turnFinished(env)
			}
		}
	}
}

// detachOnCancel turns a setup failure caused by Ctrl-C into the same clean
// detach the live loop gives.
func detachOnCancel(ctx context.Context, env *Env, err error) error {
	if ctx.Err() != nil {
		fmt.Fprintln(env.Out, "\ndetached (the turn keeps running)")
		return nil
	}
	return err
}

// turnFinished is the normal end of an attach: the turn it watched is over.
func turnFinished(env *Env) error {
	fmt.Fprintln(env.Out, "-- turn finished --")
	return nil
}

// handleAttachLine executes one typed line. done means the user asked to leave.
func handleAttachLine(ctx context.Context, env *Env, c cliCaller, row threadListRow, line string, pending []questionPromptEntry) (bool, []questionPromptEntry, error) {
	parsed := parseAttachCommand(line)
	if parsed.action == attachSteer && parsed.text == "" {
		return false, pending, nil
	}

	// An open question owns the next line: an answer is more urgent than a
	// steer, and the agent asking is blocked until it arrives.
	if parsed.action == attachSteer && len(pending) > 0 {
		q := pending[0]
		if err := answerQuestion(ctx, c, row.ID, q, parsed.text); err != nil {
			return false, pending, err
		}
		return false, pending[1:], nil
	}

	switch parsed.action {
	case attachStop:
		var res struct {
			Stopped bool `json:"stopped"`
		}
		if err := c.Call(ctx, "thread.turn.stop", map[string]string{"threadId": row.ID}, &res); err != nil {
			return false, pending, err
		}
		if res.Stopped {
			fmt.Fprintln(env.Out, "stop requested")
		} else {
			fmt.Fprintln(env.Out, "no turn is running")
		}
	case attachDetach:
		fmt.Fprintln(env.Out, "detached (the turn keeps running)")
		return true, pending, nil
	case attachHelp:
		fmt.Fprintln(env.Out, attachHelpText)
	case attachSteer:
		if err := c.Call(ctx, "thread.steer", map[string]string{"threadId": row.ID, "text": parsed.text}, nil); err != nil {
			return false, pending, err
		}
		fmt.Fprintln(env.Out, "steering sent")
	}
	return false, pending, nil
}

// answerQuestion turns a typed line into question.answer: a number picks an
// option (comma-separated numbers for a multi question), anything else is free
// text. An out-of-range number is answered as text, so a user typing "42" on a
// two-option question is not silently ignored.
func answerQuestion(ctx context.Context, c cliCaller, threadID string, q questionPromptEntry, line string) error {
	selected, ok := selectOptions(q, line)
	params := map[string]any{"threadId": threadID, "questionId": q.id}
	if ok {
		params["selected"] = selected
	} else {
		params["text"] = line
	}
	if err := c.Call(ctx, "question.answer", params, nil); err != nil {
		return err
	}
	return nil
}

// selectOptions maps "2" (or "1,3") to the option labels when every number is
// in range. Multi takes a list; choice and text take a single number.
func selectOptions(q questionPromptEntry, line string) ([]string, bool) {
	// Only a choice or multi question has selectable options; a text question
	// takes free text even if an options list rode along.
	if len(q.options) == 0 || (q.kind != contracts.QuestionChoice && q.kind != contracts.QuestionMulti) {
		return nil, false
	}
	parts := strings.Split(line, ",")
	if q.kind != contracts.QuestionMulti && len(parts) != 1 {
		return nil, false
	}
	var out []string
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > len(q.options) {
			return nil, false
		}
		out = append(out, q.options[n-1])
	}
	return out, len(out) > 0
}

// handleAttachEvent prints one live frame and returns a question that just
// opened plus the id of one that closed.
func handleAttachEvent(env *Env, ev Event, threadID string, tracker *turnTracker) (*questionPromptEntry, string) {
	frame, ok := decodeEventFrame(ev)
	if !ok || frame.ThreadID != threadID {
		return nil, ""
	}
	switch contracts.EventType(frame.Type) {
	case contracts.EvtAgentOutput:
		var out contracts.AgentOutput
		if json.Unmarshal(frame.Payload, &out) != nil {
			return nil, ""
		}
		tracker.activity = true
		writeLogEntry(env.Out, contracts.LogEntry{AgentID: out.AgentID, Kind: out.Kind, Text: out.Text, At: time.Now()})
	case contracts.EvtAgentStatusChanged:
		var status contracts.AgentStatusChanged
		if json.Unmarshal(frame.Payload, &status) != nil {
			return nil, ""
		}
		tracker.observe(string(status.AgentID), status.Status)
		writeStatusLine(env.Out, status)
	case contracts.EvtTaskDispatched:
		var dispatch contracts.TaskDispatched
		if json.Unmarshal(frame.Payload, &dispatch) != nil {
			return nil, ""
		}
		tracker.note(string(dispatch.AgentID), contracts.AgentPending)
		fmt.Fprintf(env.Out, "%s dispatched %s (%s)\n", dispatch.AgentID, dispatch.Role, truncateText(dispatch.Task, 80))
	case contracts.EvtQuestionAsked:
		var q contracts.QuestionAsked
		if json.Unmarshal(frame.Payload, &q) != nil {
			return nil, ""
		}
		return &questionPromptEntry{id: q.QuestionID, kind: q.Kind, question: q.Question, options: q.Options}, ""
	case contracts.EvtQuestionClosed:
		var closed contracts.QuestionClosed
		if json.Unmarshal(frame.Payload, &closed) != nil {
			return nil, ""
		}
		return nil, closed.QuestionID
	}
	return nil, ""
}

// printQuestion renders an agent's question and its numbered options.
func printQuestion(env *Env, q questionPromptEntry) {
	hint := ""
	switch q.kind {
	case contracts.QuestionChoice:
		hint = " (answer with a number)"
	case contracts.QuestionMulti:
		hint = " (answer with numbers separated by commas)"
	case contracts.QuestionText:
		hint = ""
	}
	fmt.Fprintf(env.Out, "\n? %s%s\n", questionText(q), hint)
	for i, option := range q.options {
		fmt.Fprintf(env.Out, "  %d) %s\n", i+1, option)
	}
	fmt.Fprintln(env.Out, "  or type a free-text answer")
}

// questionText is the question itself, with a placeholder when an agent asked
// nothing but options.
func questionText(q questionPromptEntry) string {
	text := strings.TrimSpace(q.question)
	if text == "" {
		return "question"
	}
	return text
}

func dropQuestion(pending []questionPromptEntry, id string) []questionPromptEntry {
	out := pending[:0]
	for _, q := range pending {
		if q.id != id {
			out = append(out, q)
		}
	}
	return out
}

// printAttachHeader shows what the user attached to before the log stream.
func printAttachHeader(ctx context.Context, env *Env, c cliCaller, row threadListRow) error {
	project := usageDash(row.ProjectID)
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err == nil {
		if p, ok := projectByID(projects, row.ProjectID); ok {
			project = projectLabel(p)
		}
	}
	title := row.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(env.Out, "thread  %s  %q\n", row.ID, title)
	if row.ParentThreadID != "" {
		fmt.Fprintf(env.Out, "parent  %s\n", shortThreadID(row.ParentThreadID))
	}
	fmt.Fprintf(env.Out, "project %s\n", project)
	fmt.Fprintf(env.Out, "status  %s  (%d/%d agents)\n", threadRowStatus(row), row.RunningAgents, row.Agents)
	return nil
}

// readStdinLines streams stdin lines. The channel closes at EOF; the context
// releases the reader goroutine when the caller leaves.
func readStdinLines(ctx context.Context, in io.Reader) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		if in == nil {
			return
		}
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case out <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func truncateText(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
