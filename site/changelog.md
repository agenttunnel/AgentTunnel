---
title: Changelog
layout: default
nav_order: last
---

# Changelog

Tagged releases (`vX.Y.Z`) are the stable builds. `main-<sha>` prereleases are not listed here.

## Unreleased

No changes since v0.44.0.

## v0.44.0 (2026-11-23)

### Features

- desktop: thinking level pickers for roles, leads, and solo [(#228)](https://github.com/agenttunnel/AgentTunnel/pull/228)
- core: a team's leads speak at once, as a council [(#229)](https://github.com/agenttunnel/AgentTunnel/pull/229)
- tui: ctrl+b toggles the sidebar and streaming stays cheap [(#253)](https://github.com/agenttunnel/AgentTunnel/pull/253)
- install: install.sh registers the atd service and removes it on uninstall [(#249)](https://github.com/agenttunnel/AgentTunnel/pull/249)
- desktop: auto-mode approvals are set per harness [(#256)](https://github.com/agenttunnel/AgentTunnel/pull/256)
- web: the web client ships a PWA manifest [(#243)](https://github.com/agenttunnel/AgentTunnel/pull/243)
- cli: daemon install|uninstall|status registers atd as a service [(#248)](https://github.com/agenttunnel/AgentTunnel/pull/248)
- daemon: atd yields to a live daemon and rotates its own log [(#247)](https://github.com/agenttunnel/AgentTunnel/pull/247)
- cli: run --wait --json returns a structured turn result [(#250)](https://github.com/agenttunnel/AgentTunnel/pull/250)

### Fixes

- core: a stopped agent's process group is swept until its pipes drain [(#252)](https://github.com/agenttunnel/AgentTunnel/pull/252)
- core: auto-mode model approvals are scoped per harness [(#246)](https://github.com/agenttunnel/AgentTunnel/pull/246)
- desktop: review diff refreshes on turn end, with a reconnect banner [(#245)](https://github.com/agenttunnel/AgentTunnel/pull/245)
- desktop: dialogs trap Tab, empty states explain, motion respects reduced-motion [(#255)](https://github.com/agenttunnel/AgentTunnel/pull/255)
- desktop: dialogs declare roles and manage focus [(#244)](https://github.com/agenttunnel/AgentTunnel/pull/244)
- web: attached proxy applies the cross-site gate to prefs and attachments [(#242)](https://github.com/agenttunnel/AgentTunnel/pull/242)
- tui: failures after redial, turn start, and file search are surfaced [(#240)](https://github.com/agenttunnel/AgentTunnel/pull/240)
- core: subscribing to a thread twice no longer duplicates live frames [(#239)](https://github.com/agenttunnel/AgentTunnel/pull/239)
- cli: atd autostart output is kept, and --json errors are structured [(#238)](https://github.com/agenttunnel/AgentTunnel/pull/238)
- cli: a slow detached start is not reported as no turn [(#251)](https://github.com/agenttunnel/AgentTunnel/pull/251)
- cli: headless follow exits non-zero when a turn fails [(#237)](https://github.com/agenttunnel/AgentTunnel/pull/237)

### Performance

- desktop: the graph feed grows its event log in place [(#254)](https://github.com/agenttunnel/AgentTunnel/pull/254)
- update: update checks share one memoized lookup [(#241)](https://github.com/agenttunnel/AgentTunnel/pull/241)

## v0.43.0 (2026-11-20)

### Features

- cli: web and desktop are commands, not flags [(#225)](https://github.com/agenttunnel/AgentTunnel/pull/225)
- desktop: modernize the web and desktop UI around one clear hierarchy [(#224)](https://github.com/agenttunnel/AgentTunnel/pull/224)
- cli: TUI thread view reads as a conversation, with a real composer [(#222)](https://github.com/agenttunnel/AgentTunnel/pull/222)

### Fixes

- build: desktop go.sum covers the Windows notification and dock services [(#227)](https://github.com/agenttunnel/AgentTunnel/pull/227)
- desktop: Escape closes overlays, panel drawers, and live-turn bugs [(#226)](https://github.com/agenttunnel/AgentTunnel/pull/226)

### Performance

- desktop: fold events, reconnect, stream replies, tool cards, web routing and notifications [(#223)](https://github.com/agenttunnel/AgentTunnel/pull/223)

## v0.42.0 (2026-11-15)

### Features

- core: preferences persist in the daemon, shared by desktop and web [(#216)](https://github.com/agenttunnel/AgentTunnel/pull/216)
- cli: run the TUI on Bubble Tea with an opencode-style look [(#219)](https://github.com/agenttunnel/AgentTunnel/pull/219)
- desktop: add /vim for Vimium-style keyboard control [(#221)](https://github.com/agenttunnel/AgentTunnel/pull/221)
- desktop: add Option+Tab shortcut and fuzzy search to All threads [(#213)](https://github.com/agenttunnel/AgentTunnel/pull/213)

### Fixes

- desktop: scroll-to-top pill no longer covered by messages [(#217)](https://github.com/agenttunnel/AgentTunnel/pull/217)
- update: detect external updates so the title bar button matches reality [(#214)](https://github.com/agenttunnel/AgentTunnel/pull/214)
- desktop: show offline when the daemon connection drops [(#215)](https://github.com/agenttunnel/AgentTunnel/pull/215)

### Other

- refactor(repo): rename agenttunnel-go directory to agenttunnel [(#220)](https://github.com/agenttunnel/AgentTunnel/pull/220)
- docs(installer): document update and uninstall, add --uninstall [(#218)](https://github.com/agenttunnel/AgentTunnel/pull/218)

## v0.41.0 (2026-11-15)

### Features

- desktop: add a compact composer view on Cmd+S [(#211)](https://github.com/agenttunnel/AgentTunnel/pull/211)
- desktop: give worktrees their own page with a daily prune prompt [(#212)](https://github.com/agenttunnel/AgentTunnel/pull/212)
- desktop: add project threads cleanup button
- desktop: files editor moves to a center tab beside Review
- desktop: log box gets jump-to-top and jump-to-bottom pills
- cli: register app-level TUI views
- cli: themed TUI foundation with registry and thread operations

### Fixes

- relay: StartTunnel only reports connected for its own relay socket
- desktop: copy button confirms its click and works in the shell
- desktop: turns always outlive the window, drop the run-in-background toggle
- cli: drop stale background folds in PR and issues views
- cli: make the TUI responsive, legible, and safe under load

## v0.40.0 (2026-11-15)

### Features

- desktop: add sidebar button for the all-threads super view
- desktop: add selectable color themes

### Fixes

- desktop: render thread menu delete as a danger item, not a filled button

## v0.39.0 (2026-11-14)

### Features

- desktop: master chat harness/model picker, history rail, reporter toggle
- core: master orchestrator runtime selection, durable history, reporter mode
- desktop: super view of all threads across projects, running vs unseen
- core: track when a thread was last seen and flag finished-unseen threads

## v0.38.0 (2026-11-14)

### Features

- cli: agenttunnel tui command and cross-client visibility test
- cli: pure TUI screen model and renderer
- cli: terminal key decoder and raw-mode wrapper for the TUI
- desktop: new-thread setup wizard and animated ring on scheduled rows
- desktop: add per-thread P2P chat tab for agent-to-agent messages
- cli: whatsapp connect draws the pairing QR in the terminal
- cli: agenttunnel --web `<dir>` serves the web UI on localhost
- web: add internal/webui to serve the daemon's embedded web UI
- desktop: frontend works as a plain browser client of the daemon

### Fixes

- desktop: solo thread rows get the agent's harness tint
- cli: reject non-loopback Host headers on the web UI surface
- desktop: browser-mode discovery fails loudly instead of hanging

### Other

- build: mirror the frontend SPA into internal/webui dist

## v0.37.0 (2026-11-13)

### Features

- orchestrator: lead can revive a finished subagent via tool revive
- desktop: solo mode setup and harness-tinted threads
- transport: thread.setSolo RPC and solo review guard
- orchestrator: single-agent solo turn path
- domain: solo runtime state with multi-lead normalization
- contracts: add thread.solo.set command and SoloRuntimeSet event
- desktop: collapsible prompt responses in thread chat
- orchestrator: lead can read a subagent's full log via agent-logs tool
- desktop: jump-to-top and per-message prev/next navigation in chat
- desktop: show the /btw side conversation with follow-ups
- orchestrator: /btw side questions continue a per-thread side conversation

### Fixes

- transport: /tool/call no longer drops the To field on queued tool kinds
- threads: thread.list orders by submission time, not activity
- desktop: composer surface spans full chat pane width
- transport: archiving a running thread says to stop it first

## v0.36.0 (2026-11-13)

### Features

- orchestrator: experimental wildcard role defined by a lead's brief
- orchestrator: lead knows which roles it may dispatch
- cli: open accepts a project directory
- orchestrator: thread-relaunch and thread-results agent tools
- daemon: schedule threads to trigger once their dependencies finish
- desktop: schedule UI, waiting column, and controlled-thread links
- core: thread scheduling domain model

### Fixes

- cli: accept websocket frames over the 32 KiB default
- installer: link agenttunnel-desktop at its renamed bundle path
- orchestrator: relaunched turns now run, and control is released when they end
- core: archived threads can no longer be scheduled
- transport: /tool/call no longer drops the target thread
- desktop: settings trigger always visible in the titlebar, sheet mounts on every screen

## v0.35.0 (2026-11-12)

### Features

- desktop: mark orchestrator-started threads in the UI
- transport: creation sites decide the thread owner, list surfaces it
- core: record a thread owner through contracts and the event core

### Fixes

- desktop: project board no longer goes black when issues are unavailable

## v0.34.0 (2026-11-12)

### Features

- desktop: paste, drop, preview and send images in the composer
- core: images attach to turns and reach harnesses that take them
- remote: /btw command forwards questions to the selected thread
- desktop: tint graph nodes by harness and show model
- desktop: branch icon on each message forks the thread
- core: thread.fork branches a thread up to a message
- cli: --desktop flag to open the desktop app on a path
- cli: open a thread in the desktop app
- cli: describe and inspect for threads and agents
- cli: fix attach, add agents, per-agent logs, ref resolution
- core: master sees thread detail and can control threads
- desktop: arrow up/down recalls previous prompts in the composer
- desktop: Draft and GitLab Issues columns on the thread board [(#168)](https://github.com/agenttunnel/AgentTunnel/pull/168)
- remote: let several instances share one WhatsApp account [(#167)](https://github.com/agenttunnel/AgentTunnel/pull/167)
- remote: send periodic status reports through the WhatsApp tunnel
- desktop: cheat sheet on every launch with opt-out [(#166)](https://github.com/agenttunnel/AgentTunnel/pull/166)

### Fixes

- providers: drop stale grok references left by the main rework merge
- core: engine close joins its worker so no event can append after shutdown
- core: retitle threads at turn start and after the cycle

## v0.33.0 (2026-11-12)

### Features

- desktop: markdown viewer and clickable .md links in threads [(#159)](https://github.com/agenttunnel/AgentTunnel/pull/159)
- core: richer periodic status report (done, to do, worth knowing) [(#165)](https://github.com/agenttunnel/AgentTunnel/pull/165)
- update: show release notes in a modal after an update installs [(#163)](https://github.com/agenttunnel/AgentTunnel/pull/163)
- core: lead can list/delete threads and use read-file, write-file, shell tools [(#161)](https://github.com/agenttunnel/AgentTunnel/pull/161)
- desktop: Cmd/Ctrl+P command palette [(#160)](https://github.com/agenttunnel/AgentTunnel/pull/160)
- desktop: file editor with lazy file tree [(#158)](https://github.com/agenttunnel/AgentTunnel/pull/158)

### Fixes

- desktop: roles card shows off/last status instead of always idle [(#164)](https://github.com/agenttunnel/AgentTunnel/pull/164)
- core: steer sent during the last pass is weighed before the turn ends [(#162)](https://github.com/agenttunnel/AgentTunnel/pull/162)

## v0.32.0 (2026-11-12)

### Features

- desktop: jump from a thread to its project board
- core: record steering injection and show queued/injected in the graph
- core: periodic workflow status report with configurable period

### Fixes

- desktop: graph no longer vanishes when scrubbing before the first spawn

## v0.31.0 (2026-11-12)

### Features

- cli: rename the at CLI to agenttunnel and the desktop binary to agenttunnel-desktop [(#154)](https://github.com/agenttunnel/AgentTunnel/pull/154)
- remote: talk to the Master orchestrator from WhatsApp [(#153)](https://github.com/agenttunnel/AgentTunnel/pull/153)
- desktop: agent questions read cleanly in the chat [(#150)](https://github.com/agenttunnel/AgentTunnel/pull/150)

### Fixes

- desktop: no empty-screen flash when switching threads [(#152)](https://github.com/agenttunnel/AgentTunnel/pull/152)
- desktop: remove duplicate Board button from project header [(#151)](https://github.com/agenttunnel/AgentTunnel/pull/151)

## v0.30.1 (2026-11-12)

### Fixes

- desktop: @ mention menu scrolls to the highlighted row and wraps correctly

## v0.30.0 (2026-11-12)

### Features

- desktop: attach to running daemon, keep-running toggle, background turns
- cli: add job commands run, ps, logs, attach, stop
- cli: add usage, models, and harness readouts
- cli: add job templates and projects commands
- cli: add 'at' CLI foundation: discovery, autostart, detached turns, role runtimes
- desktop: About section in settings with version and developer link
- core: app.info RPC exposes the installed version
- limits: Command Code usage tracking and collapsible limit blocks
- remote: control the orchestrator from your WhatsApp self-chat

### Fixes

- cli: resolve the five must-fix review findings on the at CLI

### Other

- docs(cli): add 'at' CLI design doc and effort estimate

## v0.29.0 (2026-11-11)

### Features

- desktop: collapsible logs box and full tool-call params in logs
- core: mark steering messages consumed and style queued ones as pending
- core: configurable master orchestrator harness and model

## v0.28.0 (2026-11-11)

### Features

- core: rank teams by how well they resolve tasks
- core: time-per-task stats for each harness and model
- core: leaderboard of harness and model usage
- desktop: move harness stats to their own Stats page
- desktop: give the master orchestrator its own page

### Fixes

- desktop: remove the phone tunnel from the title bar

## v0.27.0 (2026-11-11)

### Features

- desktop: fuzzy search project threads by title and first prompt
- desktop: searchable multi-select for auto-mode approved models
- remote: link a WhatsApp device by QR
- remote: WhatsApp workflow updates and Remote sidebar section

## v0.26.0 (2026-11-11)

### Features

- core: prompt to update harnesses that have a newer release
- desktop: add a visible Board button to the project header
- core: orchestrator commits each job separately
- core: master orchestrator chat on the home screen
- settings: check the forge CLI versions on PATH
- scm: GitLab merge requests in the PRs tab
- desktop: collapse a thread's subthreads in the sidebar

### Fixes

- providers: show Claude usage credits for enterprise accounts
- desktop: AirDrop-style ripple when creating a graph group
- desktop: pressing Start dismisses the new-thread setup view
- board: select cards with Cmd or Shift click, not a checkbox

## v0.25.0 (2026-11-11)

### Features

- board: archive and delete threads, select several at once

## v0.24.0 (2026-11-11)

### Features

- desktop: phone tunnel in the title bar; check for updates every 5 minutes
- desktop: compact thread setup with a Start gate

### Fixes

- providers: command code resolves from the app PATH; models come from discovery only

## v0.23.0 (2026-11-11)

### Features

- core: lead can steer a subagent's next run
- desktop: thread setup screen before the first run [(#115)](https://github.com/agenttunnel/AgentTunnel/pull/115)
- core: experimental context manager role and per-role disable
- core: experimental context manager and per-role enable toggles
- core: threads can run any number of leads [(#110)](https://github.com/agenttunnel/AgentTunnel/pull/110)
- desktop: thread board shows threads by run and PR state [(#111)](https://github.com/agenttunnel/AgentTunnel/pull/111)
- desktop: Pull Request view tracks every PR a thread opens
- core: threads can run any number of leads
- contextstore: seed auto-mode model selection guidance
- core: reviewer comment threads with executor replies and re-check [(#106)](https://github.com/agenttunnel/AgentTunnel/pull/106)
- desktop: group graph agents and ask them together [(#105)](https://github.com/agenttunnel/AgentTunnel/pull/105)
- desktop: start the phone tunnel from the app [(#103)](https://github.com/agenttunnel/AgentTunnel/pull/103)
- desktop: voice dictation replaces the composer with a recording animation [(#101)](https://github.com/agenttunnel/AgentTunnel/pull/101)
- desktop: start the phone tunnel from the app, no CLI needed
- relay: pair a phone over the LAN by scanning a QR code

### Fixes

- orchestrator: lead sees the thread's earlier conversation
- desktop: slash menu follows the highlighted command
- desktop: hold-to-talk keeps recording while Command+T is held
- desktop: Cmd+T dictation keeps listening while the keys are held
- providers: Command Code detection and auto-mode model picks [(#107)](https://github.com/agenttunnel/AgentTunnel/pull/107)
- domain: thread snapshots no longer share lead and task slices [(#108)](https://github.com/agenttunnel/AgentTunnel/pull/108)
- desktop: Model config opens the right pane when it is collapsed [(#109)](https://github.com/agenttunnel/AgentTunnel/pull/109)
- desktop: Model config opens a collapsed right pane
- core: thread snapshots no longer share lead and task slices
- orchestrator: auto mode picks any harness and model without role carry-over
- providers: Command Code driver looks up the commandcode binary
- desktop: external links open in the default browser [(#102)](https://github.com/agenttunnel/AgentTunnel/pull/102)
- desktop: external links open in the default browser

### Other

- Usage meter [(#104)](https://github.com/agenttunnel/AgentTunnel/pull/104)

## v0.22.0 (2026-11-10)

### Features

- core: /btw side questions and desktop slash commands [(#99)](https://github.com/agenttunnel/AgentTunnel/pull/99)
- desktop: PR tab commits, pushes, pulls, and opens or merges the PR
- desktop: review comments can span a range of diff lines
- desktop: show a project's worktrees on its own screen [(#98)](https://github.com/agenttunnel/AgentTunnel/pull/98)
- core: agents can ask the user questions [(#97)](https://github.com/agenttunnel/AgentTunnel/pull/97)
- desktop: @ in the composer attaches files and folders to a turn [(#96)](https://github.com/agenttunnel/AgentTunnel/pull/96)
- orchestrator: only the lead changes git state [(#94)](https://github.com/agenttunnel/AgentTunnel/pull/94)
- mobile: add iOS build-then-PR script, dry-run by default
- mobile: editable device name on Pair
- mobile: chat shows when the computer is offline
- mobile: threads show when the computer is offline
- mobile: daemon hook tracks presence and re-reads when it returns
- mobile: relay client reports daemon presence and cleans device names
- mobile: re-read when the relay asks for a resync
- mobile: pair through atrelay and keep the token out of URLs
- desktop: pair phones from Settings through the relay
- atd: --relay flag enables the outbound relay
- relay: daemon outbound client, off by default
- relay: tell the daemon when a phone's socket closes
- relay: atrelay router with pairing, rate limits, and revoke
- transport: relay pairing RPCs and virtual sessions
- settings: relay config, off by default
- relay: opt-in -trust-proxy-header for per-IP limits
- relay: constants for instance cap and daemon hello limit
- mobile: Expo client with threads, chat, and settings

### Fixes

- core: harness stats no longer list leads and dispatches as unknown [(#100)](https://github.com/agenttunnel/AgentTunnel/pull/100)
- subthread: report finished subthreads and stop offering a full tool [(#95)](https://github.com/agenttunnel/AgentTunnel/pull/95)
- providers: command-code runs with --yolo and reads its JSON output [(#93)](https://github.com/agenttunnel/AgentTunnel/pull/93)
- mobile: fail clearly when the active Swift is older than 6.2
- mobile: require --title for iOS PRs and warn on extra commits
- mobile: build the iOS workspace after prebuild installs pods
- relay: refuse `relay.*` RPCs on relayed sessions
- desktop: pairing QR label says how the phone takes the code
- atd: stop printing the token URL when bound off loopback
- transport: refuse ?token= auth from non-loopback peers
- relay: cap instances, rate-limit daemon hellos, write state off the lock

### Other

- ci(ios): build on macos-26 with Xcode 26.4.1
- ci(ios): pin Xcode 26.2 because 26.3 rejects expo-modules-jsi
- docs(mobile): require Xcode 26 and Swift 6.2 for the iOS build
- ci(ios): select the newest Xcode 26 before the simulator build
- docs(mobile): document workspace build and PR title rules
- ci(ios): drop the `feat/**` push trigger to stop duplicate macOS runs
- ci(mobile): build the iOS simulator app on mobile changes
- docs(mobile): document the iOS build and PR scripts
- build(mobile): add unsigned iOS simulator build script
- docs(relay): make protocol main text match the as-built relay
- docs(relay): document per-IP pair failures, hello limits, trusted proxy
- docs(relay): record as-built protocol additions and gaps
- docs(relay): publish relay protocol note

## v0.21.0 (2026-11-10)

### Features

- desktop: worktrees live in the threads view [(#91)](https://github.com/agenttunnel/AgentTunnel/pull/91)

### Fixes

- desktop: Enter steers a running turn instead of being swallowed [(#90)](https://github.com/agenttunnel/AgentTunnel/pull/90)

## v0.20.0 (2026-11-10)

### Features

- desktop: harness stats at app, project, and thread scope [(#89)](https://github.com/agenttunnel/AgentTunnel/pull/89)
- desktop: keep each thread's view, drafts, and errors separate [(#88)](https://github.com/agenttunnel/AgentTunnel/pull/88)
- desktop: scroll-to-latest button when the chat is scrolled up [(#87)](https://github.com/agenttunnel/AgentTunnel/pull/87)
- desktop: agent graph nodes can be dragged apart [(#86)](https://github.com/agenttunnel/AgentTunnel/pull/86)

## v0.19.0 (2026-11-10)

### Features

- core: decide worktree replace by ownership rules
- core: project worktree replace as one ref swap
- core: add worktree-replaced event
- core: add worktree replace command
- core: subthreads refresh an inherited worktree, detach clears an owned one
- core: a thread's worktree can be detached
- core: worktree RPCs and worktree-aware thread create, delete and diff
- core: subthreads inherit the parent worktree
- core: worktree attach, list, remove and prune for threads
- core: git worktree manager in sibling directories
- core: provisional threads keep the worktree ref until materialized
- core: decider attaches one worktree per top-level thread
- core: projector folds the worktree ref
- core: thread state carries its worktree
- core: worktree attach command
- core: thread.worktree-attached event and creation ref
- core: worktree ref type for threads
- desktop: worktrees aside tab lists each project's git worktrees

### Fixes

- desktop: worktrees are mandatory; drop the create-now toggle
- core: subthread spawn restores the parent worktree first
- core: failed worktree cuts retry every turn and audit once per reason
- core: worktree list bounds git work and skips git for non-repos
- desktop: relabel the worktree toggle to say what it does
- core: removed or vanished worktrees are restored before the next turn
- core: a declared worktree never falls back to the project checkout
- core: agents run in the thread's worktree
- desktop: drop redundant back button from threads sidebar [(#84)](https://github.com/agenttunnel/AgentTunnel/pull/84)
- desktop: model picker popover no longer spills out of the pane [(#85)](https://github.com/agenttunnel/AgentTunnel/pull/85)

### Other

- docs(agents): document per-thread worktrees as the default workflow

## v0.18.0 (2026-11-10)

### Features

- desktop: set the lead count and per-lead harnesses in the Models tab [(#82)](https://github.com/agenttunnel/AgentTunnel/pull/82)

## v0.17.0 (2026-11-10)

### Features

- desktop: council tab, enter-to-send, hold-to-talk dictation [(#83)](https://github.com/agenttunnel/AgentTunnel/pull/83)

## v0.16.2 (2026-11-10)

No user-facing commits listed.

## v0.16.1 (2026-11-10)

### Features

- desktop: resizable and collapsible side panels

### Fixes

- desktop: agent activity log uses the space while agents run

## v0.16.0 (2026-11-09)

### Features

- desktop: models panel, growing composer, collapsed pastes, turn blocks
- desktop: add projects and threads from a plus tile
- desktop: warp-style block UI
- core: subthreads run unrelated work in their own thread
- desktop: submit review comments to the team as a turn
- stats: show token usage and subagent counts per harness
- desktop: style composer in command mode
- update: install newer GitHub releases with checksum checks
- desktop: show the agreed plan as a checklist above the board
- core: leads record the agreed task list through the tool CLI
- desktop: team size picker and a task board by status
- orchestrator: run a team of leads that agree through peer messages
- orchestrator: name unnamed subagents docker-style, unique per thread
- scm: show PR/MR checks, review, and comments for the thread branch
- release: publish a prerelease for every merge to main

### Fixes

- desktop: raise faint text contrast to 4.5:1
- desktop: keep the roles panel live with each role's runtime and activity

## v0.15.0 (2026-11-09)

### Features

- desktop: run shell commands from the composer with !
- desktop: live and replayable agent graph

### Fixes

- desktop: restore typed command when thread.runCommand fails
- desktop: keep live log lines that arrive during hydration
- logstore: serve agent detail logs from the full thread history
- desktop: restore thread log box and scope composer drafts per thread
- logstore: keep each thread's output separate and restorable

### Performance

- desktop: step and seed the agent graph replay without refolding

## v0.14.0 (2026-11-09)

### Features

- desktop: keep multiple projects open in tabs

### Fixes

- providers: detect Claude token usage and cost
- desktop: make Model config usable for Command Code
- desktop: make project and thread cards respond to clicks anywhere

## v0.13.1 (2026-11-09)

### Fixes

- release: stop dropping the macOS app bundle and DMG assets
- desktop: make the macOS app bundle and DMG actually ship

## v0.13.0 (2026-11-09)

### Fixes

- desktop: make the macOS app bundle and DMG actually ship

## v0.12.0 (2026-11-08)

### Features

- desktop: minimalist UI refresh and AgentTunnel identity

## v0.11.0 (2026-11-08)

### Features

- desktop: mac app bundle, `agenttunnel <dir>` launching, and threads that save only when used

## v0.10.1 (2026-11-08)

### Fixes

- core: unsubscribing no longer panics a concurrent event publish

### Other

- docs(go): README for the Go-only repo
- chore: remove the deprecated TypeScript monorepo

## v0.10.0 (2026-11-07)

### Features

- go-core: subagent-proposed sub-spawns gated by orchestrator approval

## v0.9.0 (2026-11-07)

### Features

- go-desktop: agent graph view as a center tab
- go-desktop: graph view of the agent team
- go-core: deliver peer messages printed by subagents
- go-core: enable peer messaging by default and reach waiting agents
- go-core: persist task dependencies in the read model
- go-core: agents can message each other through the peer mailbox

### Fixes

- go-desktop: key graph conversation items by data, not index
- go-core: harden the peer mailbox after review
- go-desktop: label graph controls without native tooltips
- go-core: partition the peer mailbox by thread
- go-desktop: make message arcs keyboard reachable
- go-core: skip whitespace-only findings summaries
- go-core: let same-pass messages address protocol dispatches
- go-providers: keep line terminators on non-JSON harness output

### Other

- docs(go): describe agent-to-agent messaging

## v0.8.0 (2026-11-05)

### Features

- go-desktop: auto-mode runtime menu and board polish
- go-desktop: inherited context becomes visible and editable
- go-core: council of agents — parallel debate to a synthesized outcome
- go-core: stoppable turns and agents with real process-group teardown
- go-desktop: mark idle threads stale with a live activity timer
- go-core: parse printed tool commands and fix pi frame translation
- go-desktop: show thread update dates and sort by recency

### Fixes

- go-desktop: restore titlebar double-click to maximize

### Other

- docs: add the Go release install command to the README

## v0.7.0 (2026-11-05)

### Features

- go-desktop: tetris project/thread grid, thread menu, model config, git diffs
- web: curate auto-mode models per thread

### Fixes

- release: install GTK4 deps for the Go Linux build
- providers: list models for every workflow harness

### Other

- ci(release): build and publish the Go app
- agenttunnel in go

## v0.6.0 (2026-10-06)

### Features

- providers: add Command Code provider over ACP
- tui: theme from the terminal, follow Claude Code, add a context rail
- tui: greet with a welcome panel
- tui: usage with cost and cache, and per-agent detail
- tui: render markdown, and set thinking apart
- tui: name the failure, and keep the orchestrator on the roster
- tui: control sub-agents from the panel
- tui: keep the workflow visible, and say what it is doing
- tui: borrow omp's and pi's vocabulary for status and keys
- tui: register projects, and fix the picker's box height
- tui: live threads, sub-agent list, logs, and usage — plus workflow roles
- tui: new threads, a `/` command palette, and settings
- tui: one command to run the TUI against a local server
- tui: render the UI with OpenTUI, the stack opencode's client uses
- tui: add --demo, so the interface can be checked without a server
- tui: AgentTunnel in a terminal
- web: unify control radius and centralize surface elevation
- web: retune the AgentTunnel interface to a graphite and gold identity
- brand: drop the last T3 Code labels and rename prod icon assets
- brand: rebrand app and marketing icons to AgentTunnel

### Fixes

- tui: list only installed harnesses in the workflow picker
- tui: make the panel's letter keys work
- tui: count settled work on the progress bar
- tui: never send a model selection the environment cannot route
- tui: start threads on the workflow provider, and show the whole failure
- tui: survive a cold server, and let ctrl-r retry
- tui: give the picker a height and inset rows their own width
- tui: stop list rows wrapping over each other
- tui: show every command, wrap the cursor, and stop rows overlapping
- tui: fork subscriptions instead of scoping them
- tui: open a thread when the terminal sends Enter as `return`
- tui: connect to a real server
- tui: make the JSX runtime independent of the launch directory
- web: retune the base palette so the default look matches the theme

### Other

- Migrate workflows to Blacksmith
- refactor(tui): render the frame myself, the way pi and omp do

## v0.5.0 (2026-09-23)

### Features

- approved models for auto mode

## v0.4.0 (2026-09-23)

### Features

- web: keep a live recap of the thread above the composer
- agenttunnel: brief sub-agents like a captain, not a ticket system
- agenttunnel: let sub-agents report to the orchestrator while they work
- agenttunnel: keep each thread's orchestrator context across restarts
- web: make the agents panel honest about failures and queued retries
- agenttunnel: filter model config by harness
- agenttunnel: keep model config per thread
- agenttunnel: stop and retry subagents, and keep the orchestrator alive after failures
- usage: timestamp each subagent turn
- web: render interface text 5% denser
- web: step between messages and collapse responses
- web: render orchestrator and subagent output as scannable steps
- agenttunnel: weigh steering while subagents keep working
- agenttunnel: add planner and tester sub-agent roles
- usage: agent role, harness, and cost in the token usage panel
- server: rename .t3 home dirs to .agenttunnel with one-time migration
- usage: read Pi session transcripts on the Usage page
- web: surface orchestrator token usage in the thread usage pane
- steering: show injected steers in blue and hide the working block while queued
- agenttunnel: per-subagent turn stats in the usage drill-in
- agenttunnel: give orchestrators a shared inherited context
- web: quiet the AgentTunnel harness settings
- agenttunnel: end workflows with a real final report

### Fixes

- agenttunnel: a retry's frames own its row, however they arrive
- agenttunnel: retried subagents leave their failed styling
- theme: boot splash and window frame wear the app's palette
- desktop: keep home adoption off the Clerk pre-ready path
- server: adopt legacy .t3 homes on every path
- server: report complete pi token usage to the thread pane
- web: align orchestrator usage copy and subagent totals
- agenttunnel: stream live subagent logs in the agents drill-in

### Other

- ci(release): publish with action-gh-release v3
- ci: install the Rust toolchain in the AgentTunnel release build
- ci: run every job on Depot runners
- fix(ci): wait on real probe signals in the provider registry re-probe test
- fix(grok): keep a turn open while the CLI flushes trailing output
- fix(client-runtime): use an explicit extension for the recap module's import

## v0.3.0 (2026-09-20)

### Features

- agenttunnel: make AgentTunnel the settings home and add Pi harness
- agenttunnel: queue mid-run messages as steering for the next planning pass
- agenttunnel: make the orchestrator pass timeout configurable

### Fixes

- agenttunnel: support Pi as a role harness
- agenttunnel: clamp numeric settings to their schema ranges
- agenttunnel: read numeric settings as numbers after the write-type fix
- agenttunnel: keep steering that arrives during the final pass
- agenttunnel: address second Macroscope review (timeout input, late recaps, steering leak)
- agenttunnel: address Macroscope review on recap accuracy and auto surfaces

## v0.2.0 (2026-09-20)

### Features

- server: report AgentTunnel token usage from OpenCode frames
- web: give AgentTunnel workflow narration color and structure
- web: add the token usage right-panel surface
- web: derive per-turn and subagent token usage stats
- agenttunnel: surface steering delivery and stream workflow feedback
- server: add pi provider driver

### Fixes

- server: derive PiRuntimeError message from operation and detail
- server: address pi provider re-review findings
- server: report OpenCode totals with cached and reasoning subsets
- web: label agent status for assistive tech in the usage pane
- server: address pi provider review findings
- address token usage review findings
- agenttunnel: repair the steering test types and label layout
- server: harden pi runtime and satisfy review findings
- server: reset pi interrupt state when a prompt is rejected

### Performance

- server: gate harness usage parsing on a step_finish substring

### Other

- docs(web): clarify token usage pane reporting footnote

## v0.1.2 (2026-09-19)

### Features

- agenttunnel: queue mid-run messages as steering for the next planning pass
- agenttunnel: make the orchestrator pass timeout configurable
- agenttunnel: stream a progress recap every 30s and poll agent logs faster
- agenttunnel: auto mode lets the orchestrator pick per-task models

### Fixes

- agenttunnel: clamp numeric settings to their schema ranges
- agenttunnel: read numeric settings as numbers after the write-type fix
- agenttunnel: keep steering that arrives during the final pass
- agenttunnel: address second Macroscope review (timeout input, late recaps, steering leak)
- agenttunnel: address Macroscope review on recap accuracy and auto surfaces
- agenttunnel: show orchestrator reasoning in chat and persist server logs to disk

## v0.1.1 (2026-09-19)

### Other

- ci(release): support optional macOS/Windows signing and add install notes

## v0.1.0 (2026-09-19)

First tagged release. The history before this tag is the removed TypeScript monorepo and is not listed.
