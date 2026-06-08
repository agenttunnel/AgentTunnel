# AgentTunnel

A full-screen terminal UI wrapper for [opencode](https://opencode.ai) AI coding agents, built with [Ink](https://github.com/vadimdemedes/ink) and [Zustand](https://github.com/pmndrs/zustand). AgentTunnel orchestrates multiple opencode sessions in a single TUI: an orchestrator analyzes your task, dispatches executor sub-agents, and streams everything back to a clean, scrollable, keyboard-driven interface.

![status](https://img.shields.io/badge/status-alpha-yellow)
![license](https://img.shields.io/badge/license-MIT-blue)
![node](https://img.shields.io/badge/node-%E2%89%A520-green)

## Features

- **Full-screen streaming TUI** powered by Ink (React 19) — header, scrollable output, command-hint popup, persistent input bar.
- **Multi-agent pipeline** — orchestrator parses `<dispatch_task>` XML and spawns executor sub-agents sequentially with progress tracking.
- **Live sub-agent tracking** — every spawned agent has an id, role, pid, status, capped output buffer, and timing.
- **Zustand store** with module-level code-block tracking so fenced code blocks survive across stream chunks.
- **Config panel** for per-role harness and model selection, populated from `opencode models`.
- **Command hints** — type `/` to discover and autocomplete slash-commands.
- **Keyboard-first** — `Ctrl+C` to cancel, `↑/↓` for history, `PgUp/PgDn` to scroll, `Tab` to autocomplete, `Esc` to leave views.
- **Markdown rendering** — inline bold, inline code, headings, and horizontal rules are colorized inline.
- **Graceful cancel** — sends `SIGTERM`, escalates to `SIGKILL` after 5 s, surfaces a system message.
- **Zero runtime deps beyond `ink`, `ink-text-input`, `react`, and `zustand`.**

## Architecture

```
src/
  cli.tsx                  # Entry — arg parsing, signal handling, Ink render
  types/                   # Shared type definitions
  core/
    Pipeline.ts            # Multi-agent orchestration (orchestrator → executors)
    Session.ts             # opencode child_process wrapper with subscriber API
    prompts.ts             # LLM system prompts (orchestrator / executor / reviewer)
    tools.ts               # ToolDefinition / ToolCall type contracts
    ToolRegistry.ts        # Tool-call line parser, validator, and registry
    state/store.ts         # Zustand store + module-level inCodeBlock tracker
    utils/AnsiCleaner.ts   # ANSI escape stripping / splitting
    utils/markdown.ts      # Inline markdown parser returning styled segments
  ui/
    App.tsx                # Root Ink component — layout, input, keyboard
    components/ConfigPanel.tsx
```

The architecture is intentionally layered:

- **`Session`** owns a single `opencode run` process and exposes a small subscriber API. It is unaware of orchestration.
- **`ToolRegistry`** parses the `__AGENTTUNNEL_TOOL__` JSON-line protocol, validates calls against a JSON-Schema-like definition, and invokes registered handlers. It is the single point of truth for what the orchestrator can do.
- **`Pipeline`** composes sessions: it spawns an orchestrator, routes its tool calls to the registry, then spawns one `Session` per dispatched task. It emits high-level events.
- **Store** holds UI state only. The `Pipeline` is the single source of truth for runtime status; the store mirrors what the UI needs to render.
- **Components** are pure consumers of the store and the pipeline event bus. They do not own business logic.

This separation makes it easy to swap the TUI for a web UI, add new tools to the registry (e.g. `web_search`, `ask_user`, `reviewer`), or plug in a different LLM harness later.

## How dispatch works

The orchestrator LLM doesn't have native tool-calling bound to a JSON-schema API in a portable way across opencode versions. Instead, AgentTunnel uses an **out-of-band tool-call protocol** defined in the orchestrator's system prompt:

1. The orchestrator's prompt lists the available tools (`dispatch_task`, `done`) with their JSON-Schema-style parameters.
2. The LLM emits tool calls as standalone lines:
   ```
   __AGENTTUNNEL_TOOL__ {"name":"dispatch_task","arguments":{"role":"executor","task":"Add a vitest test for AnsiCleaner"}}
   __AGENTTUNNEL_TOOL__ {"name":"done","arguments":{"reason":"plan complete"}}
   ```
3. `Pipeline.handleOrchestratorLine` routes each line through `parseToolCallLine` and `ToolRegistry.invoke`. Valid `dispatch_task` calls are queued for execution; `done` ends the planning phase.
4. The queued tasks are then executed sequentially by `opencode run` sub-sessions.

Adding a new tool (e.g. a `web_search` tool for the orchestrator) is a two-step change:

1. Add a `ToolDefinition` in `prompts.ts` and include it in `ORCHESTRATOR_TOOLS`.
2. Register a handler with `ToolRegistry.register` if the tool has side effects.

The Pipeline will pick it up automatically.

## Installation

```bash
git clone https://github.com/your-org/agenttunnel.git
cd agenttunnel
npm install
npm run build
```

Then either run the bundled binary directly:

```bash
./dist/cli.js
```

Or install globally:

```bash
npm link
agenttunnel
```

`opencode` must be installed and on your `PATH`.

## Usage

```text
$ agenttunnel --help
Usage: agenttunnel [options]

Options:
  -w, --workspace <path>   Run inside the given directory
  -m, --model <name>       Default model for all roles
      --yes                Auto-approve all tool calls
  -h, --help               Show this help

Controls:
  Enter         Submit task
  ↑ / ↓         History (or navigate hints)
  PgUp / PgDn   Scroll output
  Tab           Autocomplete hint
  Esc           Leave config view
  Ctrl+C        Cancel running task (or exit when idle)
```

Once running, type a task at the prompt. AgentTunnel spawns the orchestrator, which returns a series of `__AGENTTUNNEL_TOOL__` tool calls (e.g. `dispatch_task`). Each task is then run sequentially by an executor sub-agent, and you can watch progress live.

## Slash Commands

Type `/` to see available commands:

| Command   | Description                  |
| --------- | ---------------------------- |
| `/config` | Open the config panel        |
| `/clear`  | Clear the output buffer      |
| `/help`   | Show in-app help             |
| `/exit`   | Quit AgentTunnel             |

## Development

```bash
npm run dev        # Run src/cli.tsx with tsx (no build step)
npm run typecheck  # Strict TypeScript check
npm test           # Vitest unit tests
npm run build      # esbuild → dist/cli.js
```

## Testing

Tests live next to the code they cover (`*.test.ts`).

```bash
npm test
```

Coverage today:

- `types/index.test.ts` — `defaultConfig()` shape
- `core/Session.test.ts` — subscriber wiring and child-process setup
- `core/state/store.test.ts` — `addOutput`, `clear`, `setRoleConfig`, `pushHistory`
- `core/utils/AnsiCleaner.test.ts` — ANSI escape stripping
- `core/ToolRegistry.test.ts` — `parseToolCallLine`, `validateToolCall`, `ToolRegistry.invoke`

## Design Patterns

- **Tool-call protocol over XML scraping.** The orchestrator emits structured `__AGENTTUNNEL_TOOL__` lines that are validated against a JSON-Schema-style definition. New tools are additive, not breaking.
- **ToolRegistry as the extension point.** Adding a new capability is registering a handler. The pipeline and orchestrator prompt are unchanged.
- **Subscriber pattern for `Session` and `Pipeline`.** Both are decoupled from React and the store via a small event interface (`onOutput`, `onExit`, `on`). The UI subscribes once and forwards events into the store.
- **Single source of truth.** The Zustand store is read-only from React's perspective; mutations go through actions defined in the store module.
- **Module-level mutable state for cross-render trackers.** The `inCodeBlock` flag is intentionally outside the store so it survives across streaming chunks without causing re-renders.
- **Pure render components.** `App.tsx` and `ConfigPanel.tsx` derive all output from store state. No side effects in render.
- **Composition over inheritance.** `Pipeline` owns `Session` instances; the store owns neither. Replacing either is a one-file change.

## Roadmap

- [ ] Reviewer role in the pipeline
- [ ] Persistent history file (in addition to in-memory)
- [ ] Per-role model overrides in the config panel
- [ ] Mouse support (scrollback, clickable hints)
- [ ] Plugin system for custom command hints

## Contributing

Issues and pull requests are welcome. Please run `npm run typecheck` and `npm test` before submitting.

## License

[MIT](./LICENSE)
