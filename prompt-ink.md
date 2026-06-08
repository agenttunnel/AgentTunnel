```
Implement AgentTunnel from scratch — a full-screen terminal UI wrapper for opencode AI coding agents, using **Ink** (not @opentui/react) with TypeScript and Zustand.

## Tech Stack
- **React** (v19+)
- **Ink** (v5+, the React-based terminal UI framework with `render`, `<Box>`, `<Text>`, `<TextInput>`, `useInput`, `useStdin`, `useStdout`)
- **Zustand** (v5+) for state management
- **TypeScript** (strict mode)
- **esbuild** for bundling
- **vitest** for testing

Do NOT use @opentui/core or @opentui/react. Every UI component must use Ink primitives.

## Project Structure
```
AgentTunnel/
├── src/
│   ├── cli.tsx              — Entry point, arg parsing, Ink render
│   ├── types/
│   │   └── index.ts         — Type definitions
│   ├── core/
│   │   ├── Pipeline.ts      — Multi-agent pipeline (EventEmitter)
│   │   ├── Session.ts       — Single opencode process manager
│   │   ├── prompts.ts       — LLM prompts for orchestrator/executor/reviewer
│   │   ├── state/
│   │   │   └── store.ts     — Zustand store
│   │   └── utils/
│   │       ├── AnsiCleaner.ts
│   │       └── markdown.ts
│   └── ui/
│       ├── App.tsx           — Root Ink component
│       └── components/
│           └── ConfigPanel.tsx
├── build.mjs
├── vitest.config.ts
├── tsconfig.json
└── package.json
```

## Features (implement ALL of these)

### 1. Full-screen Streaming TUI
- Use Ink's `render()` with `exitOnCtrlC: false`, `experimental: { preserveInput: true }`, and stdout handling for an alternate-screen-like experience.
- Layout: header bar (1 line) → scrollable output area (flex-grow) → optional error line → optional command hint panel → input bar (1 line, always at bottom).
- Empty state: show "AgentTunnel is ready" with example tasks and command hints.

### 2. Multi-agent Pipeline (`src/core/Pipeline.ts`)
- EventEmitter-based class.
- **Orchestrator phase**: spawns `opencode run <ORCHESTRATOR_PROMPT + user task>` with `--dangerously-skip-permissions`. Parses `<dispatch_task role="executor" task="..." />` XML tags from output.
- **Executor phase**: iterates through dispatched tasks sequentially, spawning `opencode run <EXECUTOR_PROMPT + task>` for each. Tracks progress as `current/total`.
- Emits events: `'output'` (text, type), `'phaseChange'` (phase), `'done'` (ok, msg), `'subAgentUpdate'` (agents[]).
- Sub-agent tracking: each spawned agent gets an id, role, pid, status, output buffer (capped at 200 lines), start/end time.
- Cancel: sends SIGTERM → SIGKILL after 5s.
- Parse both inline (`<dispatch_task ... />`) and block (`<dispatch_task>...</dispatch_task>`) XML formats.

### 3. Zustand Store (`src/core/state/store.ts`)
State shape (use `OutputLine`, `SessionStatus`, `AppConfig`, `RoleConfig`, `PipelinePhase`, `HintItem`, `SubAgentInfo` types):
- `output: OutputLine[]` — array of `{id, text, timestamp, type, codeBlock?}`
- `status: 'idle' | 'running' | 'completed' | 'failed'`
- `inputValue: string`
- `scrollOffset: number`
- `error: string | null`
- `view: 'main' | 'config'`
- `config: AppConfig` — `{ orchestrator: {harness, model}, executor: {...}, reviewer: {...} }`
- `editingField: {section, field, value} | null`
- `pipelinePhase: PipelinePhase` — `{type, label, progress?: {current, total}}`
- `models: string[]`
- `subAgents: SubAgentInfo[]`
- `history: string[]`, `historyIndex: number`
- `showHints: boolean`, `hintIndex: number`, `hintItems: HintItem[]`
- Module-level `inCodeBlock` tracker to detect ``` fences across `addOutput` calls.
- Actions: `addOutput`, `setStatus`, `setInputValue`, `setScrollOffset`, `setError`, `setView`, `setConfig`, `setRoleConfig`, `setEditingField`, `setShowHints`, `setHintIndex`, `setHintItems`, `setModels`, `setPipelinePhase`, `setSubAgents`, `pushHistory`, `setHistoryIndex`, `clear`.

### 4. Session / Process Manager (`src/core/Session.ts`)
- Spawns `opencode run` as a child process.
- `run(prompt)`: builds args with `--dangerously-skip-permissions` and optional `-m <model>`; streams stdout/stderr line-by-line; fires callbacks.
- `cancel()`: SIGTERM → SIGKILL after 5s.
- `onOutput(cb)`, `onExit(cb)` subscriber pattern returning unsubscribe functions.

