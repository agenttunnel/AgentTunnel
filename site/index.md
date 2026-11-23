---
layout: default
title: Introduction
description: AgentTunnel is a minimal GUI for coding agents. A planning orchestrator dispatches work to agents that run on the provider CLIs you already use.
---

AgentTunnel is a minimal GUI for coding agents. A planning **orchestrator** breaks a task into jobs and dispatches them to **planner, executor, tester, and reviewer** subagents. Each agent runs on a provider CLI you already use: OpenCode, Claude, Codex, Pi, or Command Code.

AgentTunnel is open source. It runs on your machine and uses the provider subscriptions you already have.

## How it works

AgentTunnel is two Go programs and one client:

| Program | What it is |
| --- | --- |
| `atd` | The daemon. It runs the agents and serves a JSON-RPC API over WebSocket. |
| `agenttunnel-desktop` | The desktop app. It embeds the daemon and is the main way to use AgentTunnel. |
| `agenttunnel` | The command-line client. It talks to the same daemon. |

> **Note:** AgentTunnel does not install the provider CLIs. Install and sign in to the ones you want to use. [Quickstart](getting-started.html#1-check-your-provider-clis) shows how to check them.

## Next steps

- [Install](install.html): get the desktop app, `atd`, and `agenttunnel` on your machine.
- [Quickstart](getting-started.html): run your first task from install to review.
- [Desktop app](desktop-app.html): learn the window and its tabs.
- [CLI](cli.html): every command and flag.
- [Configuration](configuration.html): the state directory, settings, and environment variables.
