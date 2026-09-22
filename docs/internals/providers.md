# Provider architecture

> For maintainers. Using AgentTunnel? See [docs/user](../user/).

A provider is the agent runtime that does the actual work. AgentTunnel supports several, and the
orchestration layer does not know which one is behind a thread.

## Built-in drivers

[`builtInDrivers.ts`][drivers] exports `BUILT_IN_DRIVERS` with seven entries:

| Driver kind   | Driver source                                 |
| ------------- | --------------------------------------------- |
| `codex`       | [`Drivers/CodexDriver.ts`][codex]             |
| `claudeAgent` | [`Drivers/ClaudeDriver.ts`][claude]           |
| `cursor`      | [`Drivers/CursorDriver.ts`][cursor]           |
| `grok`        | [`Drivers/GrokDriver.ts`][grok]               |
| `opencode`    | [`Drivers/OpenCodeDriver.ts`][opencode]       |
| `pi`          | [`Drivers/PiDriver.ts`][pi]                   |
| `agenttunnel` | [`Drivers/AgentTunnelDriver.ts`][agenttunnel] |

Each driver declares its `driverKind`, a `configSchema`, and a `create` function that builds an
adapter in a child scope. Adapter implementations live beside them in
`apps/server/src/provider/Layers/` (`CodexAdapter.ts`, `ClaudeAdapter.ts`, and so on) and conform to
[`ProviderAdapter.ts`][adapter]. Read the driver plus its adapter to see how a specific agent's
transport, config, and event shapes are mapped.

## Registry and routing

Two registries separate configuration from live processes:

- [`ProviderInstanceRegistry`][instances] keys configured instances by `ProviderInstanceId`. Creating
  one looks up the driver by `driverKind`, decodes `entry.config` with that driver's schema, opens a
  child scope, and calls `driver.create`.
- [`ProviderAdapterRegistry`][registry] resolves an instance ID to its live adapter via
  `getByInstance`.

[`ProviderService`][service] sits on top. It combines the adapter registry with the provider session
directory to route session and turn operations for a thread, so callers name a thread, not an agent.

Adding a driver means writing the driver plus adapter and adding it to `BUILT_IN_DRIVERS`. No
orchestration, contract, or client change is required for the common case.

## OpenCode server ownership and catalog

Each OpenCode provider instance owns one lazy local server for catalog discovery and
text-generation helpers through [`OpenCodeServerOwner.ts`][opencode-server-owner]. Concurrent
borrowers share startup. The server closes 30 seconds after the last borrower releases it, or
when the provider instance closes. A failed or exited process can be started again on the next
use. An externally configured OpenCode server remains externally owned.

The local server and its SDK clients use one resolved password. An explicit provider password
overrides `OPENCODE_SERVER_PASSWORD` in the spawned environment. Without an explicit password,
the client uses the password from the environment that the process inherits. External servers use
only their explicit provider password and never inherit the host's local password.

Every server connection must pass the authenticated `/global/health` check before inventory or
session operations start. The response must contain a valid version at or above 1.14.19. Local
owners cache this result for the lifetime of the spawned process. External actions check once when
they create their server connection, not for each model or SDK request.

Chat adapters keep their own server per thread. They register a thread-specific `t3-code` MCP
connection, while OpenCode stores MCP connections by directory. Sharing these chat servers
without changing MCP routing would let two threads in one directory replace each other's
connection.

OpenCode loads its catalog through the HTTP API when an enabled provider instance starts. The
provider registry keeps the snapshot in memory and persists it in the existing per-instance cache.
Each `subscribeServerConfig` connection refreshes all providers, so a client reconnect reloads the
OpenCode catalog from the current helper. The `serverRefreshProviders` request also refreshes it.
Periodic OpenCode probes remain disabled. OpenCode reads credentials for each inventory request,
but its native configuration files can remain cached for the lifetime of the helper process. The
helper closes 30 seconds after its last inventory or text-generation borrower releases it. A
refresh after that idle period starts a new helper and reads file changes. Repeated refreshes and
active text-generation work can extend process reuse. Changes to the provider configuration or
environment replace the instance and start a new discovery. Changes to unrelated settings only
update snapshot enrichment. Other providers retain their existing refresh policy.