### 5. Prompts (`src/core/prompts.ts`)
- `ORCHESTRATOR_PROMPT`: instructs the LLM to analyze tasks, break into subtasks, and use `<dispatch_task role="executor" task="..." />` XML format.
- `EXECUTOR_PROMPT`: instructs the executor agent to implement the task thoroughly following project conventions.
- `REVIEWER_PROMPT`: instructs reviewer to check correctness, consistency, and quality.

### 6. CLI Entry Point (`src/cli.tsx`)
- Parse args: `--workspace <path>`, `--yes`/`--auto-approve`, `--model <name>`, `--help`/`-h`.
- Require TTY, exit otherwise.
- Handle SIGINT/SIGTERM for clean shutdown.
- `render(<App ... />)` via Ink.

### 7. App Component (`src/ui/App.tsx`) — Ink Component
- Use `useInput` from Ink for keyboard handling.
- Use `useStdin` / `useStdout` for raw mode and terminal dimensions.
- **Header**: "AgentTunnel" label, optional status spinner + label, optional model name, status color.
- **Output area**: scrollable viewport. Use `useMemo` to compute visible lines based on `scrollOffset` and terminal height. Show "N lines above" indicator when not at bottom.
- **Input bar**: use Ink's `<TextInput>` for user input. Disable during running state.
- **Keyboard handling**:
  - `Ctrl+C`: cancel running task, or exit if idle.
  - `↑/↓`: history navigation (when no hints shown and not running).
  - `PgUp/PgDn`: scroll by half page.
  - `Escape`: return from config view.
  - Tab: autocomplete highlighted hint.
- **Command hints**: when input starts with `/`, show matching commands in a popup above input. Use ↑/↓ to navigate, Tab to autocomplete, Enter to execute.
- **Spinner**: animated spinner during running state (frames: "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏").
- **Markdown rendering**: inline bold (`**text**`), inline code (`` `code` ``), headings, horizontal rules colorized.
- **On mount**: fetch available models via `opencode models` spawn.
- **Pipeline events**: wire pipeline 'phaseChange', 'output', 'done', 'subAgentUpdate' events to Zustand store.

### 8. Config Panel (`src/ui/components/ConfigPanel.tsx`)
- Navigable form with sections for Orchestrator, Executor, Reviewer.
- Each section has Harness (text) and Model (picker) fields.
- Model picker: scrollable list of models from `opencode models`, ↑/↓ to navigate, Enter to select, Escape to close.
- Keyboard: ↑/↓ to navigate fields, Return to edit, q/Escape to return to main view.

### 9. Utilities
- `AnsiCleaner.ts`: `stripAnsi()`, `splitAnsi()`, `hasAnsi()` functions using regex for CSI and OSC sequences.
- `markdown.ts`: `parseMarkdownInline()` returns `MdSegment[]` with `text`, `bold?`, `color?`, `dim?`.

### 10. Types (`src/types/index.ts`)
- `SessionStatus`, `ViewMode`, `OutputLine`, `RoleConfig`, `AppConfig`, `defaultConfig()`.

### 11. Ink-specific Notes
- Use `import {render, Box, Text, useInput, useApp, useStdout, useStdin} from 'ink';`
- Use `import TextInput from 'ink-text-input';` for the input bar (or implement a simple custom one using `useInput` and `useState`).
- Use `useStdout().rows` and `useStdout().columns` for terminal dimensions.
- Use Ink's `<Box flexDirection="column">` for layout.
- Use `<Text color="...">` and `<Text bold>` for styling.
- The app should be a single `<Box>` filling terminal height.

### 12. Build / Config
- `package.json`: `"type": "module"`, bin `AgentTunnel → dist/cli.js`, scripts for `build` (esbuild), `start`/`dev` (tsx), `test` (vitest), `typecheck` (tsc).
- `tsconfig.json`: ES module, strict, JSX react-jsx with `jsxImportSource` for React (not Ink-specific JSX transform — Ink uses standard React JSX).
- `build.mjs`: esbuild bundling for Node, bundle entry `src/cli.tsx`, externalize Ink packages, output `dist/cli.js` with `#!/usr/bin/env node` banner.

### 13. Tests
Write vitest tests for:
- `types/index.test.ts` — `defaultConfig()` returns correct shape.
- `core/Session.test.ts` — verify Session sets up correctly.
- `core/state/store.test.ts` — addOutput, clear, setRoleConfig, pushHistory behavior.
- `core/utils/AnsiCleaner.test.ts` — stripAnsi removes known sequences.

### Important Implementation Details
- When a running pipeline already exists, ignore new task submissions.
- The output lines should be typed (`user`, `assistant`, `system`, `error`, `tool`, `tool_output`) for color-coded rendering.
- Sub-agent updates from the pipeline must be pushed to the store in real time via event listeners.
- The `inCodeBlock` tracker must be module-level in the store file (not inside React) so it persists across renders.
- Cancel must set status to `'idle'` and emit a system message.

Generate all files with complete implementations. Every file should be production-quality TypeScript with proper types and no `any` unless absolutely necessary.
```
