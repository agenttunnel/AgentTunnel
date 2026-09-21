# Review usage

The Usage page combines Codex, Claude Code, Grok Build, and Pi activity from your connected
environments. It reads the providers' local session history and shows API-equivalent token cost,
processed tokens, cache savings, provider shares, and model breakdowns. Subscription billing is
separate from the raw token cost shown here.

Grok Build totals come from persisted session updates. Interactive turns that never wrote a
completed-turn record will not appear.

Pi totals come from its session files under `~/.pi/agent/sessions`, including the cost Pi reports
for each response. Sessions created by `/fork` or `/clone` copy the source branch's history into a
new file; those copies are de-duplicated so they count once.

Use **Past 24h** for an hourly chart covering the exact rolling 24-hour period. The **7 days**,
**30 days**, and **90 days** ranges use daily resolution. Cost and token toggles update both the
headline and chart, and refreshing rescans every connected environment.

## Per-thread token usage

While a thread is open, open the **Usage** surface from the right panel's surface launcher (or the
`+` menu) to see what that thread has spent. Close it like any other panel tab.

The pane is cumulative for the thread and shows:

- **Totals**: input, output, cached input, reasoning output, orchestrator, subagent, and total
  tokens, plus the provider's processed-token count and a cost figure when a harness priced the
  run. In workflow threads the orchestrator figures are the thread's own model usage and reconcile
  with the total: total equals orchestrator plus subagent tokens.
- **By turn**: each turn's input, output, and subagent tokens, and the turn's total.
- **Agents**: up to 100 subagents' role, harness, input, output, tool-call count, cost, and total
  (the roster keeps the live and most recent agents). In workflow threads an Orchestrator row comes
  first, showing the thread's own usage, the role and harness that ran it; it has no per-step
  drill-in. Select a subagent row to drill into its own stats: how many turns (model steps) it ran,
  the input, cached, output, reasoning, cost, and total tokens for each turn, and the context size
  fed into each step. A running agent's turns appear as they complete.

Cost is what each harness reported for its own run — the same API-equivalent figure the Usage page
shows, not money spent on a subscription. A harness with no rates for a model prices nothing, so
those rows show a dash instead of an estimate.

Subagent tokens count toward the turn that spawned the agent. Figures are exactly what the provider
reported; providers that report no usage leave the pane empty instead of estimating. AgentTunnel
threads count tokens streamed by their harness runs — the OpenCode and Pi harnesses both report
per-step frames — so the orchestrator, executor, and reviewer rows each carry the tokens their own
CLI reported.