AgentTunnel does not own an external OpenCode process. Native configuration changes there can require
an external reload or restart before AgentTunnel's next refresh sees them.

The shared server's idle shutdown does not clear the catalog. Failed discovery keeps the last
known models, slash commands, and skills through the registry's existing merge rules. A successful
empty inventory is authoritative. Existing threads keep their explicit model identifier and
options when catalog metadata is missing; the catalog is not permission to choose a different
model for a thread.

## Pi RPC sessions

Pi runs as a child of the server. `pi --mode rpc` speaks strict JSONL over stdin/stdout, framed on
LF only so a JSON string containing U+2028/U+2029 is not split ([`piRuntime.ts`][pi-runtime]). One
child serves one thread, spawned with `--session-id <threadId>` so a fresh process resumes that
thread's session. Closing the adapter scope kills the child, so a stopped session cannot leak a
process. The adapter maps pi's event stream into `ProviderRuntimeEvent`s
([`Layers/PiAdapter.ts`][pi-adapter]), including delta-only `message_update` assembly, tool
lifecycle, and usage from `message_end`.

Pi RPC exposes no approval or permission protocol, so `respondToRequest` and `respondToUserInput`
are resolving no-ops and an extension UI request surfaces as a runtime warning. The snapshot probe
runs `pi --version` and `pi --list-models` ([`Layers/PiProvider.ts`][pi-provider]); model slugs are
`provider/model`. Pi instances are manual-refresh only (`refreshOnInterval` false), and text
generation uses one-shot `pi --mode json -p` invocations
([`textGeneration/PiTextGeneration.ts`][pi-textgen]).

## Model manifest

The model picker's legacy section is driven by `apps/server/src/provider/model-manifest.json`, which
lists the current (non-legacy) model slugs per driver kind. The `ModelManifest` service
(`apps/server/src/provider/ModelManifest.ts`) refreshes that data from the same file on `main` via
raw.githubusercontent.com, so moving a model in or out of the legacy section is a commit, not a
release. Preference order is remote fetch, then the on-disk copy of the last successful fetch (in
the state directory), then the bundled copy. Fetches are TTL-gated, run concurrently with provider
probes, respect the `enableProviderUpdateChecks` setting, and never fail a provider check. The
Codex and Claude drivers apply the classification to every snapshot with `applyModelManifest`;
driver kinds absent from the manifest have no legacy concept.

## AgentTunnel workflow driver

`agenttunnel` presents one AgentTunnel thread as a workflow coordinator. Its server-side adapter launches an
orchestrator CLI process, parses `__AGENTTUNNEL_TOOL__` actions, and schedules planner, executor,
tester, and reviewer CLI processes in named pipelines. Pipelines can run in parallel, run
sequentially, or wait on other pipelines. The adapter maps the workflow coordinator to a
`local_workflow` task and each child to a `local_agent` task with `parentAgentId`, phase metadata,
progress, and terminal status. The existing chat timeline and Agents panel therefore render the
workflow without a client-specific transport.

AgentTunnel is enabled by default and is the primary provider for fresh installations; the other
built-in providers are opt-in. Configure each role's harness path in provider settings — orchestrator,
planner, executor, tester, and reviewer all default to `opencode`. AgentTunnel child sessions
are currently non-interactive and use the configured permission-bypass setting; approval and user-input
requests are not yet routed through the AgentTunnel request surface.

Settings for AgentTunnel and its harnesses live together on the `/settings/agenttunnel` page: a
**Workflow** section (auto mode, roles, permissions, pass limits) backed by the default AgentTunnel
instance, and a **Harnesses** section listing every provider instance with an enable switch. Enabling a
harness writes the matching default provider instance's `enabled` flag; the composer's model config
shows only harnesses that are enabled (or currently selected by a role). There is no standalone
Providers settings page.

