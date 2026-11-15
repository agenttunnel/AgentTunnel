package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// describeEventLimit bounds the Events section to the newest rows, as kubectl
// describe does.
const describeEventLimit = 10

func init() {
	subcommands := []Command{
		{
			Name:    "thread",
			Summary: "Show a thread's full definition, agents, tasks and recent events",
			Usage:   "agenttunnel describe thread <thread> [-o json|yaml]",
			Run:     runDescribeThread,
		},
		{
			Name:    "agent",
			Summary: "Show one agent's definition and spend",
			Usage:   "agenttunnel describe agent <thread> <agent> [-o json|yaml]",
			Run:     runDescribeAgent,
		},
	}
	Register(Command{
		Name:        "describe",
		Summary:     "Show the full definition of a thread or agent",
		Usage:       "agenttunnel describe thread|agent ...",
		Subcommands: subcommands,
	})
	// inspect is docker's name for the same read; it runs the same subcommands.
	Register(Command{
		Name:        "inspect",
		Summary:     "Alias for describe",
		Usage:       "agenttunnel inspect thread|agent ...",
		Subcommands: subcommands,
	})
}

// threadRef names a related thread (parent or subthread) by id and title.
type threadRef struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// describeEvent is one row of the Events section: a thread.events entry.
type describeEvent struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// threadDescription is the describe view of one thread, assembled from
// thread.get plus the listing and project rows that name its relatives.
type threadDescription struct {
	ID          string                 `json:"id"`
	Title       string                 `json:"title"`
	Parent      *threadRef             `json:"parent,omitempty"`
	Project     string                 `json:"project,omitempty"`
	Worktree    *contracts.WorktreeRef `json:"worktree,omitempty"`
	Harness     string                 `json:"harness,omitempty"`
	Model       string                 `json:"model,omitempty"`
	Status      string                 `json:"status"`
	Created     *time.Time             `json:"created,omitempty"`
	Updated     time.Time              `json:"updated"`
	Turns       int                    `json:"turns"`
	Usage       contracts.TokenUsage   `json:"usage"`
	Roles       []string               `json:"roles"`
	PeerMailbox bool                   `json:"peerMailbox"`
	Subthreads  []threadRef            `json:"subthreads"`
	Agents      []contracts.Agent      `json:"agents"`
	Tasks       []domain.TaskItem      `json:"tasks"`
	Events      []describeEvent        `json:"events"`
}

// agentDescription is the describe view of one agent in its thread.
type agentDescription struct {
	ID        string               `json:"id"`
	Name      string               `json:"name,omitempty"`
	Thread    threadRef            `json:"thread"`
	Parent    string               `json:"parent,omitempty"`
	Pipeline  string               `json:"pipeline,omitempty"`
	Role      string               `json:"role"`
	Harness   string               `json:"harness"`
	Model     string               `json:"model,omitempty"`
	Status    string               `json:"status"`
	Reason    string               `json:"reason,omitempty"`
	Task      string               `json:"task,omitempty"`
	DependsOn []string             `json:"dependsOn"`
	Started   time.Time            `json:"started"`
	Ended     *time.Time           `json:"ended,omitempty"`
	Usage     contracts.TokenUsage `json:"usage"`
}

func runDescribeThread(ctx context.Context, env *Env, args []string) error {
	pos, format, err := parseDescribeArgs("agenttunnel describe thread <thread> [-o json|yaml]", args, env, 1)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return renderThreadDescribe(ctx, env, c, pos[0], format)
}

// renderThreadDescribe resolves the thread reference and prints its definition
// in the requested format (text, json or yaml).
func renderThreadDescribe(ctx context.Context, env *Env, c cliCaller, ref, format string) error {
	rows, err := threadRows(ctx, c)
	if err != nil {
		return err
	}
	id, err := matchThreadRow(rows, ref)
	if err != nil {
		return err
	}
	var st domain.ThreadState
	if err := c.Call(ctx, "thread.get", map[string]string{"threadId": id}, &st); err != nil {
		return err
	}
	events, err := recentEvents(ctx, c, id)
	if err != nil {
		return err
	}
	desc := describeThread(&st, rows, projectRows(ctx, c), events)
	switch format {
	case "json":
		return env.PrintJSON(desc)
	case "yaml":
		return writeYAMLish(env.Out, desc)
	default:
		desc.writeText(env.Out)
		return nil
	}
}

func runDescribeAgent(ctx context.Context, env *Env, args []string) error {
	pos, format, err := parseDescribeArgs("agenttunnel describe agent <thread> <agent> [-o json|yaml]", args, env, 2)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return renderAgentDescribe(ctx, env, c, pos[0], pos[1], format)
}

