# AgentTunnel remote execution (v1)

How to run the agent work on a Linux server so the laptop can be off. Shared
contract for `install.sh --server` (provisioning), `atd` under systemd (the
daemon), Caddy (TLS), the `agenttunnel` CLI's saved remotes, and the desktop
app's remote connection. Nothing here adds a second protocol: a remote client
speaks the same daemon JSON-RPC (`/ws` in `internal/transport`) as a local one.

## Goal and user workflow (decided)

The user keeps AgentTunnel — the `agenttunnel` CLI or the desktop app — on the
laptop, and runs the actual work on a remote Linux server:

1. Once: install and start `atd` on the server, log each provider CLI in,
   clone the repos, let Caddy put TLS in front.
2. Every day: open the desktop app (or run `agenttunnel`) from the laptop. The
   client connects over `wss` to the server's daemon; threads, agents, and
   files live there.
3. The laptop closes. Turns keep running on the server; the client reconnects
   later and re-reads thread state. The daemon is the source of truth.

This is the complementary shape to the relay (see `docs/relay-protocol.md`):
the relay answers "a phone reaches a daemon with no inbound port"; remote
execution answers "the daemon should not be on my laptop at all".

## Model

```
laptop                          server (Linux VPS)
─────                           ──────────────────
agenttunnel CLI ──┐
                  ├── wss ──▶ Caddy (TLS, :443) ──▶ atd 127.0.0.1:7331
desktop app ──────┘                                   │ systemd, user agenttunnel
                                                      │
                                    StateDir (/var/lib/agenttunnel)
                                      token (0600), thread logs, settings
                                      provider CLI logins (HOME of the
                                      service user), cloned repos
```

- `atd` binds `127.0.0.1:7331` only. It is never exposed directly; Caddy
  terminates TLS and proxies to loopback.
- The **StateDir holds durable thread state** (`<state dir>/threads`, settings,
  the token file). Restarting the service, or rebooting the box, rehydrates
  every thread from its event log.
- **Provider CLIs are installed and logged in once on the server.** The daemon
  spawns harness children on the server; their credentials live under the
  service user's home, which is the state dir. `claude`, `codex`, `opencode`
  (and any other supported harness) each authenticate one time during setup.
- **Repos are cloned over SSH on the server** (into
  `<state dir>/workspace` or wherever you keep them) and registered as
  projects by path. Paths are resolved on the daemon, not the client.

### The desktop's loopback proxy

A browser WebView cannot set an `Authorization` header on a WebSocket, and the
daemon accepts `?token=` only from loopback peers — so the desktop hides both
facts behind a proxy instead of weakening either:

1. The Go shell (not the WebView) listens on a random `127.0.0.1` port and runs
   a reverse proxy in front of the remote daemon. It forwards the daemon's
   HTTP surface — the `/ws` JSON-RPC socket and endpoints like `/conn` — to
   the remote URL, **adding `Authorization: Bearer <token>`** to every
   forwarded request.
2. The WebView gets only the proxy's loopback address and a per-run random
   local secret. The remote token stays in the Go process; it never reaches
   the frontend, its URL bar, or devtools.
3. The proxy rejects any request that does not present that local secret
   (bearer, or `?token=` for the WebSocket the WebView opens — the peer is
   loopback, and the proxy deletes the query parameter before forwarding), so
   other local processes and rebound web pages cannot use the remote
   credentials.

The CLI does not need this: it opens its own sockets and sends the
`Authorization` header directly.

## Server setup

Every command below is what `install.sh --server` and the unit it writes
actually do; the script prints the same steps when it finishes.

```bash
# 1. One-time install: atd + agenttunnel into /usr/local/bin, a system user,
#    a state dir, a systemd unit, and a sample Caddyfile. Nothing is started.
curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh \
  | sudo sh -s -- --server

# 2. Start and enable the daemon (systemd unit: /etc/systemd/system/atd.service).
sudo systemctl daemon-reload && sudo systemctl enable --now atd

# 3. Read the token (create-on-first-start, mode 0600):
sudo cat /var/lib/agenttunnel/token
```

