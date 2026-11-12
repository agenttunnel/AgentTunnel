package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func init() {
	Register(Command{
		Name:    "logs",
		Summary: "Print a thread's agent output, optionally following it",
		Usage:   "agenttunnel logs <thread> [--follow] [--agent <id>] [--lines N]",
		Run:     runLogs,
	})
}

const (
	logsDefaultLines = 200
	logsMaxLines     = 5000
	// followPollInterval refreshes agent statuses while following. The turn end
	// itself is event-driven; this only catches a roster that went terminal
	// without a status event we saw.
	followPollInterval = 2 * time.Second
	// followStartGrace bounds how long a follower waits for a turn to appear
	// before giving up. A detached turn that fails before it records an agent
	// (a dead harness, a refused worktree) writes an error line and no agent;
	// without this the follower would wait forever and look like a hang.
	followStartGrace = 15 * time.Second
)

// logsOptions is the parsed `agenttunnel logs` invocation.
type logsOptions struct {
	ref    string
	follow bool
	agent  string
	lines  int
}

func parseLogsArgs(args []string) (logsOptions, error) {
	opts := logsOptions{lines: logsDefaultLines}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--follow" || arg == "-f":
			opts.follow = true
		case arg == "--agent" || strings.HasPrefix(arg, "--agent="):
			value, next, err := usageFlagValue(args, i, "--agent")
			if err != nil {
				return opts, err
			}
			opts.agent, i = strings.TrimSpace(value), next
		case arg == "--lines" || arg == "-n" || strings.HasPrefix(arg, "--lines="):
			name := "--lines"
			if arg == "-n" {
				name = "-n"
			}
			value, next, err := usageFlagValue(args, i, name)
			if err != nil {
				return opts, err
			}
			n, convErr := strconv.Atoi(strings.TrimSpace(value))
			if convErr != nil || n < 0 {
				return opts, usageError("--lines wants a non-negative number, got %q", value)
			}
			if n > logsMaxLines {
				return opts, usageError("--lines is capped at %d", logsMaxLines)
			}
			opts.lines, i = n, next
		case strings.HasPrefix(arg, "-"):
			return opts, usageError("unknown flag %q (usage: agenttunnel logs <thread> [--follow] [--agent <id>] [--lines N])", arg)
		case opts.ref != "":
			return opts, usageError("agenttunnel logs takes one thread reference")
		default:
			opts.ref = arg
		}
	}
	if opts.ref == "" {
		return opts, usageError("agenttunnel logs requires a thread id, id prefix, or title")
	}
	return opts, nil
}

func runLogs(ctx context.Context, env *Env, args []string) error {
	opts, err := parseLogsArgs(args)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()

	row, err := resolveThreadRow(ctx, c, opts.ref)
	if err != nil {
		return err
	}
	if opts.follow && !env.JSON {
		cancelled, stop := withInterrupt(ctx)
		defer stop()
		ctx = cancelled
	}
	return streamLogs(ctx, env, c, row, followOptions{
		lines: opts.lines, follow: opts.follow, agent: opts.agent,
	})
}

// followOptions configures streamLogs. It is shared by `logs --follow`,
// `attach`, and `run --follow`.
type followOptions struct {
	lines int
	// follow keeps the stream open after the backlog until the turn ends.
	follow bool
	// agent narrows the stream to one agent id.
	agent string
	// requireActivity makes the end check wait for the turn to visibly start,
	// so `run --follow` cannot mistake a previous turn's terminal roster for
	// this turn finishing.
	requireActivity bool
	// onEvent, when set, observes every decoded event (attach uses it for
	// questions). It must not block.
	onEvent func(event any)
}

// streamLogs prints the tail, then (when asked) follows live output until the
// turn ends or the context is cancelled. The subscription is opened before the
// backlog is read so no line is missed between the two; the deduper drops any
// live frame the backlog already carried.
func streamLogs(ctx context.Context, env *Env, c *Client, row threadListRow, opts followOptions) error {
	// A cancelled context anywhere in the setup is Ctrl-C, which must exit
	// cleanly rather than print an error.
	if err := subscribeThread(ctx, c, row.ID); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	backlog, err := fetchBacklog(ctx, c, row, opts)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if env.JSON {
		return env.PrintJSON(backlog)
	}
	for _, entry := range backlog {
		writeLogEntry(env.Out, entry)
	}
	if !opts.follow {
		return nil
	}
	return followOutput(ctx, env, c, row, backlog, opts)
}

