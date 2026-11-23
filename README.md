<p align="center">
  <img src="agenttunnel/desktop/frontend/public/brand-mark.svg" width="56" alt="AgentTunnel" />
</p>

# AgentTunnel

AgentTunnel is a minimal GUI for coding agents: a multi-agent coding workflow
where a planning **orchestrator** breaks a task into jobs and dispatches them to
**planner / executor / tester / reviewer** subagents, each of which can run on
any provider CLI (Codex, Claude, OpenCode, Pi, Command Code).

The app is written in Go: the `atd` daemon supervises concurrent provider
subprocesses and fans their streams out over WebSocket, and a Wails v3 desktop
client drives it. Both ship as static binaries.

> The previous TypeScript monorepo (web app, Electron desktop, mobile client,
> relay) was removed when the Go app became the main product. Git history
> retains everything.

## Install

**macOS** — download `AgentTunnel-darwin-arm64.dmg` (Apple Silicon) or
`AgentTunnel-darwin-amd64.dmg` (Intel) from
[the releases page](https://github.com/agenttunnel/AgentTunnel/releases), open
it, and drag AgentTunnel into Applications.

The installer sets up the app in `~/Applications` (so it gets its icon in
Spotlight/Launchpad) and links the terminal command into `~/.local/bin`:

```
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
```

**Linux / Windows** — the installer (or the release tarball) provides `atd`,
the daemon, `agenttunnel`, the CLI, and `agenttunnel-desktop`, the desktop client. Linux needs GTK 4 and
WebKitGTK 6.0 at runtime.

To build a DMG locally instead:

```
cd agenttunnel/desktop
./build-app.sh --dmg
# dist/AgentTunnel-darwin-<arch>.dmg
```

### Versions

- **Tagged releases** (`vX.Y.Z`) are the stable builds. `releases/latest` and the
  installer resolve to these.
- **Every merge to `main`** publishes a prerelease named `main-<short sha>`. Its
  version is the 7-character commit hash (for example `main-1a2b3c4`). Prereleases
  are never "latest", so the installer keeps pointing at tagged builds. Pin one with
  `AGENTTUNNEL_VERSION=main-1a2b3c4`.

The version is embedded at link time (`-X main.version=…`). Check it with
`atd --version` or `agenttunnel --version`; local builds report `dev`.

### Update

- **Desktop app:** when a newer release exists, an **Update to vX.Y.Z** button
  appears in the titlebar. Click it, then restart AgentTunnel to run the new build.
- **Daemon and CLI:** `atd update --check` reports whether a newer release is
  available. `atd update` installs it after a prompt (`atd update -y` skips the
  prompt). Restart `atd` to run it. A daemon that is already running keeps the
  old build until it restarts.
- **Re-run the installer:** the install command below installs the latest tagged
  release over the existing one. Pin a version with `AGENTTUNNEL_VERSION=vX.Y.Z`.

### Uninstall

```
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh -s -- --uninstall
```

From a checkout, run `sh agenttunnel/scripts/install.sh --uninstall`. Quit
AgentTunnel and any running `atd` first; deleting files does not stop them.

This removes `AgentTunnel.app` (from `~/Applications`, or `/Applications` when the
installer did not use `AGENTTUNNEL_APP_DIR`) and the `atd`, `agenttunnel`, and
`agenttunnel-desktop` commands from `~/.local/bin` (or `AGENTTUNNEL_INSTALL_DIR`).

Your threads and settings in `~/.agenttunnel` are kept. To delete them too, which
cannot be undone:

```
rm -rf ~/.agenttunnel
```

## Build from source

Requires Go (see `agenttunnel/go.mod`) and Node for the desktop frontend.

```
cd agenttunnel
go build ./...
./desktop/build.sh   # desktop app: frontend + linked binary in desktop/bin/
```

Architecture, the tool protocol, and how to hack on it: [agenttunnel/README.md](agenttunnel/README.md).

## License

MIT