What the install did, and its knobs:

| Thing | Default | Override |
|---|---|---|
| binaries (`atd`, `agenttunnel`) | `/usr/local/bin` | `AGENTTUNNEL_INSTALL_DIR` |
| state dir | `/var/lib/agenttunnel` | `AGENTTUNNEL_STATE_DIR` |
| service user | `agenttunnel` | `AGENTTUNNEL_USER` |
| systemd unit | `/etc/systemd/system/atd.service` | `AGENTTUNNEL_SYSTEMD_DIR` |
| sample Caddyfile | `/etc/agenttunnel/Caddyfile.sample` | `AGENTTUNNEL_CONFIG_DIR` |

`--server` is Linux only. The unit runs, as the service user:

```
atd --host 127.0.0.1 --port 7331 --home-dir /var/lib/agenttunnel --workdir /var/lib/agenttunnel/workspace
```

with `Restart=on-failure`. The state dir is `0700`, owned by the service user;
the token file `<state dir>/token` is created `0600` on first start and is
reused after that, so saved clients survive reboots.

**Provider logins.** Over an interactive SSH session (use `ssh -t` when
sudo/2FA needs a TTY), log each provider CLI in once as the service user —
its `HOME` is the state dir, so credentials persist next to the thread data:

```bash
ssh -t you@server
sudo -u agenttunnel claude        # complete the login, then exit
sudo -u agenttunnel codex         # likewise
sudo -u agenttunnel opencode      # likewise
```

Install the CLIs somewhere on the unit's `PATH` (`/usr/local/bin` is safe;
`~/.local/bin` is not on a systemd service's `PATH`).

**Repos.** Give the server its own SSH key (a deploy key is enough), clone, and
register as a project by its path on the server:

```bash
sudo -u agenttunnel git clone git@github.com:you/your-repo.git /var/lib/agenttunnel/workspace/your-repo
```

Then add the project from the client (below). The daemon resolves the path on
the server; it can pull over SSH itself thereafter.

**TLS (Caddy).** Point a DNS name at the server, install Caddy, copy
`/etc/agenttunnel/Caddyfile.sample` into your Caddy config (usually
`/etc/caddy/Caddyfile`), replace the host name, and reload. The sample is the
whole proxy:

```
atd.example.com {
	reverse_proxy 127.0.0.1:7331
}
```

Caddy obtains and renews the certificate itself. The sample also carries a
commented log filter that redacts `?token=` from URIs — it is unverified
against a Caddy binary; run `caddy validate` before relying on it. Plain
`ws`/`http` is never used off loopback, so the bearer token never crosses the
network in clear text.

## Client setup

### CLI

```bash
agenttunnel remote add my-server wss://atd.example.com/ws --token-stdin
# paste the token from `sudo cat /var/lib/agenttunnel/token`, then Enter
agenttunnel remote use my-server
agenttunnel remote ping my-server
```

- `remote add` requires `wss://` for any non-loopback host (plain `ws://` is
  loopback-only) and the URL names the daemon's `/ws` path. A name starts
  with a letter or digit and continues with letters, digits, `.`, `_`, `-`.
- `--token-stdin` reads the token from stdin, so it never lands in shell
  history or `ps` argv. Without it the profile is saved without a token.
- Profiles live in `<user config dir>/agenttunnel/remotes.json` (`~/.config/
  agenttunnel/` on Linux, `~/Library/Application Support/agenttunnel/` on
  macOS), written `0600`; the token is stored there and never printed (`remote
  list` shows whether one is saved, not what it is).
- `remote use <name>` makes the profile the default target. `remote ping
  [<name>]` dials the remote (never a local daemon), prints its version and
  round-trip latency, and doubles as a health check.
- Connection resolution order for every CLI command: `--daemon <wss-url>`,
  `AT_DAEMON_URL` (plus `AT_TOKEN`), the active saved profile, then the local
  daemon (autostarted). A remote target never autostarts anything.