The driver branches on the harness CLI basename: `opencode` uses the OpenCode inventory probe
(`<harness> models --verbose`) and `run --format json`, while `pi` uses `pi --list-models` for the
model list and one-shot `pi --mode json -p <prompt> --no-session` per role, translating pi's JSONL
`message_end` text into the same `__AGENTTUNNEL_TOOL__` protocol. Any other basename keeps the generic
`run` invocation and exposes no model list. Model probes never read stdin and are wrapped in a
timeout, so a wedged or interactive harness degrades to a readable provider warning instead of
blocking the registry.

A completed workflow ends with a real final report rather than just the machinery tables. The
orchestrator's closing `done` action may carry `summary` (what happened) and `next_steps`, and only a
`done` from a pass that dispatched no new pipelines counts as the final report, so an earlier
planning `done` cannot overwrite it. The final assistant message leads with `### What happened` and
`### Next steps` before the pipelines and agents tables, `workflow.completed.summary` carries a
plain-text condensation of that result, and the report is stored in shared context as
`workflow_result` and `next_steps` so a follow-up turn can resolve "continue" or "previous task".

### Orchestrator passes and steering

Passes and pipelines are not step-locked. Pipelines run as forked fibers and the orchestrator takes a
pass on whichever comes first — the user steering or the running work settling — so steering never
waits for subagents to finish: the pass weighs it while they keep working, and any pipeline that pass
dispatches starts as soon as its dependencies allow. Steering that lands while a pass is already
running waits for that pass, because a harness process cannot be handed another prompt mid-run; the
queue starts the next pass as soon as the running one ends. A pass carrying steering uses a
`## Steering pass` section and lists the sub-agents still running under `## Agents working right now`,
so the orchestrator routes affected work to a follow-up dispatch — naming the running pipeline in
`depends_on` when the change must land after it finishes — rather than assuming a running one-shot
agent will adjust. A round that ran pipelines still earns its summary pass once that work settles.

### Harness model catalogs

The provider snapshot is the union of one catalog per harness the workflow can run: every role
harness in the instance config, plus the enumerable presets (`opencode`, `pi`) so switching a role —
or a thread's own model config — to a preset still finds a catalog when the environment's harnesses
all point at one CLI. Harnesses with no listing command are skipped; their models stay available
through their own provider instance instead.

Each model carries `harnesses`, the CLIs that reported it, and a slug two harnesses both report stays
one row tagged with both, so the model config can narrow a role to its harness without rendering
duplicates. Custom models carry no harness: the user typed them for the workflow, not for one CLI.
Probes run concurrently; only the orchestrator harness gates the snapshot state, so a role harness
that cannot answer is a missing catalog rather than a warning on the provider.

The model config narrows the list to the harness of the row it is editing. A harness the driver
cannot enumerate (`codex`, `claude`, `cursor-agent`, `grok`, or an unmapped path) shows that
harness's own provider instance models; selecting one of those for the thread model switches the
thread to that provider, since the workflow catalog has no matching slug.

### Model selection and auto mode

Each role has a harness, model, and reasoning variant. Those live in provider settings as the
environment's **defaults**, and each thread stores its own copy of them: `thread.modelConfig`, set
from the defaults when the thread is created and editable per thread from the composer's model
config panel. A thread keeps its snapshot for its whole life, so changing the defaults only affects
threads created afterwards; the panel's reset action re-snapshots the thread from the current
defaults. Threads created before per-thread configs existed (and threads created by clients that do
not send one) have no snapshot and follow the defaults live. On the wire the snapshot rides
`thread.create` / `thread.meta.update` (guarded by the `threadModelConfig` environment capability)
and reaches the adapter on `ProviderSendTurnInput.modelConfig`, which the adapter spreads over the
instance settings for that turn. `autoMode` rides along with it.

