# Terminal client

`t3-tui` is AgentTunnel in a terminal: pick a thread, read it, send a message, interrupt the turn.
It speaks the same WebSocket RPC as the web, desktop, and mobile clients, so a thread you work in the
terminal is the same thread everywhere.

```bash
bun apps/tui/src/index.tsx --url http://localhost:13773
```

It renders through [OpenTUI](https://github.com/sst/opentui) — the same terminal UI stack opencode's
client uses — so it runs under Bun (`engines.bun >= 1.3`) rather than Node.

- `--url` — the server origin. Defaults to `$T3CODE_URL`.
- `--token` — a session credential, sent as the WebSocket ticket the server already accepts.
  Defaults to `$T3CODE_TOKEN`. Omit it for a server that does not require authentication.
- `--demo` — render a canned thread and transcript without connecting, so the interface can be
  reviewed (and the keybindings tried) without a server or a credential.

## Keys

| Key       | What it does                                                                 |
| --------- | ---------------------------------------------------------------------------- |
| `↑` / `↓` | Move through the thread list, or scroll the transcript once a thread is open |
| `Enter`   | Open the selected thread, or send the message you typed                      |
| `Esc`     | Back to the thread list                                                      |
| `Ctrl-R`  | Refresh from the server                                                      |
| `Ctrl-I`  | Interrupt the running turn                                                   |
| `Ctrl-C`  | Quit                                                                         |

## What it shows

The thread list is every thread on the server, newest first. An open thread renders its messages, what
each sub-agent did (from the same fold the Agents panel uses), and a recap line — the thread's goal
and whether the fleet is working or settled — above the composer.

## Limits

- Snapshots only for now: the client reads the snapshot frame each subscription sends and does not
  apply the incremental event stream, so updates arrive on `Ctrl-R` (and after sending) rather than
  live. Streaming deltas are the next step.
- One server, one credential: the connection is wired directly, without the desktop/mobile connection
  supervisor that owns stored credentials, relay, and reconnect policy.