- Other subcommands: `remote list`, `remote rm <name>`. A one-off target also
  works without a profile: `agenttunnel --daemon wss://atd.example.com/ws …`
  (the profile with a matching URL supplies its token).

### Desktop

Settings → **Daemon connection**:

- **Name** (optional), **Address** (`https://atd.example.com`; `ws://`,`wss://`
  are normalized to their `http(s)` base; plain `http` is loopback-only),
  **Token** (typed; shown as a password field).
- **Connect** verifies the daemon and token (one authenticated `project.list`
  round trip), saves the remote to `<state dir>/desktop-prefs.json`
  (`~/.agenttunnel/desktop-prefs.json`, mode `0600`), and swaps the window to
  the loopback proxy. The window reloads and shows
  “Connected to *name* · url”.
- While a remote is saved, launching the app drives that daemon and starts no
  local one; opening a project by argument is ignored with a note, since that
  needs the local daemon.
- **Disconnect** forgets the remote. The window returns to the local daemon;
  when the launch had no local daemon (a pure remote launch), the app asks
  for a restart instead.
- If the daemon is unreachable at launch, the spinner screen offers
  “Forget this remote daemon”.

## Authentication: never in argv, logs, or a URL

The daemon's auth is one bearer token. 128-bit random hex, generated once,
stable across restarts.

- **Server:** the token resolves from `--token`, `--token-file`,
  `AT_DAEMON_TOKEN`, or `<state dir>/token` (created `0600`). `--token` is
  discouraged — argv is visible to `ps`; the flag help says so. The daemon
  never logs the value; it logs the file's path and stops there.
- **On the wire:** `Authorization: Bearer <token>` is accepted from any peer.
  `?token=` is accepted **only from loopback peers**, because query strings
  end up in proxy and access logs. A TLS proxy like Caddy connects from
  loopback, which is exactly why the desktop's browser-style WebSocket still
  authenticates — through its own local proxy, which then sends the header.
- **CLI:** `Dial` sends the token as a Bearer header and never in the URL.
  Profiles (`remotes.json`) and the file's directory are `0600`/`0700`.
- **Desktop:** the remote token never reaches the WebView. It lives in the Go
  process, is attached by the loopback proxy's forwarder, and is persisted in
  `desktop-prefs.json` (`0600`, atomic rename). The frontend sees only the
  proxy address, a per-run local secret, and the remote's name and URL. The
  `at:connection` handoff carries the remote's name and URL — not its token.
- All comparisons are constant time.

## Where to host it (November 2026)

**Default: a small Hetzner VPS.** Roughly 4–6 EUR/mo covers a VM that runs a
daemon plus a few concurrent harness CLIs. Two caveats at order time: prices
rose during 2026 (check current pricing), and the cheapest CX line is
intermittently out of stock — if it is, the next size up is the pragmatic
answer. Verify both on Hetzner's site before committing.

**Free option: Oracle Cloud Always Free A1.** The A1 allowance was cut to
2 OCPU / 12 GB in August 2026, still enough for this workload — but capacity
is often unavailable in the popular regions, so treat it as
*free-if-provisionable*: worth trying, not worth waiting for.

**IONOS.** Technically equal for this purpose — root Linux, persistent NVMe
disk, unlimited traffic at 1 Gbit/s, IPv4+IPv6, and a Spain datacenter you
can select at order time, with Spanish-language support. Financially worse:
the 2 EUR/mo headline is a 3-month promotional price on a 12-month minimum
term with a 10 EUR setup fee. Steady state is about 5 EUR/mo excl. IVA for
1 vCore / 2 GB, and 12 EUR/mo for 2 vCPU / 4 GB, versus Hetzner's roughly
4–6 EUR. At equal specs IONOS is worse value — choose it only for the Spain
datacenter or the Spanish support, and then order the 1-month term (it is
selectable) so the promo structure does not lock you in.

