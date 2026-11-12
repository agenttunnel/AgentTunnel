package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func init() {
	Register(Command{
		Name:    "usage",
		Summary: "Show token usage, cost, and subscription limits",
		Usage:   "agenttunnel usage [--thread <ref>] [--project <id>] [--limits]",
		Run:     runUsage,
	})
}

// cliCaller is the slice of Client the read-only commands need. Tests drive the
// renderers with a fake, so no daemon (or socket) is required.
type cliCaller interface {
	Call(ctx context.Context, method string, params, out any) error
}

// connectReadOnly dials a daemon that is already running. Unlike Env.Connect it
// never autostarts one: an empty ledger from a daemon this command just spawned
// would claim the environment used nothing.
func connectReadOnly(ctx context.Context, env *Env) (*Client, error) {
	if env.DaemonURL != "" {
		c, _, err := env.Connect(ctx)
		return c, err
	}
	c, _, err := env.connectExisting(ctx)
	if err != nil {
		return nil, fmt.Errorf("no AgentTunnel daemon is running; start one with `agenttunnel daemon start`")
	}
	return c, nil
}

// usageOptions is the parsed `agenttunnel usage` invocation.
type usageOptions struct {
	thread  string
	project string
	limits  bool
}

func parseUsageArgs(args []string) (usageOptions, error) {
	var opts usageOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--limits":
			opts.limits = true
		case arg == "--thread" || strings.HasPrefix(arg, "--thread="):
			value, next, err := usageFlagValue(args, i, "--thread")
			if err != nil {
				return opts, err
			}
			opts.thread, i = value, next
		case arg == "--project" || strings.HasPrefix(arg, "--project="):
			value, next, err := usageFlagValue(args, i, "--project")
			if err != nil {
				return opts, err
			}
			opts.project, i = value, next
		default:
			return opts, usageError("unknown argument %q (usage: agenttunnel usage [--thread <ref>] [--project <id>] [--limits])", arg)
		}
	}
	opts.thread = strings.TrimSpace(opts.thread)
	opts.project = strings.TrimSpace(opts.project)
	if opts.thread != "" && opts.project != "" {
		return opts, usageError("--thread and --project cannot be combined")
	}
	return opts, nil
}

// flagValue reads "--flag value" or "--flag=value" starting at i, returning the
// value and the index of the last argument consumed.
func usageFlagValue(args []string, i int, name string) (string, int, error) {
	arg := args[i]
	if strings.HasPrefix(arg, name+"=") {
		return strings.TrimPrefix(arg, name+"="), i, nil
	}
	if i+1 >= len(args) {
		return "", i, usageError("%s requires a value", name)
	}
	return args[i+1], i + 1, nil
}

// --- wire shapes ---

// usageThread is the subset of thread.get's ThreadState the usage readouts need.
type usageThread struct {
	ID         string                          `json:"id"`
	Exists     bool                            `json:"exists"`
	Title      string                          `json:"title"`
	Turn       int                             `json:"turn"`
	AgentOrder []string                        `json:"agentOrder"`
	Agents     map[string]usageAgent           `json:"agents"`
	Usage      map[string]contracts.AgentUsage `json:"usage"`
	Turns      []usageTurn                     `json:"turns"`
}

// usageAgent is the agent identity a usage row is enriched with.
type usageAgent struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	Harness       string `json:"harness"`
	Model         string `json:"model"`
	ParentAgentID string `json:"parentAgentId"`
}

// usageTurn is one turn's per-agent spend.
type usageTurn struct {
	Turn       int                             `json:"turn"`
	At         time.Time                       `json:"at"`
	AgentOrder []string                        `json:"agentOrder"`
	Agents     map[string]contracts.TokenUsage `json:"agents"`
}

// usageHarnessStat mirrors domain.HarnessStat: a per-harness rollup scoped to a
// project (stats.harnesses with projectId).
type usageHarnessStat struct {
	Harness   string               `json:"harness"`
	Threads   int                  `json:"threads"`
	Agents    int                  `json:"agents"`
	Subagents int                  `json:"subagents"`
	Usage     contracts.TokenUsage `json:"usage"`
}

// --- command ---

func runUsage(ctx context.Context, env *Env, args []string) error {
	opts, err := parseUsageArgs(args)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return renderUsage(ctx, env, c, opts)
}

