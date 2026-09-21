# Install AgentTunnel

AgentTunnel is a web and desktop GUI for running coding agents on your machine.

## Requirements

Node.js `^22.16 || ^23.11 || >=24.10` on the machine that runs the AgentTunnel server.

At least one provider CLI, installed and authenticated. See [Providers](#providers) below.

## Run Without Installing

```bash
npx t3@latest
```

This starts the AgentTunnel server on your machine and opens the local web app. Use
`npx t3@latest --help` for the full CLI reference.

## Desktop App

Download the latest release from
[GitHub Releases](https://github.com/pingdotgg/t3code/releases), or install from a package
registry.

Windows:

```bash
winget install T3Tools.T3Code
```

macOS:

```bash
brew install --cask t3-code
```

Arch Linux:

Stable:

```bash
yay -S t3code-bin
```

Nightly:

```bash
yay -S t3code-nightly-bin
```

### Windows Subsystem for Linux

When the desktop app runs a WSL backend, it installs the matching server runtime into
`~/.agenttunnel/wsl-runtime` inside the selected distro. The first launch after installing or updating
AgentTunnel may take a little longer while that release's runtime is extracted. Later launches reuse the
Linux-local copy so startup does not depend on reading application files through `/mnt/c`. After a
successful launch, AgentTunnel keeps the current runtime and one previous runtime for rollback and
removes older caches automatically. If a cached runtime stops working, AgentTunnel launches from the
application files under `/mnt/c` instead and reinstalls the runtime on the next launch.

## Providers

AgentTunnel drives provider CLIs; it does not ship them. Install the CLI for each provider you want
to use, then authenticate it.

AgentTunnel is the default workflow provider. It uses the configured orchestrator, executor, and
reviewer harnesses; all three default to OpenCode. The other providers remain available as optional
single-agent runtimes.

Each role can have its own model in the AgentTunnel provider settings; when a role has no model set,
it falls back to the orchestrator model, and then to the model selected in the composer. Turn on
**Auto mode** in those settings to let the orchestrator pick a model for each executor and reviewer
task from the models the provider lists in that environment.
The per-role models remain as fallbacks when the orchestrator does not pick one, and harnesses stay
configured per role. If the orchestrator picks a model the environment does not offer, AgentTunnel
shows a warning and uses the fallback instead.

AgentTunnel orchestrators also learn across projects. Each environment keeps a hidden
`.orchestrator-context/` folder in its data directory (`~/.agenttunnel/userdata`, or `~/.agenttunnel/dev` for dev
installs). It holds a `GUIDE.md`, one rules file per category under `rules/`, and a `LOG.md` ledger.
Before planning, the orchestrator reads the rules; when a workflow ends, it records what it learned —
design decisions you liked or disliked, which harness and model combinations fit which tasks, and how
models performed overall — and AgentTunnel appends those entries with the date, project, and thread.
You can edit any of these files at any time and the next orchestrator inherits your edits. Only one
workflow updates the folder at a time.

| Provider   | CLI                                                      | Default binary | Log in with           |
| ---------- | -------------------------------------------------------- | -------------- | --------------------- |
| Codex      | [Codex CLI](https://developers.openai.com/codex/cli)     | `codex`        | `codex login`         |
| Claude     | [Claude Code](https://claude.com/product/claude-code)    | `claude`       | `claude auth login`   |
| Cursor     | [Cursor CLI](https://cursor.com/cli)                     | `cursor-agent` | `agent login`         |
| Grok Build | [Grok Build CLI](https://x.ai/cli)                       | `grok`         | `grok login`          |
| OpenCode   | [OpenCode](https://opencode.ai)                          | `opencode`     | `opencode auth login` |
| Pi         | [Pi](https://pi.dev) (`@earendil-works/pi-coding-agent`) | `pi`           | `pi`, then `/login`   |

Codex and Claude are on by default. Cursor, Grok Build, OpenCode, and Pi are off by default; turn
them on in **Settings** → the provider's card when you want to use them.

Cursor is the one to watch: install Cursor CLI, which provides the `cursor-agent` binary that
AgentTunnel looks for, but authenticate with `agent login`, not `cursor-agent login`.

Pi authenticates by running `pi` and using `/login` for a subscription provider, or by setting the
provider's API-key environment variables on the environment.

Run the login command on the machine running the AgentTunnel server, not on the device you browse
from.

### Binary Discovery

Each provider CLI must be on the server's `PATH`, or have an explicit binary path set in
**Settings** → the provider instance → **Binary path**. Use the explicit path when a version
manager or a non-standard install location keeps the CLI off the `PATH` of the shell that
started AgentTunnel.

### When Auth Is Needed

Provider auth is required before you start a session with that provider, not before you start
AgentTunnel. You can install AgentTunnel, open it, and add providers afterwards. A provider that is not
authenticated shows its status in **Settings** and fails at session start with the login command
to run.

For provider-specific setup and accounts, see [Codex](./providers-codex.md),
[Claude](./providers-claude.md), and [Pi](./providers-pi.md).

## Next Steps

- [Permission modes](./permission-modes.md): how much AgentTunnel asks before acting
- [Remote access](./remote-access.md): connect from a phone, tablet, or another desktop
- [Keeping AgentTunnel in sync](./updating.md): client and server version skew
- [Running in the background](./background-service.md): Linux background service
