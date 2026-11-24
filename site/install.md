---
layout: default
title: Install
description: Install AgentTunnel on macOS, Linux, or Windows, or build it from source.
---

## macOS

Download `AgentTunnel-darwin-arm64.dmg` (Apple Silicon) or `AgentTunnel-darwin-amd64.dmg` (Intel) from the [releases page](https://github.com/agenttunnel/AgentTunnel/releases), open it, and drag AgentTunnel into Applications.

The builds are unsigned, so the installer below is the easier route. It clears Gatekeeper quarantine for you.

## Installer

On macOS, Linux, and Windows, run:

```sh
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
```

The installer:

- puts `AgentTunnel.app` in `~/Applications` (macOS),
- puts `atd`, `agenttunnel`, and `agenttunnel-desktop` in `~/.local/bin`, and
- registers `atd` as an always-on service (launchd, `systemd --user`, or Task Scheduler) so the daemon runs from login.

Make sure `~/.local/bin` is on your `PATH`.

### Installer options

| Variable | Effect |
| --- | --- |
| `AGENTTUNNEL_NO_SERVICE=1` | skip the always-on service |
| `AGENTTUNNEL_INSTALL_DIR` | install the binaries somewhere other than `~/.local/bin` |
| `AGENTTUNNEL_APP_DIR` | install the app somewhere other than `~/Applications` |
| `AGENTTUNNEL_VERSION=vX.Y.Z` | pin a release. Use `main-<sha>` for a prerelease. |

## Linux desktop dependencies

The desktop app needs GTK 4 and WebKitGTK 6.0 at runtime. On Debian or Ubuntu, install `libgtk-4-1` and `libwebkitgtk-6.0-4`. `atd` has no GUI dependencies.

## Build from source

Build from source with Go (see `agenttunnel/go.mod`) and Node for the desktop frontend.

```sh
git clone https://github.com/agenttunnel/AgentTunnel.git
cd AgentTunnel/agenttunnel
go build ./...
./desktop/build.sh          # desktop app: frontend, then the linked binary in desktop/bin/
```

## Updates

- Tagged releases (`vX.Y.Z`) are the stable builds. The installer and `releases/latest` use them.
- Every merge to `main` publishes a prerelease named `main-<short sha>`.
- The desktop app shows an **Update** button in the titlebar when a newer release exists. Restart after updating.
- `atd update --check` reports whether a newer daemon build is available. `atd update` installs it after a prompt. A daemon that is already running keeps its old build until it restarts.
- Re-running the installer also updates everything to the latest tagged release.

## Uninstall

Run the installer with `--uninstall`. Your threads and settings in `~/.agenttunnel` are kept. See [Configuration](configuration.html) for what that directory holds.