**RunPod (or any stop-when-idle pod host).** Not cheaper even part-time,
and its model fights this design. Pods are containers, not VMs — no systemd,
no Docker Compose; `atd` would have to run as the container entrypoint.
Stopping a pod erases its container disk (the CLIs, their logins, everything
not on a volume), volume-disk storage bills at double the running rate while
stopped, and only network volumes survive both stop and terminate, at
$0.07/GB/mo — roughly $3.50/mo for a 50 GB volume before any compute.
Billing is per-second on demand (savings plans are discontinued); the
cheapest published rate is a $0.16/hr GPU pod, CPU-only pods exist but
publish no rate, and there is no built-in auto-stop. Inbound HTTP goes
through a Cloudflare proxy with a 100-second timeout — wrong for a
long-lived `wss` — and TCP exposure gives a public IP whose external port
mapping rotates whenever the pod resets, so every restart re-points saved
CLI profiles and the desktop connection. Even one hour of use a day costs
more than Hetzner's flat price. RunPod's lane is per-thread GPU execution
sandboxes later, not the daemon.

**Rejected for daemon hosting:**

- **Cloud Run** — no persistent disk (thread state would need to live
  elsewhere), and CPU is throttled to near-zero outside requests, which is
  the opposite of a standing daemon supervising subprocesses.
- **Modal, E2B, Daytona** — ephemeral sandbox platforms. They are built for
  short-lived code-execution jobs, not for a long-lived stateful daemon with
  logins and a filesystem.

Anything that can run systemd, keep a disk, and accept inbound TLS is
otherwise fine: a home server, a Pi, a spare box at the office. The relay
(`docs/relay-protocol.md`) covers reaching such a daemon from a phone.

## Limitations (deliberate cuts, v1)

- **No automated deploy or pairing.** `install.sh --server` writes files;
  it does not log into providers, clone repos, obtain DNS, or fetch a token
  into your client. Every one of those steps is a human step above.
- **Provider logins are manual.** Each CLI authenticates once over an
  interactive SSH session (`ssh -t`), as the service user; there is no
  delegated or browser-forwarded login.
- **Repos are cloned manually**, by you, with your own SSH keys on the
  server. The daemon never imports a local checkout.
- **No sleep/wake, no cost controls.** The server runs and bills around the
  clock. Stopping the instance stops the daemon; there is no suspend/resume
  of in-flight turns and no budget gate on provider spend. A stop-when-idle
  host does not fix this — see RunPod under hosting.
- **Worktrees assume git on the server.** Per-thread worktrees are cut from
  the project's repo on the daemon's machine; `git` must be installed there,
  and clones must be real clones (SSH), not mounted folders.
- **The desktop's “Add project” picker browses the laptop**, and the chosen
  path is resolved on the daemon. That works when both machines agree on the
  path layout; otherwise add the project by typing the server-side path (the
  typed-path sheet) instead.
- **One remote at a time per client.** The CLI has an active profile; the
  desktop one saved remote per install. Connecting to several daemons means
  several profiles you switch between, or several machines.

## As built (v1)

- `cmd/atd/token.go` — token resolution and the stable `0600` token file;
  the daemon prints `atd token file: <path> (the token is not logged)`.
- `scripts/install.sh --server` — headless install, system user, state dir,
  `atd.service`, `Caddyfile.sample`; prints next steps, starts nothing.
- `scripts/cloud/atd.service`, `scripts/cloud/Caddyfile` — the same unit and
  Caddyfile as the script writes, kept as reference copies in the repo.
- `internal/cli/remote.go`, `internal/cli/remote_profiles.go` — `remote
  add|use|list|rm|ping`, `remotes.json` (0600), target resolution for the
  whole CLI.
- `desktop/remote.go` — the loopback proxy, the local-secret guard,
  connect/disconnect events, and the remote preference in
  `desktop-prefs.json` (`desktop/attach.go`).

Not built in v1, on purpose, and the limits above are the user-visible cost:
no provisioning automation, no provider-login forwarding, no wake-on-demand,
no per-turn sandboxing of the server account (the daemon and its harnesses
run as the service user, with that user's credentials). If any of that
matters to you, keep the daemon on a box you trust with your provider
logins.
