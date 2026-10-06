# Command Code

AgentTunnel uses the Command Code setup on the connected environment. With a remote environment,
its Command Code login and configuration apply, not the setup on your desktop or phone.

Command Code is off by default. Add or enable a Command Code provider in **Settings > AgentTunnel**
to turn it on. AgentTunnel runs the `command-code` binary on the environment's `PATH` and drives it
through its built-in [Agent Client Protocol](https://agentclientprotocol.com) agent
(`command-code acp`). Set **Binary path** on a Command Code provider to point at a different install
or a specific executable. The value accepts a bare command name such as `command-code` or an
absolute path. You can add more than one Command Code provider, each with its own binary path and
provider environment variables.

## Authentication

AgentTunnel does not store Command Code credentials. Sign in with the CLI itself on the environment:

```bash
command-code login
```

On Windows the command is `cmdc login`; `command-code` also works everywhere. The ACP session
reuses that account. AgentTunnel runs `command-code status --json` during the provider check and
shows the signed-in user; when the environment is not signed in, the provider reports a warning and
starting a turn returns Command Code's own authentication error.

Text generation (commit messages, pull request text, branch names, and thread titles) also runs
through Command Code and needs the same authentication.

## Refresh the model list

AgentTunnel discovers the model catalog from an ACP session when an enabled Command Code provider
starts and keeps the list in its cache. Reconnecting a client or using a refresh control asks
Command Code for the list again. The periodic provider health setting does not refresh the catalog.

After changing a Command Code login or the models it can reach outside AgentTunnel, open
**Settings > AgentTunnel**, select the environment, and choose **Refresh provider status**.
Changing the provider's configuration in AgentTunnel also replaces that provider connection.

On mobile, open the thread settings and select **Refresh models**. If a refresh fails, AgentTunnel
keeps the last known models; fix the problem and refresh again. A successful refresh can remove
entries Command Code no longer offers.

## Continue an existing thread

Each thread is pinned to one Command Code ACP session. AgentTunnel stores the session id in the
thread and resumes it with `session/load` when the thread continues, so a reconnect or environment
restart picks the conversation back up.

You can switch models within a running thread. AgentTunnel sends the new model to Command Code
before the next turn. An existing thread keeps its selected model when that model is temporarily
absent from the catalog; if Command Code rejects it, select an available model before trying again.

## Permissions

The thread's runtime mode maps onto Command Code's permission modes:

- **Approval required** uses Command Code's `default` mode — it asks before running tools that
  change anything.
- **Auto-accept edits** uses `auto-accept` — file edits are accepted, riskier actions still ask.
- **Auto** uses `dont-ask` — runs without prompting, keeping the safety rails on.
- **Full access** uses `bypass` — every permission check is skipped.

When Command Code does ask, AgentTunnel shows the request in the thread and waits for your decision.
The plan interaction mode selects Command Code's `plan` mode for read-only research and planning.

## Limitations

- **Authentication is external.** AgentTunnel cannot sign you in; run `command-code login` on the
  environment first.
- **Usage and cost are not in the Usage page.** The Review usage page does not include Command Code
  activity, provider shares, or an API-equivalent cost.