// renderUsage performs the RPCs and renders (or, with --json, prints) the
// requested view. A caller-supplied client keeps it testable without a daemon.
func renderUsage(ctx context.Context, env *Env, c cliCaller, opts usageOptions) error {
	switch {
	case opts.thread != "":
		id, err := resolveThreadReference(ctx, c, opts.thread)
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if err := c.Call(ctx, "thread.get", map[string]string{"threadId": id}, &raw); err != nil {
			return err
		}
		if env.JSON {
			return printUsageJSON(ctx, env, c, opts, "thread", raw)
		}
		var st usageThread
		if err := json.Unmarshal(raw, &st); err != nil {
			return fmt.Errorf("thread.get: decode result: %w", err)
		}
		if !st.Exists {
			return fmt.Errorf("thread %q does not exist", id)
		}
		renderThreadUsage(env.Out, st)
		return appendLimits(ctx, env, c, opts)

	case opts.project != "":
		params := map[string]string{"projectId": opts.project}
		var raw json.RawMessage
		if err := c.Call(ctx, "stats.harnesses", params, &raw); err != nil {
			return err
		}
		if env.JSON {
			return printUsageJSON(ctx, env, c, opts, "usage", raw)
		}
		var stats []usageHarnessStat
		if err := json.Unmarshal(raw, &stats); err != nil {
			return fmt.Errorf("stats.harnesses: decode result: %w", err)
		}
		renderHarnessUsage(env.Out, opts.project, stats)
		return appendLimits(ctx, env, c, opts)

	default:
		var raw json.RawMessage
		if err := c.Call(ctx, "usage.get", nil, &raw); err != nil {
			return err
		}
		if env.JSON {
			return printUsageJSON(ctx, env, c, opts, "usage", raw)
		}
		var usage contracts.ThreadUsage
		if err := json.Unmarshal(raw, &usage); err != nil {
			return fmt.Errorf("usage.get: decode result: %w", err)
		}
		renderLedgerUsage(env.Out, usage, agentDetails(ctx, c))
		return appendLimits(ctx, env, c, opts)
	}
}

// printUsageJSON writes the raw RPC result. When limits were also requested the
// two results ride one object so each stays byte-identical to its RPC payload.
func printUsageJSON(ctx context.Context, env *Env, c cliCaller, opts usageOptions, key string, raw json.RawMessage) error {
	if !opts.limits {
		return writeRawJSON(env.Out, raw)
	}
	var limits json.RawMessage
	if err := c.Call(ctx, "usage.limits", nil, &limits); err != nil {
		return err
	}
	return env.PrintJSON(map[string]json.RawMessage{key: raw, "limits": limits})
}

func appendLimits(ctx context.Context, env *Env, c cliCaller, opts usageOptions) error {
	if !opts.limits {
		return nil
	}
	var limits []contracts.HarnessLimits
	if err := c.Call(ctx, "usage.limits", nil, &limits); err != nil {
		return err
	}
	fmt.Fprintln(env.Out)
	renderLimits(env.Out, limits)
	return nil
}

func writeRawJSON(w io.Writer, raw json.RawMessage) error {
	_, err := fmt.Fprintln(w, string(raw))
	return err
}

// --- thread refs ---

// usageThreadRef is a thread.list row, reduced to the fields a ref may match.
type usageThreadRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// resolveThreadReference accepts an exact thread id, a unique id prefix, or a
// unique title substring. Anything else is an error; ambiguity lists the
// candidates so the caller can narrow the ref while staying script-safe.
func resolveThreadReference(ctx context.Context, c cliCaller, ref string) (string, error) {
	var threads []usageThreadRef
	if err := c.Call(ctx, "thread.list", nil, &threads); err != nil {
		return "", err
	}
	return matchThreadRef(threads, ref)
}

func matchThreadRef(threads []usageThreadRef, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", usageError("thread reference is empty")
	}
	lower := strings.ToLower(ref)
	var exactID []usageThreadRef
	var exactTitle []usageThreadRef
	matched := make(map[string]usageThreadRef)
	for _, t := range threads {
		switch {
		case t.ID == ref:
			exactID = append(exactID, t)
		case t.Title == ref:
			exactTitle = append(exactTitle, t)
		}
		if strings.HasPrefix(t.ID, ref) || strings.Contains(strings.ToLower(t.Title), lower) {
			matched[t.ID] = t
		}
	}
	if len(exactID) == 1 {
		return exactID[0].ID, nil
	}
	if len(exactTitle) == 1 {
		return exactTitle[0].ID, nil
	}
	if len(matched) == 1 {
		for id := range matched {
			return id, nil
		}
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("no thread matches %q", ref)
	}
	candidates := make([]string, 0, len(matched))
	for _, t := range threads {
		if _, ok := matched[t.ID]; !ok {
			continue
		}
		label := t.ID
		if t.Title != "" {
			label += fmt.Sprintf(" (%q)", t.Title)
		}
		candidates = append(candidates, label)
	}
	return "", fmt.Errorf("thread reference %q is ambiguous; candidates: %s", ref, strings.Join(candidates, ", "))
}

