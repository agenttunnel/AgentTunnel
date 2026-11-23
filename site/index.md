---
layout: default
title: Overview
---

# AgentTunnel

AgentTunnel is a minimal GUI for coding agents. A planning **orchestrator** breaks a task into jobs and dispatches them to **planner, executor, tester, and reviewer** subagents. Each agent runs on a provider CLI you already use: OpenCode, Claude, Codex, Cursor, Pi, or Command Code.

The app is two Go programs:

- **`atd`** is the daemon. It runs the agents and serves a JSON-RPC API over WebSocket.
- **The desktop app** (`agenttunnel-desktop`) embeds the daemon and is the main way to use it.

`agenttunnel` is the command-line client. It talks to the same daemon.

## Install

**macOS.** Download `AgentTunnel-darwin-arm64.dmg` (Apple Silicon) or `AgentTunnel-darwin-amd64.dmg` (Intel) from the [releases page](https://github.com/agenttunnel/AgentTunnel/releases), open it, and drag AgentTunnel into Applications. The builds are unsigned, so the installer below is the easier route: it clears Gatekeeper quarantine for you.

**macOS, Linux, and Windows** use the installer:

```sh
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
```

The installer:

- puts `AgentTunnel.app` in `~/Applications` (macOS),
- puts `atd`, `agenttunnel`, and `agenttunnel-desktop` in `~/.local/bin`, and
- registers `atd` as an always-on service (launchd, `systemd --user`, or Task Scheduler) so the daemon runs from login.

Set `AGENTTUNNEL_NO_SERVICE=1` to skip the service. Set `AGENTTUNNEL_INSTALL_DIR` or `AGENTTUNNEL_APP_DIR` to choose other locations, and `AGENTTUNNEL_VERSION=vX.Y.Z` to pin a release. Make sure `~/.local/bin` is on your `PATH`.

Linux needs GTK 4 and WebKitGTK 6.0 at runtime for the desktop app. On Debian or Ubuntu these are `libgtk-4-1` and `libwebkitgtk-6.0-4`. `atd` has no GUI dependencies.

AgentTunnel does not install the provider CLIs. Install and sign in to the ones you want to use. [Getting started](getting-started.html) shows how to check them.

### Build from source

Requires Go (see `agenttunnel/go.mod`) and Node for the desktop frontend.

```sh
git clone https://github.com/agenttunnel/AgentTunnel.git
cd AgentTunnel/agenttunnel
go build ./...
./desktop/build.sh          # desktop app: frontend, then the linked binary in desktop/bin/
```

## Versions and updates

- Tagged releases (`vX.Y.Z`) are the stable builds. The installer and `releases/latest` use them.
- Every merge to `main` publishes a prerelease named `main-<short sha>`. Pin one with `AGENTTUNNEL_VERSION=main-<sha>`.
- The desktop app shows an **Update** button in the titlebar when a newer release exists. Restart after updating.
- `atd update --check` reports whether a newer daemon build is available. `atd update` installs it after a prompt. A daemon that is already running keeps its old build until it restarts.
- Re-running the installer also updates everything to the latest tagged release.

To uninstall, run the installer with `--uninstall`. Your threads and settings in `~/.agenttunnel` are kept. See [Configuration](configuration.html) for what that directory holds.

## Next

- [Getting started](getting-started.html): run your first task.
- [Features](features.html): desktop app, threads, worktrees, orchestrator, roles, harnesses, schedules, and remote access.
- [CLI](cli.html): every command and flag.
- [Configuration](configuration.html): state directory, settings, and environment variables.
