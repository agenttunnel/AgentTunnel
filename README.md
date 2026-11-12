<p align="center">
  <img src="agenttunnel-go/desktop/frontend/public/brand-mark.svg" width="56" alt="AgentTunnel" />
</p>

# AgentTunnel

AgentTunnel is a minimal GUI for coding agents: a multi-agent coding workflow
where a planning **orchestrator** breaks a task into jobs and dispatches them to
**planner / executor / tester / reviewer** subagents, each of which can run on
any provider CLI (Codex, Claude, Cursor, OpenCode, Pi, Command Code).

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
cd agenttunnel-go/desktop
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

## Build from source

Requires Go (see `agenttunnel-go/go.mod`) and Node for the desktop frontend.

```
cd agenttunnel-go
go build ./...
./desktop/build.sh   # desktop app: frontend + linked binary in desktop/bin/
```

Architecture, the tool protocol, and how to hack on it: [agenttunnel-go/README.md](agenttunnel-go/README.md).

## License

MIT
