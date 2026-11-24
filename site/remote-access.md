---
layout: default
title: Remote access
description: Reach a daemon from another machine or a phone, over your network or through a relay.
---


By default `atd` listens on `127.0.0.1` only, and its pairing URL carries the token. There are two ways to reach a daemon from elsewhere.

## Bind to the network

`atd --host 0.0.0.0 --token <secret>` listens on the network. When the daemon is not on loopback, it doesn't print the token in its log. Clients send the token as an `Authorization: Bearer` header. Use this only on a network you trust, or behind TLS.

## Relay

The relay lets a phone reach a daemon that has no inbound port:

- `atrelay` is a self-hosted relay. Run it on a host you control. The relay operator can read the traffic it routes, so don't use someone else's relay.
- The daemon only dials out, and the relay is **off by default**. In the desktop app, open **Remote** in the sidebar footer and enter your relay's URL. Or start `atd` with `--relay wss://your-relay.example`.
- Plain `ws://` is accepted only for loopback and private addresses. For the internet, put `atrelay` behind a TLS proxy and use `wss://`.

```sh
atrelay --listen 127.0.0.1:7777                # loopback, for testing
atrelay --listen 0.0.0.0:7777                  # the LAN; prints the URL to use
```

The phone app lives in `agenttunnel/mobile`. It is an Expo app that you build from source. There are no published store builds yet.

See [`docs/relay-protocol.md`](https://github.com/agenttunnel/AgentTunnel/blob/main/agenttunnel/docs/relay-protocol.md) for the wire protocol.
