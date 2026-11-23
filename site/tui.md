---
layout: default
title: Terminal UI
description: The full-screen terminal interface, on the same daemon as the desktop app.
---

## Terminal UI

`agenttunnel tui` opens a full-screen terminal interface on the same daemon. `agenttunnel tui <thread>` opens straight into a thread.

| Key | Does |
| --- | --- |
| `enter` | send the prompt |
| `alt+enter` | new line |
| `↑` `↓` | prompt history, or the options of a question an agent asked |
| `ctrl+g` | edit the prompt in `$VISUAL` or `$EDITOR` |
| `@` | search the thread's files, then `tab` to attach a path to the turn |
| `tab` `shift+tab` | move between views (Files, Review, PR, and so on) |
| `pgup` `pgdn` | scroll the transcript |
| `ctrl+k` | command palette |
| `ctrl+b` | show or hide the sidebar |
| `esc` | back to the thread list |

On the thread list, `/` filters, `n` starts a new thread, and `r` refreshes. `q` quits.