Harness and variant otherwise come straight from the effective config. By default
(`autoMode` off), a child's model follows the existing precedence: the role's model (`executorModel`,
`testerModel`, and so on) when set, then `orchestratorModel`, then the session model. Task-level
models are ignored entirely in this mode.

Auto mode lets the orchestrator choose a model per dispatched task. The driver builds
`availableModels` from the models the orchestrator harness reported, plus the workflow's custom
models, and the adapter forwards it to `runAgentTunnel`. Models that only another role's harness
reported stay out: the orchestrator cannot dispatch them to the coordinator.
In auto mode the orchestrator prompt for pass 0 and summary passes gains an `## Available models`
block listing those slugs (only when the list is non-empty), appended after the base prompt and before
the `## Shared context` and `## Summary pass` sections. `dispatch_task` accepts an optional `model`
argument.

A task's `model` is honored only in auto mode and only when it is a member of `availableModels`; an
empty or missing list means no task model is ever valid. A non-empty slug outside the list emits an
`orchestrator.output` warning and is ignored. The precedence becomes task model (auto only) > role
model > `orchestratorModel` > session model. The chosen model is shown in the queued dispatch message
and in the `agent.started` event, so the thread timeline and Agents panel surface it per task.

### Inherited context

Every workflow inherits a global, cross-project context from earlier orchestrators and feeds durable
lessons back into it. The store lives in the hidden `.orchestrator-context/` folder under the
environment's state directory: `GUIDE.md` explains how to navigate the rules, `rules/*.md` holds one
file per category (`design-decisions`, `model-fit`, `model-quality`, plus any category an
orchestrator introduces), and `LOG.md` is the chronological ledger. The adapter renders the guide and
rules into the orchestrator prompt before `runAgentTunnel` (pass 0, steering passes, and summary
passes) and appends the orchestrator's `record_learning` entries after the run, under an in-process
semaphore plus a lock file, so concurrent workflows on one environment never interleave a
read-modify-write. The summary and steering pass prompts include a dispatch-outcomes table (role,
harness, model, status, task) so model-fit and model-quality entries are grounded in actual results,
and the finish markdown lists the recorded entries. Every entry is stamped with its ISO date,
project, thread, workflow, and terminal status. Prompt rendering caps the guide and each rules file and trims from the top when the budget is
exceeded, so a long-lived context cannot blow up the planner prompt. Flushing is best-effort: it never
fails a turn, and it logs a warning and skips the update if the lock stays held too long.

### Thread context

The shared notebook and the thread's turn history are durable, not process-local. `ThreadContextStore.ts`
writes one JSON file per thread under `.orchestrator-context/threads/<threadId>.json` holding the
notebook the last turn ended with plus every turn's task, terminal status, and summary (newest 50).
The adapter loads it when a session starts, so closing the app and resuming a thread continues from
the context the team built instead of an empty notebook, and it appends a run after every turn —
including turns that crashed, which keep the previous notebook. Writes are atomic and best-effort:
losing the durable copy logs a warning and never fails the user's turn.

A resumed turn rehydrates the stored notebook into real keys rather than nesting the dump as one
opaque blob (`parseSharedContextDump` reads back the `### key` sections `renderForPrompt` writes), so
the orchestrator reads `workflow_result`, `next_steps`, and `decisions` as its own entries. When a
thread resumes, the session also prints a short notice — how much context came back, what the last
turn produced, and that turn's next steps — so the user sees where the thread stands without opening
the file.

The orchestrator records decisions with the `record_decision` action (`decision`, optional
`rationale`). Decisions append to the notebook's `decisions` key, oldest first, and therefore ride
into the context dump, the durable file, and the next session's prompt; each one also prints as a
`🧭 Decision recorded` line in the transcript. The prompt requires a decision entry for anything a
later session would otherwise have to re-decide — the approach chosen, what was rejected, a
model/harness pick, a retry or stop, a change of plan after steering — and tells the orchestrator to
supersede rather than silently repeat an earlier decision.