// renderAgentDescribe resolves the thread and agent references and prints the
// agent's definition in the requested format.
func renderAgentDescribe(ctx context.Context, env *Env, c cliCaller, threadArg, agentArg, format string) error {
	rows, err := threadRows(ctx, c)
	if err != nil {
		return err
	}
	id, err := matchThreadRow(rows, threadArg)
	if err != nil {
		return err
	}
	var st domain.ThreadState
	if err := c.Call(ctx, "thread.get", map[string]string{"threadId": id}, &st); err != nil {
		return err
	}
	agent, err := matchAgent(rosterOf(&st), agentArg)
	if err != nil {
		return err
	}
	desc := describeAgent(&st, agent)
	switch format {
	case "json":
		return env.PrintJSON(desc)
	case "yaml":
		return writeYAMLish(env.Out, desc)
	default:
		desc.writeText(env.Out)
		return nil
	}
}

// parseDescribeArgs reads the positional references and the output format. -o
// (or --output) picks json or yaml; the global --json selects json when no -o
// is given. Exactly want positionals are required.
func parseDescribeArgs(usage string, args []string, env *Env, want int) ([]string, string, error) {
	var pos []string
	format := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output="):
			value, next, err := usageFlagValue(args, i, "--output")
			if err != nil {
				return nil, "", err
			}
			format, i = value, next
		case strings.HasPrefix(arg, "-"):
			return nil, "", usageError("unknown flag %q (usage: %s)", arg, usage)
		default:
			pos = append(pos, arg)
		}
	}
	if len(pos) != want {
		return nil, "", usageError("usage: %s", usage)
	}
	switch {
	case format == "" && env.JSON:
		format = "json"
	case format == "":
		format = "text"
	case format != "json" && format != "yaml":
		return nil, "", usageError("unknown output format %q (want json or yaml)", format)
	}
	return pos, format, nil
}

