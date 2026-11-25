package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiThreadCommand is one thread operation. The palette lists it by title and
// hint, and the composer runs it as /name. run gets the text after the name
// ("" from the palette) and opens a prompt or picker for whatever is missing.
type tuiThreadCommand struct {
	name   string
	args   string
	title  string
	onList bool // runs on the thread list rather than an open thread
	run    func(m *tuiModel, arg string)
}

// tuiThreadCommands is the command table. It is a function rather than a
// variable because several commands read the table themselves (help).
func tuiThreadCommands() []tuiThreadCommand {
	return []tuiThreadCommand{
		{name: "new", title: "Threads: new thread", onList: true, run: tuiNewThread},
		{name: "new-solo", title: "Threads: new solo thread", onList: true, run: tuiNewSoloThread},
		{name: "new-project", title: "Projects: new project", onList: true, run: func(m *tuiModel, _ string) {
			m.openNewProject(nil)
		}},
		{name: "stop", title: "Thread: stop turn", run: tuiStopTurn},
		{name: "btw", args: "<question>", title: "Thread: side question (not recorded)", run: tuiBtw},
		{name: "shell", args: "<command>", title: "Thread: run a shell command", run: tuiShell},
		{name: "council", args: "<topic>", title: "Thread: start a council", run: tuiCouncil},
		{name: "rename", args: "<title>", title: "Thread: rename", run: tuiRename},
		{name: "fork", title: "Thread: fork from a message", run: tuiFork},
		{name: "archive", title: "Thread: archive or unarchive", run: tuiArchive},
		{name: "delete", title: "Thread: delete", run: tuiDelete},
		{name: "auto", title: "Thread: toggle auto mode", run: tuiAutoMode},
		{name: "peer", title: "Thread: toggle peer mailbox", run: tuiPeerMailbox},
		{name: "solo", args: "[off|<harness> [model]]", title: "Thread: toggle solo mode", run: tuiSolo},
		{name: "solo-runtime", title: "Thread: choose the solo harness and model", run: tuiSoloPick},
		{name: "lead", title: "Thread: set lead runtime", run: tuiLead},
		{name: "role", title: "Thread: set a role's runtime", run: tuiRole},
		{name: "schedule", title: "Thread: schedule after other threads", run: tuiSchedule},
		{name: "unschedule", title: "Thread: cancel the schedule", run: tuiUnschedule},
		{name: "copy", title: "Thread: copy the last reply", run: tuiCopyReply},
		{name: "edit", title: "Thread: write the prompt in $EDITOR", run: func(m *tuiModel, _ string) {
			m.edit = m.screen == tuiScreenThread
		}},
		{name: "mail", title: "Thread: show peer mailbox", run: tuiShowMailbox},
		{name: "tasks", title: "Thread: show task list", run: tuiShowTasks},
		{name: "agent", title: "Thread: agent logs", run: tuiAgentLogs},
		{name: "dismiss", title: "Thread: dismiss the open question", run: tuiDismissQuestion},
		{name: "read", title: "Thread: mark read", run: tuiMarkRead},
		{name: "help", title: "Thread: show commands", run: func(m *tuiModel, _ string) {
			m.appendLogLines(tuiHelpLines()...)
		}},
		{name: "detach", title: "Thread: leave the thread (keeps the turn)", run: func(m *tuiModel, _ string) {
			m.closeThread()
		}},
	}
}

// tuiSlashCommand resolves a composer line to a thread command. Only known
// names count: an unknown "/word" stays steering text, as attach treats it.
func tuiSlashCommand(line string) (func(m *tuiModel, arg string), string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return nil, "", false
	}
	name, arg, _ := strings.Cut(trimmed[1:], " ")
	for _, cmd := range tuiThreadCommands() {
		if cmd.name == name && !cmd.onList {
			return cmd.run, strings.TrimSpace(arg), true
		}
	}
	return nil, "", false
}

