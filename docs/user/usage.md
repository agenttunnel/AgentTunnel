# Review usage

The Usage page combines Codex, Claude Code, and Grok Build activity from your connected
environments. It reads the providers' local session history and shows API-equivalent token cost,
processed tokens, cache savings, provider shares, and model breakdowns. Subscription billing is
separate from the raw token cost shown here.

Grok Build totals come from persisted session updates. Interactive turns that never wrote a
completed-turn record will not appear.

Use **Past 24h** for an hourly chart covering the exact rolling 24-hour period. The **7 days**,
**30 days**, and **90 days** ranges use daily resolution. Cost and token toggles update both the
headline and chart, and refreshing rescans every connected environment.

## Per-thread token usage

While a thread is open, open the **Usage** surface from the right panel's surface launcher (or the
`+` menu) to see what that thread has spent. Close it like any other panel tab.

The pane is cumulative for the thread and shows:

- **Totals**: input, output, cached input, reasoning output, subagent, and total tokens, plus the
  provider's processed-token count when it reports one.
- **By turn**: each turn's input, output, and subagent tokens, and the turn's total.
- **Agents**: up to 100 subagents' input, output, tool-call count, and total (the roster keeps the
  live and most recent agents). Select an agent to drill into its own stats: how many turns (model
  steps) it ran, the input, cached, output, reasoning, and total tokens for each turn, and the
  context size fed into each step. A running agent's turns appear as they complete.

Subagent tokens count toward the turn that spawned the agent. Figures are exactly what the provider
reported; providers that report no usage leave the pane empty instead of estimating. AgentTunnel
threads count tokens streamed by OpenCode harness runs; a run on a harness that reports no token
frames contributes nothing.