// fetchBacklog reads the lines preceding the live stream. The daemon caps
// thread.logs at its own tail; --lines trims further here.
func fetchBacklog(ctx context.Context, c cliCaller, row threadListRow, opts followOptions) ([]contracts.LogEntry, error) {
	entries, err := fetchEntries(ctx, c, row.ID, opts.agent)
	if err != nil {
		return nil, err
	}
	if opts.lines >= 0 && len(entries) > opts.lines {
		entries = entries[len(entries)-opts.lines:]
	}
	return entries, nil
}

// fetchEntries reads a thread's (or one agent's) output history.
func fetchEntries(ctx context.Context, c cliCaller, threadID, agent string) ([]contracts.LogEntry, error) {
	if agent != "" {
		var entries []contracts.LogEntry
		params := map[string]string{"threadId": threadID, "agentId": agent}
		if err := c.Call(ctx, "agents.logs", params, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}
	var entries []contracts.LogEntry
	if err := c.Call(ctx, "thread.logs", map[string]string{"threadId": threadID}, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// subscribeThread asks the daemon to stream this thread's events to us. Failures
// are not fatal: without a subscription a follow still polls statuses, it just
// prints no live lines.
func subscribeThread(ctx context.Context, c cliCaller, threadID string) error {
	return c.Call(ctx, "thread.subscribe", map[string]string{"threadId": threadID}, nil)
}

// followedEvent is the subset of a thread.event frame a tail consumes. The
// payload is decoded per type once the frame is known to be ours.
type followedEvent struct {
	ID       string          `json:"id"`
	ThreadID string          `json:"threadId"`
	Type     string          `json:"type"`
	At       time.Time       `json:"at"`
	Seq      int64           `json:"seq"`
	Payload  json.RawMessage `json:"payload"`
}

func decodeEventFrame(ev Event) (followedEvent, bool) {
	if ev.Method != "thread.event" {
		return followedEvent{}, false
	}
	var frame followedEvent
	if err := json.Unmarshal(ev.Params, &frame); err != nil {
		return followedEvent{}, false
	}
	return frame, frame.Type != ""
}

// followOutput is the live half of a tail: it prints agent output and status
// lines for one thread until every agent is terminal.
func followOutput(ctx context.Context, env *Env, c *Client, row threadListRow, backlog []contracts.LogEntry, opts followOptions) error {
	statuses, err := fetchAgentStatuses(ctx, c, row.ID)
	if err != nil {
		// A following user still wants the backlog; a missing roster only
		// disables the event-driven end check.
		statuses = map[string]contracts.AgentStatus{}
	}
	tracker := newTurnTracker(statuses, opts.requireActivity)
	dedupe := newLogDeduper(backlog)
	started := time.Now()

	poll := time.NewTicker(followPollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if fresh, err := fetchAgentStatuses(ctx, c, row.ID); err == nil {
				tracker.update(fresh)
			}
			if !tracker.started() && time.Since(started) >= followStartGrace {
				fmt.Fprintln(env.Out, "no turn started; see `agenttunnel logs` for errors")
				return nil
			}
			if tracker.ended() {
				return nil
			}
		case ev, ok := <-c.Events():
			if !ok {
				return nil
			}
			printFollowEvent(env, ev, row.ID, opts, tracker, dedupe)
			if tracker.ended() {
				return nil
			}
		}
	}
}

// printFollowEvent renders one frame if it belongs to this tail.
func printFollowEvent(env *Env, ev Event, threadID string, opts followOptions, tracker *turnTracker, dedupe *logDeduper) {
	frame, ok := decodeEventFrame(ev)
	if !ok || frame.ThreadID != threadID {
		return
	}
	switch contracts.EventType(frame.Type) {
	case contracts.EvtAgentOutput:
		var out contracts.AgentOutput
		if json.Unmarshal(frame.Payload, &out) != nil {
			return
		}
		if opts.agent != "" && string(out.AgentID) != opts.agent {
			return
		}
		if dedupe.duplicate(string(out.AgentID), out.Kind, out.Text) {
			return
		}
		tracker.activity = true
		writeLogEntry(env.Out, contracts.LogEntry{AgentID: out.AgentID, Kind: out.Kind, Text: out.Text, At: time.Now()})

	case contracts.EvtAgentStatusChanged:
		var status contracts.AgentStatusChanged
		if json.Unmarshal(frame.Payload, &status) != nil {
			return
		}
		if opts.agent != "" && string(status.AgentID) != opts.agent {
			return
		}
		tracker.observe(string(status.AgentID), status.Status)
		writeStatusLine(env.Out, status)

	case contracts.EvtTaskDispatched:
		var dispatch contracts.TaskDispatched
		if json.Unmarshal(frame.Payload, &dispatch) != nil {
			return
		}
		tracker.note(string(dispatch.AgentID), contracts.AgentPending)
	}

	if opts.onEvent != nil {
		opts.onEvent(frame)
	}
}