// slashMatches lists the composer commands the typed "/prefix" could become.
// It is empty once a space starts the argument, while a question owns the
// prompt, and for text that is not a slash command.
func (m *tuiModel) slashMatches() []tuiThreadCommand {
	line := m.input.string()
	if m.pending != nil || !strings.HasPrefix(line, "/") || strings.Contains(line, " ") {
		return nil
	}
	var out []tuiThreadCommand
	for _, cmd := range tuiThreadCommands() {
		if !cmd.onList && strings.HasPrefix(cmd.name, line[1:]) {
			out = append(out, cmd)
		}
	}
	return out
}

// completeSlash fills in the first matching command, plus a space when it
// takes an argument.
func (m *tuiModel) completeSlash() {
	matches := m.slashMatches()
	if len(matches) == 0 {
		return
	}
	sel := matches[m.slashSel%len(matches)]
	line := "/" + sel.name
	if sel.args != "" {
		line += " "
	}
	m.input.set(line)
	m.slashSel = 0
}

// moveSlash shifts the highlighted hint by d, wrapping, and reports whether
// hints were showing to take the key.
func (m *tuiModel) moveSlash(d int) bool {
	n := len(m.slashMatches())
	if n == 0 {
		return false
	}
	m.slashSel = ((m.slashSel+d)%n + n) % n
	return true
}

// slashRows is the hint list above the prompt: one row per matching command
// with its usage and description, the highlighted one marked as the tab target.
func (m *tuiModel) slashRows(width int) []tuiRow {
	var rows []tuiRow
	for i, cmd := range m.slashMatches() {
		usage := "/" + cmd.name
		if cmd.args != "" {
			usage += " " + cmd.args
		}
		nameInk, mark := tuiInk{fg: tuiColText}, "  "
		if i == m.slashSel {
			nameInk, mark = tuiInk{fg: tuiColAccent, bold: true}, "▸ "
		}
		rows = append(rows, tuiText(mark, tuiInk{fg: tuiColAccent}).join(
			tuiText(fmt.Sprintf("%-26s", usage), nameInk),
			tuiText(strings.TrimPrefix(cmd.title, "Thread: "), tuiInk{fg: tuiColFaint}),
		).fit(width))
	}
	if len(rows) > 0 {
		rows = append(rows, tuiText("tab complete", tuiInk{fg: tuiColFaint}))
	}
	return rows
}

// tuiHelpLines is the composer's command list, built from the table.
func tuiHelpLines() []string {
	lines := []string{"commands:", "  <text>  steer the agents with this message"}
	for _, cmd := range tuiThreadCommands() {
		if cmd.onList {
			continue
		}
		usage := "/" + cmd.name
		if cmd.args != "" {
			usage += " " + cmd.args
		}
		lines = append(lines, fmt.Sprintf("  %-26s %s", usage, strings.TrimPrefix(cmd.title, "Thread: ")))
	}
	return lines
}

// tuiPaletteActions turns the command table into palette entries. Each shows
// only on the screen its command runs on.
func tuiPaletteActions() []tuiAction {
	var actions []tuiAction
	for _, cmd := range tuiThreadCommands() {
		actions = append(actions, tuiAction{
			Title: cmd.title,
			Hint:  "/" + cmd.name,
			Show: func(m *tuiModel) bool {
				return m.view == tuiThreadsViewID && (m.screen == tuiScreenThread) != cmd.onList
			},
			Run: func(m *tuiModel) { cmd.run(m, "") },
		})
	}
	return actions
}

// notify reports a result the user acts on now: the thread's log when a
// thread is open, the list's flash otherwise.
func (m *tuiModel) notify(text string) {
	if m.screen == tuiScreenThread {
		m.appendLogLines(text)
		return
	}
	m.flash = text
}

// onThread reports whether id is the thread on screen.
func (m *tuiModel) onThread(id string) bool {
	return m.screen == tuiScreenThread && m.thread.ID == id
}

// tuiThreadArg runs then with arg when the command was given one, else opens a
// prompt for it.
func tuiThreadArg(m *tuiModel, arg, title string, then func(m *tuiModel, value string)) {
	if arg != "" {
		then(m, arg)
		return
	}
	m.openPrompt(title, "", then)
}