// --- rendering ---

// usageTokenCount formats a token count the way the desktop does: 1.2k / 3.4M.
func usageTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func usageCost(c float64) string { return fmt.Sprintf("$%.4f", c) }

// renderDockerTable prints an aligned, borderless table with uppercase headers.
func renderDockerTable(w io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i := range headers {
			if i < len(row) && len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	writeRow := func(cells []string) {
		var b strings.Builder
		for i := range headers {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			b.WriteString(cell)
			if i < len(headers)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len(cell)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}
}

// usageSummaryLine is the totals row shared by the ledger and thread views.
func usageSummaryLine(w io.Writer, total contracts.TokenUsage) {
	fmt.Fprintf(w, "TOTAL  INPUT %s  OUTPUT %s  CACHE %s  COST %s  TOOLS %d\n",
		usageTokenCount(total.InputTokens),
		usageTokenCount(total.OutputTokens),
		usageTokenCount(total.CachedTokens),
		usageCost(total.CostUSD),
		total.ToolUses,
	)
}

// renderLedgerUsage renders the daemon-wide ledger: totals plus one row per
// agent. details (agent id -> identity) supplies the model and role, which the
// ledger itself does not carry.
func renderLedgerUsage(w io.Writer, usage contracts.ThreadUsage, details map[string]usageAgent) {
	total := usage.Orchestrator.Add(usage.Subagents)
	usageSummaryLine(w, total)
	if len(usage.ByAgent) == 0 {
		fmt.Fprintln(w, "\nno agent usage recorded yet")
		return
	}
	fmt.Fprintln(w)
	rows := make([][]string, 0, len(usage.ByAgent))
	for _, entry := range usage.ByAgent {
		detail := details[string(entry.AgentID)]
		role := string(entry.Role)
		if role == "" {
			role = detail.Role
		}
		model := detail.Model
		if model == "" {
			model = "-"
		}
		rows = append(rows, []string{
			string(entry.AgentID),
			usageDash(role),
			model,
			usageTokenCount(entry.Usage.InputTokens),
			usageTokenCount(entry.Usage.OutputTokens),
			usageTokenCount(entry.Usage.CachedTokens),
			usageCost(entry.Usage.CostUSD),
		})
	}
	renderDockerTable(w, []string{"AGENT", "ROLE", "MODEL", "INPUT", "OUTPUT", "CACHE", "COST"}, rows)
}

// renderThreadUsage renders one thread's durable usage: totals, the agent
// roster, and the per-turn split.
func renderThreadUsage(w io.Writer, st usageThread) {
	title := st.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(w, "THREAD %s  %q  turn #%d\n", st.ID, title, st.Turn)

	var total contracts.TokenUsage
	order := st.AgentOrder
	if len(order) == 0 {
		for id := range st.Usage {
			order = append(order, id)
		}
		sort.Strings(order)
	}
	for _, id := range order {
		total = total.Add(st.Usage[id].Usage)
	}
	usageSummaryLine(w, total)

	if len(order) == 0 {
		fmt.Fprintln(w, "\nno agent usage recorded yet")
	} else {
		fmt.Fprintln(w)
		rows := make([][]string, 0, len(order))
		for _, id := range order {
			entry := st.Usage[id]
			agent := st.Agents[id]
			role := string(entry.Role)
			if role == "" {
				role = agent.Role
			}
			rows = append(rows, []string{
				id,
				usageDash(role),
				usageDash(agent.Model),
				usageTokenCount(entry.Usage.InputTokens),
				usageTokenCount(entry.Usage.OutputTokens),
				usageTokenCount(entry.Usage.CachedTokens),
				usageCost(entry.Usage.CostUSD),
			})
		}
		renderDockerTable(w, []string{"AGENT", "ROLE", "MODEL", "INPUT", "OUTPUT", "CACHE", "COST"}, rows)
	}

	if len(st.Turns) == 0 {
		return
	}
	fmt.Fprintln(w)
	turnRows := make([][]string, 0, len(st.Turns))
	turns := append([]usageTurn(nil), st.Turns...)
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Turn < turns[j].Turn })
	for _, t := range turns {
		var turnTotal contracts.TokenUsage
		for _, u := range t.Agents {
			turnTotal = turnTotal.Add(u)
		}
		turnRows = append(turnRows, []string{
			fmt.Sprintf("%d", t.Turn),
			usageTokenCount(turnTotal.InputTokens),
			usageTokenCount(turnTotal.OutputTokens),
			usageTokenCount(turnTotal.CachedTokens),
			usageCost(turnTotal.CostUSD),
			fmt.Sprintf("%d", len(t.Agents)),
		})
	}
	renderDockerTable(w, []string{"TURN", "INPUT", "OUTPUT", "CACHE", "COST", "AGENTS"}, turnRows)
}