## How provider work is requested

Clients never call a provider directly. They dispatch orchestration commands over the RPC method
`orchestration.dispatchCommand`, defined with the rest of the orchestration surface in
[`orchestration.ts`][contracts]. The client-dispatchable provider-facing commands are
`thread.turn.start`, `thread.turn.interrupt`, `thread.approval.respond`,
`thread.user-input.respond`, `thread.checkpoint.revert`, and `thread.session.stop`, plus the mode
setters `thread.runtime-mode.set` and `thread.interaction-mode.set`.

The engine persists an event for the command, and a server-side reactor performs the provider call.
Provider output comes back as internal commands such as `thread.message.assistant.delta` and
`thread.session.set`, which clients observe through `orchestration.subscribeThread`. See
[overview.md](./overview.md) for the command/event loop.

## Server-side workers

Provider work flows through three queue-backed workers. All three are built with
`makeDrainableWorker` from [`DrainableWorker.ts`][worker] and expose `drain` for deterministic test
synchronization.

1. [`ProviderRuntimeIngestion`][ingest] consumes provider runtime streams and emits orchestration
   commands.
2. [`ProviderCommandReactor`][cmd] reacts to orchestration intent events and dispatches provider
   calls.
3. [`CheckpointReactor`][checkpoint] captures workspace checkpoints on turn start and completion, and
   performs reverts.

### Buffered assistant delivery

A thread in `buffered` assistant delivery mode accumulates assistant text instead of streaming each
delta. The buffer is not held until turn completion. In [`ProviderRuntimeIngestion`][ingest],
`MAX_BUFFERED_ASSISTANT_CHARS` is 24,000: the append that would exceed it invalidates the buffer and
spills the whole accumulated text as one delta. The buffer also flushes at interaction boundaries,
when a request opens (approval) or user input is requested, via
`flushBufferedAssistantMessagesForTurn`.

[drivers]: ../../apps/server/src/provider/builtInDrivers.ts
[codex]: ../../apps/server/src/provider/Drivers/CodexDriver.ts
[claude]: ../../apps/server/src/provider/Drivers/ClaudeDriver.ts
[cursor]: ../../apps/server/src/provider/Drivers/CursorDriver.ts
[grok]: ../../apps/server/src/provider/Drivers/GrokDriver.ts
[opencode]: ../../apps/server/src/provider/Drivers/OpenCodeDriver.ts
[opencode-server-owner]: ../../apps/server/src/provider/OpenCodeServerOwner.ts
[pi]: ../../apps/server/src/provider/Drivers/PiDriver.ts
[pi-runtime]: ../../apps/server/src/provider/piRuntime.ts
[pi-adapter]: ../../apps/server/src/provider/Layers/PiAdapter.ts
[pi-provider]: ../../apps/server/src/provider/Layers/PiProvider.ts
[pi-textgen]: ../../apps/server/src/textGeneration/PiTextGeneration.ts
[agenttunnel]: ../../apps/server/src/provider/Drivers/AgentTunnelDriver.ts
[adapter]: ../../apps/server/src/provider/Services/ProviderAdapter.ts
[instances]: ../../apps/server/src/provider/Services/ProviderInstanceRegistry.ts
[registry]: ../../apps/server/src/provider/Services/ProviderAdapterRegistry.ts
[service]: ../../apps/server/src/provider/Layers/ProviderService.ts
[contracts]: ../../packages/contracts/src/orchestration.ts
[worker]: ../../packages/shared/src/DrainableWorker.ts
[ingest]: ../../apps/server/src/orchestration/Layers/ProviderRuntimeIngestion.ts
[cmd]: ../../apps/server/src/orchestration/Layers/ProviderCommandReactor.ts
[checkpoint]: ../../apps/server/src/orchestration/Layers/CheckpointReactor.ts
