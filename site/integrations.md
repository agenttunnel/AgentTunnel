---
layout: default
title: Integrations
description: Web client, pull requests and issues, and WhatsApp updates.
---

## Web client

`agenttunnel web [dir]` serves the web UI on `127.0.0.1`, and opens it in your browser. It prefers port 7878 and uses another free port if that one is taken. If a daemon is already running, the web UI attaches to it. Otherwise it starts one for the session. The web UI listens on loopback only.

## Pull requests and issues

The **Pull request** panel and the `pr.*` and `issues.list` RPCs use your installed `gh` (GitHub) or `glab` (GitLab) CLI. They are not available unless that CLI is installed and signed in.

## WhatsApp

AgentTunnel can post workflow updates to WhatsApp. Link it with:

```sh
agenttunnel whatsapp connect
```

This shows a QR code in the terminal to scan from WhatsApp. Without configuration, the daemon links as a companion device and keeps its session in the state directory. To use the WhatsApp Cloud API instead, set `AT_WHATSAPP_TOKEN`, `AT_WHATSAPP_PHONE_NUMBER_ID`, and `AT_WHATSAPP_TO`. See [Configuration](configuration.html#environment-variables).

