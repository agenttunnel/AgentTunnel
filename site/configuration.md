---
layout: default
title: Configuration
description: Where AgentTunnel keeps its state, what the settings mean, and which environment variables it reads.
---

Most configuration happens in the desktop app. This page lists where AgentTunnel keeps its state, what the settings mean, and which environment variables it reads.

## State directory

The daemon keeps everything under one directory. The default is `~/.agenttunnel`. Change it with `--home-dir` on `atd` and `agenttunnel`.

```
~/.agenttunnel/
  settings.json              projects, roles, harnesses, models, relay, and other settings
  prefs.json                 per-viewer preferences
  threads/<thread>.jsonl     the event log of each thread; the source of truth
  logs/thread-output/        each thread's agent output, as <thread>.log
  logs/actions.jsonl         the action log
  attachments/               files attached to prompts
  .orchestrator-context/     the inherited context shared by every project
  update-notes.json          release notes from the last update
  relay.json                 relay state (when the relay is used)
  whatsapp.json, whatsapp.db WhatsApp link state (when WhatsApp is linked)
  daemon.json                how to reach the running daemon: address, token, pid
  daemon.lock                held briefly while a daemon starts
  atd.log                    output of a daemon started by the CLI
```

Threads are event-sourced. On start, the daemon replays each `threads/*.jsonl` file to rebuild its state, so the files are what matter for backups. A torn final line from an interrupted write is skipped. A thread with no activity is not saved at all.

`daemon.json` holds the token and is removed when the daemon exits. Don't share it.

Back up `~/.agenttunnel` by copying it while the daemon is stopped. To delete everything, stop the daemon and remove the directory. This cannot be undone.

Your repository is not copied here. Agents work in git worktrees in the project's `<repo>.worktrees/` directory, or in the project directory for non-git projects. Agent output and attachments are stored here, though, so they can contain code.

## Settings

The daemon writes `settings.json`, so change settings through the desktop app or the `settings.set` RPC, not by editing the file while the daemon runs. These are the keys the file holds:

| Key | Default | Meaning |
| --- | --- | --- |
| `workdir` | the `atd --workdir` value | the default working directory, used when none is set |
| `projects` | `[]` | registered projects, each with `id`, `name`, and `path` |
| `roles` | `{}` | per-role runtime: `{ "<role>": { "harness", "model", "variant" } }`. An omitted field uses the daemon default. |
| `autoMode` | `false` | let the orchestrator choose each task's model, from the approved models |
| `approvedByHarness` | `{}` | models auto mode may use, per harness: `{ "<harness>": ["<model>", ...] }` |
| `customModels` | `{}` | models you added, per harness. Claude and Codex have no model listing, so add theirs here. |
| `disabledHarnesses` | `[]` | harnesses the daemon will not use |
| `disabledRoles` | `[]` | roles that are off. The orchestrator and executor are required and ignore this. |
| `experimentalRoles` | `false` | allow the Context Manager and Wildcard roles |
| `staleAfterMinutes` | `30` | minutes of inactivity before a thread counts as stale (1 to 43200) |
| `relay` | `{ "enabled": false, "url": "" }` | the outbound relay. See [Remote access](remote-access.html#relay). |
| `masterRuntime` | unset | the runtime of the master chat (`harness`, `model`, `variant`) |
| `masterReporter` | `false` | send a status report over WhatsApp whenever the connection is established |

`approvedModels` is an older flat allowlist. The daemon reads it once to migrate to `approvedByHarness`.

## Environment variables

### Read by AgentTunnel

| Variable | Used for |
| --- | --- |
| `AT_ATD` | path to the `atd` binary the CLI starts. Default: `atd` beside `agenttunnel`, then on `PATH`. |
| `VISUAL`, `EDITOR` | editor opened by `ctrl+g` in the terminal UI. `VISUAL` wins. |
| `CLAUDE_CONFIG_DIR` | where Claude's credentials live, for `agenttunnel usage --limits`. Default `~/.claude`. |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude token for subscription limits. Used in place of the credentials file. |
| `COMMAND_CODE_API_KEY` | Command Code key for subscription limits |
| `XDG_CONFIG_HOME` | where the Linux `systemd --user` service unit is written. Default `~/.config`. |
| `AT_WHATSAPP_TOKEN`, `AT_WHATSAPP_PHONE_NUMBER_ID`, `AT_WHATSAPP_TO` | WhatsApp Cloud API: access token, sending phone number ID, and recipient. Set all three to use the Cloud API instead of the companion link. |
| `AT_WHATSAPP_GRAPH_URL` | override the Cloud API base URL |
| `AT_WHATSAPP_PAIRING_CODE` | registration PIN, for a number that has not been registered yet |

The daemon also adds the usual tool directories to its `PATH` (`~/.local/bin`, `~/.bun/bin`, `~/go/bin`, and similar) so provider CLIs installed there are found.

### Set for agents, not for you

The daemon sets these inside each agent's environment so agents can call back into it: `AT_AGENT_ID`, `AT_THREAD_ID`, `AT_DAEMON_URL`, `AT_TOKEN`, and `AT_TOOL`. Don't set them yourself. A daemon refuses to start when `AT_AGENT_ID` is set, to keep agents from starting a second daemon.

## Token and address

`atd` generates a token when you don't pass `--token`. On loopback, the pairing URL printed at start carries the token, and the desktop app and CLI read it from `daemon.json`.

On a non-loopback address, `atd` does not print the token. Set `--token` yourself and send it as an `Authorization: Bearer` header. The token is also accepted as a `?token=` query parameter, but only from loopback peers.

Passing `--daemon <ws-url>` tells the CLI which daemon to use. An explicit daemon is never started automatically.