// resolveThreadRefs maps comma-separated thread ids or unique id prefixes from
// the list to ids. A blank input means no dependencies.
func (m *tuiModel) resolveThreadRefs(refs string) ([]string, error) {
	out := []string{}
	for _, ref := range strings.Split(refs, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		var matches []string
		for _, row := range m.rows {
			if strings.HasPrefix(row.thread.ID, ref) {
				matches = append(matches, row.thread.ID)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("%q matches %d threads", ref, len(matches))
		}
		out = append(out, matches[0])
	}
	return out, nil
}

// --- Thread state, catalog, and job builders ---

// tuiThreadState is the part of thread.get the thread menus read.
type tuiThreadState struct {
	Title       string `json:"title"`
	Archived    bool   `json:"archived"`
	AutoMode    bool   `json:"autoMode"`
	PeerMailbox bool   `json:"peerMailbox"`
	Leads       int    `json:"leads"`
	// SoloRuntime is set while the thread runs one agent with no team.
	SoloRuntime *contracts.LeadRuntime `json:"soloRuntime,omitempty"`
	Messages    []tuiMessageRow        `json:"messages"`
	Mailbox     []tuiMailboxRow        `json:"mailbox"`
	Tasks       []tuiTaskRow           `json:"tasks"`
}

type tuiMessageRow struct {
	ID     string    `json:"id"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

type tuiMailboxRow struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	Kind string    `json:"kind"`
	Body string    `json:"body"`
	At   time.Time `json:"at"`
}

type tuiTaskRow struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// tuiCatalog is harness.list: the enabled harnesses and each one's models. The
// session fetches it once, when a runtime menu first needs it.
type tuiCatalog struct {
	Enabled []contracts.Harness `json:"enabled"`
	Models  map[string][]string `json:"models"`
}

func fetchTuiThreadState(ctx context.Context, c cliCaller, threadID string) (tuiThreadState, error) {
	var st tuiThreadState
	err := c.Call(ctx, "thread.get", map[string]string{"threadId": threadID}, &st)
	return st, err
}

// tuiStateJob loads the thread's state and runs then on the loop, if the thread
// is still open there when the state arrives.
func tuiStateJob(threadID string, then func(m *tuiModel, st tuiThreadState)) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		st, err := fetchTuiThreadState(ctx, c, threadID)
		if err != nil {
			return tuiNote(threadID, "thread.get: "+err.Error())
		}
		return func(m *tuiModel) {
			if m.onThread(threadID) {
				then(m, st)
			}
		}
	}
}

// tuiCallJob runs one RPC and reports ok on the thread. An empty ok means the
// call is silent on success, so only failures show.
func tuiCallJob(threadID, method string, params any, ok string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if err := c.Call(ctx, method, params, nil); err != nil {
			return tuiNote(threadID, method+": "+err.Error())
		}
		if ok == "" {
			return nil
		}
		return tuiNote(threadID, ok)
	}
}

// tuiCatalogJob fetches the harness catalog once and then runs then.
func tuiCatalogJob(then func(*tuiModel)) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var cat tuiCatalog
		if err := c.Call(ctx, "harness.list", nil, &cat); err != nil {
			return func(m *tuiModel) { m.flash = "harness.list: " + err.Error() }
		}
		return func(m *tuiModel) {
			m.catalog = &cat
			then(m)
		}
	}
}

// tuiFlipJob reads the thread's current flag and sets the opposite through
// method. describe words the result for the thread log.
func tuiFlipJob(threadID, method string, current func(tuiThreadState) bool, describe func(on bool) string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		st, err := fetchTuiThreadState(ctx, c, threadID)
		if err != nil {
			return tuiNote(threadID, "thread.get: "+err.Error())
		}
		on := !current(st)
		params := map[string]any{"threadId": threadID, "enabled": on}
		if err := c.Call(ctx, method, params, nil); err != nil {
			return tuiNote(threadID, method+": "+err.Error())
		}
		return tuiNote(threadID, describe(on))
	}
}

func tuiOnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// tuiRuntimeJob sets one runtime through method. extra adds fields such as a
// lead index or a role; an empty harness clears the runtime daemon-side.
func tuiRuntimeJob(threadID, method, ok string, rt contracts.LeadRuntime, extra map[string]any) tuiJob {
	params := map[string]any{"threadId": threadID, "harness": rt.Harness, "model": rt.Model, "variant": rt.Variant}
	for k, v := range extra {
		params[k] = v
	}
	return tuiCallJob(threadID, method, params, ok)
}

// pickRuntime walks the runtime menus: harness first, where the default clears
// the runtime, then model. done receives the chosen runtime.
func (m *tuiModel) pickRuntime(title string, done func(m *tuiModel, rt contracts.LeadRuntime)) {
	m.pickRuntimeOrClear(title, "(default)", done)
}

// pickRuntimeOrClear is pickRuntime with the clearing entry worded for what
// clearing means there; an empty clear omits it, so the pick must be a harness.
func (m *tuiModel) pickRuntimeOrClear(title, clear string, done func(m *tuiModel, rt contracts.LeadRuntime)) {
	if m.catalog == nil {
		m.startJob(tuiCatalogJob(func(m *tuiModel) { m.pickRuntimeOrClear(title, clear, done) }))
		return
	}
	var items []tuiPickItem
	if clear != "" {
		items = append(items, tuiPickItem{clear, ""})
	}
	for _, h := range m.catalog.Enabled {
		items = append(items, tuiPickItem{string(h), string(h)})
	}
	m.openPicker(title, items, func(m *tuiModel, harness string) {
		h := contracts.Harness(harness)
		if h == "" {
			done(m, contracts.LeadRuntime{})
			return
		}
		m.pickModel(title, h, done)
	})
}

// pickModel chooses a model for harness h. A harness that lists no models takes
// a typed one, since its catalog may be empty until a user adds models.
func (m *tuiModel) pickModel(title string, h contracts.Harness, done func(m *tuiModel, rt contracts.LeadRuntime)) {
	models := m.catalog.Models[string(h)]
	if len(models) == 0 {
		m.openPrompt(title+" · model (blank for default)", "", func(m *tuiModel, model string) {
			done(m, contracts.LeadRuntime{Harness: h, Model: model})
		})
		return
	}
	items := []tuiPickItem{{"(default)", ""}}
	for _, id := range models {
		items = append(items, tuiPickItem{id, id})
	}
	m.openPicker(title+" · model", items, func(m *tuiModel, model string) {
		done(m, contracts.LeadRuntime{Harness: h, Model: model})
	})
}

// --- Commands ---

func tuiNewThread(m *tuiModel, _ string) { m.newThread(false) }

// tuiNewSoloThread starts a thread that runs one agent on a harness chosen up
// front, with no orchestrator team.
func tuiNewSoloThread(m *tuiModel, _ string) { m.newThread(true) }

// newThread picks the project, then for a solo thread its harness and model,
// and creates the draft the composer opens on.
func (m *tuiModel) newThread(solo bool) {
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var projects []projectsRow
		if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
			return tuiNote("", "project.list: "+err.Error())
		}
		return func(m *tuiModel) {
			items := make([]tuiPickItem, 0, len(projects)+1)
			for _, p := range projects {
				items = append(items, tuiPickItem{projectLabel(p), p.ID})
			}
			items = append(items, tuiPickItem{"New project…", ""})
			title := "new thread in"
			if solo {
				title = "new solo thread in"
			}
			create := func(m *tuiModel, projectID string) {
				if !solo {
					m.startJob(tuiDraftJob(projectID, nil))
					return
				}
				m.pickRuntimeOrClear("solo harness", "", func(m *tuiModel, rt contracts.LeadRuntime) {
					m.startJob(tuiDraftJob(projectID, &rt))
				})
			}
			m.openPicker(title, items, func(m *tuiModel, projectID string) {
				if projectID == "" {
					m.openNewProject(create)
					return
				}
				create(m, projectID)
			})
		}
	})
}

// tuiUnderDir reports whether dir is root or sits inside it. Both are
// followed through symlinks first, so a project registered by one spelling of
// its path is found from another.
func tuiUnderDir(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(tuiRealPath(root), tuiRealPath(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// tuiRealPath is path with its symlinks resolved, or path itself when it
// cannot be resolved (it may not exist on this machine at all).
func tuiRealPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// openNewProject asks for a name and a folder, then creates the project and
// runs then with its id. A nil then just reports the project.
func (m *tuiModel) openNewProject(then func(m *tuiModel, projectID string)) {
	// The folder the TUI was started in is the likeliest project: both
	// prompts open filled in from it, ready to confirm or type over.
	name := ""
	if m.cwd != "" {
		name = filepath.Base(m.cwd)
	}
	m.openPrompt("project name", name, func(m *tuiModel, name string) {
		if name == "" {
			m.notify("project name is required")
			return
		}
		m.openPrompt("project folder (absolute path)", m.cwd, func(m *tuiModel, path string) {
			if path == "" {
				m.notify("project folder is required")
				return
			}
			m.startJob(tuiCreateProjectJob(name, path, then))
		})
	})
}

func tuiCreateProjectJob(name, path string, then func(m *tuiModel, projectID string)) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var res struct {
			Project projectsRow `json:"project"`
		}
		if err := c.Call(ctx, "project.create", map[string]string{"name": name, "path": path}, &res); err != nil {
			return tuiNote("", "project.create: "+err.Error())
		}
		return func(m *tuiModel) {
			if then == nil {
				m.flash = "created project " + name
				return
			}
			then(m, res.Project.ID)
		}
	}
}

// tuiDraftJob creates an empty thread in a project and opens it. A draft cuts
// its worktree on the first turn, so an unused draft leaves nothing behind.
func tuiDraftJob(projectID string, solo *contracts.LeadRuntime) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var res struct {
			ThreadID string `json:"threadId"`
		}
		if err := c.Call(ctx, "thread.create", map[string]any{"projectId": projectID, "draft": true}, &res); err != nil {
			return tuiNote("", "thread.create: "+err.Error())
		}
		if solo != nil {
			params := map[string]any{"threadId": res.ThreadID, "harness": solo.Harness, "model": solo.Model, "variant": solo.Variant}
			if err := c.Call(ctx, "thread.setSolo", params, nil); err != nil {
				return tuiNote("", "thread.setSolo: "+err.Error())
			}
		}
		return func(m *tuiModel) {
			m.openID = res.ThreadID
			m.refresh = true
		}
	}
}

func tuiStopTurn(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var res struct {
			Stopped bool `json:"stopped"`
		}
		if err := c.Call(ctx, "thread.turn.stop", map[string]string{"threadId": id}, &res); err != nil {
			return tuiNote(id, "thread.turn.stop: "+err.Error())
		}
		if !res.Stopped {
			return tuiNote(id, "no running turn to stop")
		}
		return tuiNote(id, "stop requested")
	})
}

func tuiBtw(m *tuiModel, arg string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	tuiThreadArg(m, arg, "side question (not recorded)", func(m *tuiModel, question string) {
		m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
			var res struct {
				Answer string `json:"answer"`
			}
			if err := c.Call(ctx, "thread.btw", map[string]string{"threadId": id, "question": question}, &res); err != nil {
				return tuiNote(id, "thread.btw: "+err.Error())
			}
			lines := []string{"btw: " + question}
			lines = append(lines, strings.Split(strings.TrimRight(res.Answer, "\n"), "\n")...)
			return tuiNote(id, lines...)
		})
	})
}

func tuiShell(m *tuiModel, arg string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	tuiThreadArg(m, arg, "shell command", func(m *tuiModel, command string) {
		m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
			var res struct {
				ExitCode int  `json:"exitCode"`
				TimedOut bool `json:"timedOut"`
				Stopped  bool `json:"stopped"`
			}
			params := map[string]string{"threadId": id, "command": command}
			if err := c.Call(ctx, "thread.runCommand", params, &res); err != nil {
				return tuiNote(id, "thread.runCommand: "+err.Error())
			}
			status := fmt.Sprintf("$ %s  exit %d", command, res.ExitCode)
			switch {
			case res.TimedOut:
				status += " (timed out)"
			case res.Stopped:
				status += " (stopped)"
			}
			return tuiNote(id, status)
		})
	})
}

func tuiCouncil(m *tuiModel, arg string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	tuiThreadArg(m, arg, "council topic", func(m *tuiModel, topic string) {
		m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
			var res struct {
				Consensus string `json:"consensus"`
				Stopped   bool   `json:"stopped"`
			}
			if err := c.Call(ctx, "thread.council.start", map[string]string{"threadId": id, "topic": topic}, &res); err != nil {
				return tuiNote(id, "thread.council.start: "+err.Error())
			}
			if res.Stopped {
				return tuiNote(id, "council stopped")
			}
			lines := []string{"council consensus:"}
			lines = append(lines, strings.Split(strings.TrimRight(res.Consensus, "\n"), "\n")...)
			return tuiNote(id, lines...)
		})
	})
}

func tuiRename(m *tuiModel, arg string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	submit := func(m *tuiModel, title string) {
		if title == "" {
			m.notify("title is required")
			return
		}
		m.startJob(tuiRenameJob(id, title))
	}
	if arg != "" {
		submit(m, arg)
		return
	}
	m.openPrompt("rename thread", m.thread.Title, submit)
}

func tuiRenameJob(threadID, title string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if err := c.Call(ctx, "thread.rename", map[string]string{"threadId": threadID, "title": title}, nil); err != nil {
			return tuiNote(threadID, "thread.rename: "+err.Error())
		}
		return func(m *tuiModel) {
			if m.onThread(threadID) {
				m.thread.Title = title
			}
			m.refresh = true
			m.notify("renamed to " + title)
		}
	}
}

func tuiFork(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiStateJob(id, func(m *tuiModel, st tuiThreadState) {
		// Newest first, capped: a fork point is almost always recent.
		var items []tuiPickItem
		for i := len(st.Messages) - 1; i >= 0 && len(items) < 30; i-- {
			msg := st.Messages[i]
			if msg.ID == "" {
				continue
			}
			label := msg.Author + ": " + truncateText(strings.Join(strings.Fields(msg.Text), " "), 60)
			items = append(items, tuiPickItem{label, msg.ID})
		}
		m.openPicker("fork from message", items, func(m *tuiModel, messageID string) {
			m.startJob(tuiForkJob(id, messageID))
		})
	}))
}

func tuiForkJob(threadID, messageID string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var res struct {
			ThreadID string `json:"threadId"`
		}
		params := map[string]string{"threadId": threadID, "messageId": messageID}
		if err := c.Call(ctx, "thread.fork", params, &res); err != nil {
			return tuiNote(threadID, "thread.fork: "+err.Error())
		}
		return func(m *tuiModel) {
			m.refresh = true
			m.notify("forked to " + shortThreadID(res.ThreadID))
		}
	}
}

func tuiDelete(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	items := []tuiPickItem{
		{"Delete thread, keep its worktree", "keep"},
		{"Delete thread and its worktree", "worktree"},
		{"Cancel", ""},
	}
	m.openPicker("delete thread", items, func(m *tuiModel, choice string) {
		if choice == "" {
			return
		}
		m.startJob(tuiDeleteJob(id, choice == "worktree"))
	})
}

func tuiDeleteJob(threadID string, removeWorktree bool) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		params := map[string]any{"threadId": threadID, "removeWorktree": removeWorktree}
		if err := c.Call(ctx, "thread.delete", params, nil); err != nil {
			return tuiNote(threadID, "thread.delete: "+err.Error())
		}
		return func(m *tuiModel) {
			if m.onThread(threadID) {
				m.closeThread()
			}
			m.refresh = true
			m.flash = "deleted thread"
		}
	}
}

func tuiArchive(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiFlipJob(id, "thread.setArchived",
		func(st tuiThreadState) bool { return st.Archived },
		func(on bool) string {
			if on {
				return "archived"
			}
			return "unarchived"
		}))
}

func tuiAutoMode(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiFlipJob(id, "thread.setAutoMode",
		func(st tuiThreadState) bool { return st.AutoMode },
		func(on bool) string { return "auto mode " + tuiOnOff(on) }))
}

func tuiPeerMailbox(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiFlipJob(id, "thread.setPeerMailbox",
		func(st tuiThreadState) bool { return st.PeerMailbox },
		func(on bool) string { return "peer mailbox " + tuiOnOff(on) }))
}

// tuiSolo is /solo: with no argument it flips solo mode the way the desktop
// toggle does, "off" turns it off, and "<harness> [model]" turns it on there.
func tuiSolo(m *tuiModel, arg string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	fields := strings.Fields(arg)
	switch {
	case len(fields) == 0:
		m.startJob(tuiSoloToggleJob(id))
	case len(fields) == 1 && fields[0] == "off":
		m.startJob(tuiSoloSetJob(id, contracts.LeadRuntime{}))
	default:
		rt := contracts.LeadRuntime{Harness: contracts.Harness(fields[0]), Model: strings.Join(fields[1:], " ")}
		m.startJob(tuiSoloSetJob(id, rt))
	}
}

// tuiSoloPick is /solo-runtime: the harness and model menus, with the first
// entry turning solo mode off.
func tuiSoloPick(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.pickRuntimeOrClear("solo harness", "(off: orchestrator team)", func(m *tuiModel, rt contracts.LeadRuntime) {
		m.startJob(tuiSoloSetJob(id, rt))
	})
}

// tuiSoloSetJob sets the thread's solo runtime and says what it is now. An
// empty harness turns solo mode off, so the daemon's own rule is the one used.
func tuiSoloSetJob(threadID string, rt contracts.LeadRuntime) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		params := map[string]any{"threadId": threadID, "harness": rt.Harness, "model": rt.Model, "variant": rt.Variant}
		if err := c.Call(ctx, "thread.setSolo", params, nil); err != nil {
			return tuiNote(threadID, "thread.setSolo: "+err.Error())
		}
		return func(m *tuiModel) {
			if !m.onThread(threadID) {
				return
			}
			if rt.Harness == "" {
				m.thread.SoloRuntime = nil
				m.appendLogLines("solo mode off: the orchestrator team runs the next turn")
				return
			}
			m.thread.SoloRuntime = &rt
			m.appendLogLines("solo mode on: " + tuiRuntime(string(rt.Harness), rt.Model, rt.Variant) + " runs the next turn alone")
		}
	}
}

// tuiSoloToggleJob turns solo off when it is on. Turning it on picks the
// harness the desktop does: the orchestrator role's, else the lead's, else the
// first enabled one.
func tuiSoloToggleJob(threadID string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		st, err := fetchTuiThreadState(ctx, c, threadID)
		if err != nil {
			return tuiNote(threadID, "thread.get: "+err.Error())
		}
		if st.SoloRuntime != nil {
			return tuiSoloSetJob(threadID, contracts.LeadRuntime{})(ctx, c)
		}
		var cfg tuiSettingsWire
		var cat tuiCatalog
		if err := c.Call(ctx, "settings.get", nil, &cfg); err != nil {
			return tuiNote(threadID, "settings.get: "+err.Error())
		}
		if err := c.Call(ctx, "harness.list", nil, &cat); err != nil {
			return tuiNote(threadID, "harness.list: "+err.Error())
		}
		harness := cfg.Lead.Harness
		for _, r := range cfg.Roles {
			if r.ID == "orchestrator" && r.Harness != "" {
				harness = r.Harness
			}
		}
		if harness == "" && len(cat.Enabled) > 0 {
			harness = string(cat.Enabled[0])
		}
		if harness == "" {
			return tuiNote(threadID, "no harness is enabled: pick one with /solo <harness>")
		}
		return tuiSoloSetJob(threadID, contracts.LeadRuntime{Harness: contracts.Harness(harness)})(ctx, c)
	}
}

func tuiLead(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiStateJob(id, func(m *tuiModel, st tuiThreadState) {
		n := max(st.Leads, 1)
		if n == 1 {
			m.pickLeadRuntime(id, 0)
			return
		}
		items := make([]tuiPickItem, n)
		for i := range items {
			items[i] = tuiPickItem{fmt.Sprintf("lead %d", i+1), strconv.Itoa(i)}
		}
		m.openPicker("lead", items, func(m *tuiModel, index string) {
			i, _ := strconv.Atoi(index)
			m.pickLeadRuntime(id, i)
		})
	}))
}

func (m *tuiModel) pickLeadRuntime(threadID string, index int) {
	m.pickRuntime(fmt.Sprintf("lead %d runtime (default clears it)", index+1), func(m *tuiModel, rt contracts.LeadRuntime) {
		m.startJob(tuiRuntimeJob(threadID, "thread.setLeadRuntime", "lead runtime updated", rt, map[string]any{"index": index}))
	})
}

func tuiRole(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var roles []contracts.Role
		if err := c.Call(ctx, "roles.list", nil, &roles); err != nil {
			return tuiNote(id, "roles.list: "+err.Error())
		}
		return func(m *tuiModel) {
			items := make([]tuiPickItem, 0, len(roles))
			for _, r := range roles {
				items = append(items, tuiPickItem{r.Name + " (" + string(r.ID) + ")", string(r.ID)})
			}
			m.openPicker("role", items, func(m *tuiModel, role string) {
				m.pickRuntime("role "+role+" runtime (default clears it)", func(m *tuiModel, rt contracts.LeadRuntime) {
					m.startJob(tuiRuntimeJob(id, "thread.setRoleRuntime", "role runtime updated", rt, map[string]any{"role": role}))
				})
			})
		}
	})
}

func tuiSchedule(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.openPrompt("depends on (thread ids or prefixes, comma-separated; blank for none)", "", func(m *tuiModel, refs string) {
		deps, err := m.resolveThreadRefs(refs)
		if err != nil {
			m.notify(err.Error())
			return
		}
		m.openPrompt("prompt to run once they finish", "", func(m *tuiModel, prompt string) {
			if prompt == "" {
				m.notify("prompt is required")
				return
			}
			params := map[string]any{"threadId": id, "dependsOn": deps, "prompt": prompt}
			m.startJob(tuiCallJob(id, "thread.schedule", params, "scheduled"))
		})
	})
}

func tuiUnschedule(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiCallJob(id, "thread.unschedule", map[string]string{"threadId": id}, "schedule cleared"))
}

func tuiShowMailbox(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	m.startJob(tuiStateJob(m.thread.ID, func(m *tuiModel, st tuiThreadState) {
		m.openPane("peer mailbox", mailboxLines(st.Mailbox), true)
	}))
}

func tuiShowTasks(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	m.startJob(tuiStateJob(m.thread.ID, func(m *tuiModel, st tuiThreadState) {
		m.openPane("tasks", taskLines(st.Tasks), false)
	}))
}

func tuiAgentLogs(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	ids := make([]string, 0, len(m.roster))
	for agent := range m.roster {
		ids = append(ids, agent)
	}
	sort.Strings(ids)
	items := make([]tuiPickItem, len(ids))
	for i, agent := range ids {
		items[i] = tuiPickItem{agent + "  " + string(m.roster[agent]), agent}
	}
	m.openPicker("agent logs", items, func(m *tuiModel, agent string) {
		m.startJob(tuiAgentLogsJob(id, agent))
	})
}

// tuiAgentLogsJob reads an agent's full log, not the thread's tail, and opens
// it as a pane that follows the newest lines.
func tuiAgentLogsJob(threadID, agent string) tuiJob {
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		entries, err := fetchEntries(ctx, c, threadID, agent)
		if err != nil {
			return tuiNote(threadID, "agents.logs: "+err.Error())
		}
		return func(m *tuiModel) {
			if m.onThread(threadID) {
				m.openPane("agent "+agent, tuiLogBlocks(entries...), true)
			}
		}
	}
}

// tuiCopyReply asks the loop to put the newest agent reply on the clipboard.
func tuiCopyReply(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	reply := m.lastReply()
	if reply == "" {
		m.notify("no reply to copy yet")
		return
	}
	m.copyText = reply
}

func tuiDismissQuestion(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	if m.pending == nil {
		m.notify("no open question")
		return
	}
	id, qid := m.thread.ID, m.pending.id
	params := map[string]string{"threadId": id, "questionId": qid}
	m.startJob(tuiCallJob(id, "question.dismiss", params, "question dismissed"))
}

func tuiMarkRead(m *tuiModel, _ string) {
	if m.screen != tuiScreenThread {
		return
	}
	id := m.thread.ID
	m.startJob(tuiCallJob(id, "thread.markSeen", map[string]string{"threadId": id}, "marked read"))
}

// tuiMarkReadJob clears the daemon's unread state when a thread is opened. It
// is silent: opening a thread is the read, and failures do not interrupt it.
func tuiMarkReadJob(threadID string) tuiJob {
	return tuiCallJob(threadID, "thread.markSeen", map[string]string{"threadId": threadID}, "")
}