// threadRows reads thread.list, the table thread references resolve against.
func threadRows(ctx context.Context, c cliCaller) ([]threadListRow, error) {
	var rows []threadListRow
	if err := c.Call(ctx, "thread.list", nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// projectRows reads project.list best effort: a missing project name falls
// back to its id rather than failing the describe.
func projectRows(ctx context.Context, c cliCaller) []projectsRow {
	var projects []projectsRow
	if err := c.Call(ctx, "project.list", nil, &projects); err != nil {
		return nil
	}
	return projects
}

// recentEvents reads the thread's graph-relevant events and keeps the newest
// describeEventLimit of them, oldest first.
func recentEvents(ctx context.Context, c cliCaller, threadID string) ([]describeEvent, error) {
	var events []describeEvent
	if err := c.Call(ctx, "thread.events", map[string]string{"threadId": threadID}, &events); err != nil {
		return nil, err
	}
	if len(events) > describeEventLimit {
		events = events[len(events)-describeEventLimit:]
	}
	if events == nil {
		events = []describeEvent{}
	}
	return events, nil
}

// rosterOf returns a thread's agents in roster order.
func rosterOf(st *domain.ThreadState) []contracts.Agent {
	agents := make([]contracts.Agent, 0, len(st.AgentOrder))
	for _, id := range st.AgentOrder {
		if a, ok := st.Agents[id]; ok {
			agents = append(agents, a)
		}
	}
	return agents
}

func describeThread(st *domain.ThreadState, rows []threadListRow, projects []projectsRow, events []describeEvent) threadDescription {
	roster := rosterOf(st)
	titles := make(map[string]string, len(rows))
	for _, r := range rows {
		titles[r.ID] = r.Title
	}

	d := threadDescription{
		ID:          string(st.ID),
		Title:       st.Title,
		Worktree:    st.Worktree,
		Status:      threadStatus(st, roster),
		Updated:     st.UpdatedAt,
		Turns:       st.Turn,
		Roles:       []string{},
		PeerMailbox: st.PeerMailbox,
		Subthreads:  []threadRef{},
		Agents:      roster,
		Tasks:       st.Tasks,
		Events:      events,
	}
	if d.Tasks == nil {
		d.Tasks = []domain.TaskItem{}
	}
	if st.ParentThreadID != "" {
		d.Parent = &threadRef{ID: string(st.ParentThreadID), Title: titles[string(st.ParentThreadID)]}
	}
	if p, ok := projectByID(projects, st.ProjectID); ok {
		d.Project = projectLabel(p)
	} else if st.ProjectID != "" {
		d.Project = st.ProjectID
	}
	if len(st.Messages) > 0 {
		created := st.Messages[0].At
		d.Created = &created
	}
	for _, id := range st.Subthreads {
		d.Subthreads = append(d.Subthreads, threadRef{ID: string(id), Title: titles[string(id)]})
	}
	for _, u := range st.Usage {
		d.Usage = d.Usage.Add(u.Usage)
	}

	seen := make(map[contracts.RoleID]bool)
	for _, a := range roster {
		if !seen[a.Role] {
			seen[a.Role] = true
			d.Roles = append(d.Roles, string(a.Role))
		}
	}
	// The lead's harness and model are the thread's; the thread's own model
	// selection is the fallback before the lead starts.
	d.Model = st.Model.Model
	for _, a := range roster {
		if a.IsOrchestrator() {
			d.Harness = string(a.Harness)
			if a.Model != "" {
				d.Model = a.Model
			}
			break
		}
	}
	return d
}

// threadStatus is the thread's one-word state: a draft has not run, an agent in
// flight means running, otherwise the thread is idle (or archived).
func threadStatus(st *domain.ThreadState, roster []contracts.Agent) string {
	for _, a := range roster {
		if a.Status == contracts.AgentRunning {
			return "running"
		}
	}
	switch {
	case st.Draft:
		return "draft"
	case st.Archived:
		return "archived"
	default:
		return "idle"
	}
}

func describeAgent(st *domain.ThreadState, a contracts.Agent) agentDescription {
	d := agentDescription{
		ID:        string(a.ID),
		Name:      a.Name,
		Thread:    threadRef{ID: string(st.ID), Title: st.Title},
		Parent:    string(a.ParentAgentID),
		Pipeline:  string(a.PipelineID),
		Role:      string(a.Role),
		Harness:   string(a.Harness),
		Model:     a.Model,
		Status:    string(a.Status),
		Reason:    a.Reason,
		Task:      a.Task,
		DependsOn: a.DependsOn,
		Started:   a.StartedAt,
		Ended:     a.EndedAt,
	}
	if d.DependsOn == nil {
		d.DependsOn = []string{}
	}
	if u, ok := st.Usage[a.ID]; ok {
		d.Usage = u.Usage
	}
	return d
}

func (d threadDescription) writeText(w io.Writer) {
	field := fieldWriter(w)
	field("Name", usageDash(d.Title))
	field("ID", d.ID)
	field("Parent", threadRefLabel(d.Parent))
	field("Project", usageDash(d.Project))
	field("Worktree", worktreeLabel(d.Worktree))
	field("Harness/model", joinNonEmpty(d.Harness, d.Model, "/"))
	field("Status", d.Status)
	field("Created", timeLabel(d.Created))
	field("Updated", d.Updated.Local().Format(describeTimeLayout))
	field("Turns", strconv.Itoa(d.Turns))
	field("Spend", spendLabel(d.Usage))
	field("Roles", listOrNone(d.Roles))
	field("Peer mailbox", onOff(d.PeerMailbox))
	field("Subthreads", subthreadsLabel(d.Subthreads))

	fmt.Fprintln(w, "\nAgents:")
	if len(d.Agents) == 0 {
		fmt.Fprintln(w, "  <none>")
	} else {
		rows := make([][]string, 0, len(d.Agents))
		for _, a := range d.Agents {
			rows = append(rows, []string{
				string(a.ID),
				agentDisplayName(a),
				string(a.Role),
				string(a.Status),
				string(a.Harness),
				usageDash(string(a.ParentAgentID)),
				usageDash(string(a.PipelineID)),
			})
		}
		renderDockerTable(w, []string{"ID", "NAME", "ROLE", "STATUS", "HARNESS", "PARENT", "PIPELINE"}, rows)
	}

	fmt.Fprintln(w, "\nTasks:")
	if len(d.Tasks) == 0 {
		fmt.Fprintln(w, "  <none>")
	} else {
		rows := make([][]string, 0, len(d.Tasks))
		for _, t := range d.Tasks {
			rows = append(rows, []string{t.Name, string(t.Status), t.Title})
		}
		renderDockerTable(w, []string{"NAME", "STATUS", "TITLE"}, rows)
	}

	fmt.Fprintln(w, "\nEvents:")
	if len(d.Events) == 0 {
		fmt.Fprintln(w, "  <none>")
	} else {
		rows := make([][]string, 0, len(d.Events))
		for _, e := range d.Events {
			rows = append(rows, []string{e.At.Local().Format(describeTimeLayout), e.Type, eventDetail(e.Payload)})
		}
		renderDockerTable(w, []string{"TIME", "TYPE", "DETAIL"}, rows)
	}
}

func (d agentDescription) writeText(w io.Writer) {
	field := fieldWriter(w)
	field("Name", usageDash(d.Name))
	field("ID", d.ID)
	field("Thread", threadRefText(d.Thread))
	field("Parent", usageDash(d.Parent))
	field("Pipeline", usageDash(d.Pipeline))
	field("Role", d.Role)
	field("Harness/model", joinNonEmpty(d.Harness, d.Model, "/"))
	field("Status", d.Status)
	field("Reason", usageDash(d.Reason))
	field("Started", d.Started.Local().Format(describeTimeLayout))
	field("Ended", timeLabel(d.Ended))
	field("Depends on", listOrNone(d.DependsOn))
	field("Spend", spendLabel(d.Usage))
	fmt.Fprintln(w, "\nTask:")
	fmt.Fprintf(w, "  %s\n", usageDash(d.Task))
}

// fieldWriter returns a writer for kubectl-style "Name:   value" lines.
func fieldWriter(w io.Writer) func(name, value string) {
	return func(name, value string) {
		fmt.Fprintf(w, "%-14s %s\n", name+":", value)
	}
}

const describeTimeLayout = "2006-01-02 15:04:05"

func threadRefLabel(ref *threadRef) string {
	if ref == nil {
		return "<none>"
	}
	return threadRefText(*ref)
}

func threadRefText(ref threadRef) string {
	if ref.Title == "" {
		return ref.ID
	}
	return fmt.Sprintf("%s (%q)", ref.ID, ref.Title)
}

func subthreadsLabel(refs []threadRef) string {
	if len(refs) == 0 {
		return "<none>"
	}
	labels := make([]string, 0, len(refs))
	for _, r := range refs {
		labels = append(labels, threadRefText(r))
	}
	return strings.Join(labels, ", ")
}

func worktreeLabel(w *contracts.WorktreeRef) string {
	if w == nil {
		return "<project checkout>"
	}
	label := w.Path
	if w.Branch != "" {
		label += fmt.Sprintf(" (branch %s, base %s)", w.Branch, usageDash(w.Base))
	}
	if w.Inherited {
		label += " (inherited)"
	}
	return label
}

func spendLabel(u contracts.TokenUsage) string {
	return fmt.Sprintf("input %s  output %s  cost %s  tools %d",
		usageTokenCount(u.InputTokens), usageTokenCount(u.OutputTokens), usageCost(u.CostUSD), u.ToolUses)
}

func timeLabel(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format(describeTimeLayout)
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "<none>"
	}
	return strings.Join(items, ", ")
}

func onOff(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

// joinNonEmpty joins the non-empty parts with sep, or a dash when none is set.
func joinNonEmpty(first, second, sep string) string {
	var parts []string
	for _, p := range []string{first, second} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, sep)
}

// eventDetail is the Events DETAIL column: the payload's compact JSON, clipped
// to one line.
func eventDetail(payload json.RawMessage) string {
	if len(payload) == 0 {
		return "-"
	}
	return truncateText(string(payload), 100)
}

// writeYAMLish prints v as a YAML-shaped document for -o yaml. It is a
// read-only rendering of the describe views, so it covers only maps, lists and
// scalars, with map keys sorted. The JSON form stays the machine contract.
func writeYAMLish(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	for _, line := range yamlLines(tree, "") {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func yamlLines(v any, indent string) []string {
	switch node := v.(type) {
	case map[string]any:
		if len(node) == 0 {
			return []string{indent + "{}"}
		}
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, yamlEntry(indent+k+":", node[k], indent)...)
		}
		return lines
	case []any:
		if len(node) == 0 {
			return []string{indent + "[]"}
		}
		var lines []string
		for _, item := range node {
			sub := yamlLines(item, indent+"  ")
			sub[0] = indent + "- " + strings.TrimLeft(sub[0], " ")
			lines = append(lines, sub...)
		}
		return lines
	default:
		raw, _ := json.Marshal(node)
		return []string{indent + string(raw)}
	}
}

// yamlEntry writes "key: scalar" inline, and a nested value on the lines below.
func yamlEntry(key string, value any, indent string) []string {
	switch child := value.(type) {
	case map[string]any:
		if len(child) == 0 {
			return []string{key + " {}"}
		}
	case []any:
		if len(child) == 0 {
			return []string{key + " []"}
		}
	case nil, string, float64, bool:
		raw, _ := json.Marshal(child)
		return []string{key + " " + string(raw)}
	}
	return append([]string{key}, yamlLines(value, indent+"  ")...)
}