// renderHarnessUsage renders a project-scoped stats.harnesses rollup.
func renderHarnessUsage(w io.Writer, project string, stats []usageHarnessStat) {
	fmt.Fprintf(w, "PROJECT %s\n", project)
	if len(stats) == 0 {
		fmt.Fprintln(w, "\nno usage recorded in this project yet")
		return
	}
	var total contracts.TokenUsage
	rows := make([][]string, 0, len(stats))
	for _, s := range stats {
		total = total.Add(s.Usage)
		rows = append(rows, []string{
			usageDash(s.Harness),
			fmt.Sprintf("%d", s.Threads),
			fmt.Sprintf("%d", s.Agents),
			usageTokenCount(s.Usage.InputTokens),
			usageTokenCount(s.Usage.OutputTokens),
			usageTokenCount(s.Usage.CachedTokens),
			usageCost(s.Usage.CostUSD),
		})
	}
	fmt.Fprintln(w)
	renderDockerTable(w, []string{"HARNESS", "THREADS", "AGENTS", "INPUT", "OUTPUT", "CACHE", "COST"}, rows)
	fmt.Fprintf(w, "\nTOTAL  INPUT %s  OUTPUT %s  CACHE %s  COST %s\n",
		usageTokenCount(total.InputTokens),
		usageTokenCount(total.OutputTokens),
		usageTokenCount(total.CachedTokens),
		usageCost(total.CostUSD),
	)
}

// renderLimits prints each harness's subscription windows. An unavailable
// harness is unmetered: it never renders as 0%, because a missing quota is not
// an empty one.
func renderLimits(w io.Writer, limits []contracts.HarnessLimits) {
	if len(limits) == 0 {
		fmt.Fprintln(w, "LIMITS  no harness reported subscription limits")
		return
	}
	fmt.Fprintln(w, "LIMITS")
	var rows [][]string
	for _, h := range limits {
		harness := string(h.Harness)
		if !h.Available || len(h.Windows) == 0 {
			rows = append(rows, []string{harness, "-", "unmetered", "-", usageDash(h.Reason)})
			continue
		}
		for _, window := range h.Windows {
			rows = append(rows, []string{
				harness,
				usageDash(window.Label),
				fmt.Sprintf("%.0f%%", window.UsedPercent),
				usageResetAt(window.ResetsAt),
				usageDash(h.Reason),
			})
		}
	}
	renderDockerTable(w, []string{"HARNESS", "WINDOW", "USED", "RESETS", "NOTE"}, rows)
}

func usageResetAt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func usageDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// agentDetails maps every known agent id to its identity, so the ledger's
// per-agent rows can show a model and role. Best effort: a failed lookup leaves
// the model blank rather than failing the usage report.
func agentDetails(ctx context.Context, c cliCaller) map[string]usageAgent {
	details := map[string]usageAgent{}
	var threads []usageThreadRef
	if err := c.Call(ctx, "thread.list", nil, &threads); err != nil {
		return details
	}
	for _, t := range threads {
		var st usageThread
		if err := c.Call(ctx, "thread.get", map[string]string{"threadId": t.ID}, &st); err != nil {
			continue
		}
		for _, id := range st.AgentOrder {
			agent, ok := st.Agents[id]
			if !ok {
				continue
			}
			if _, seen := details[id]; seen && agent.Model == "" {
				continue
			}
			details[id] = agent
		}
	}
	return details
}
