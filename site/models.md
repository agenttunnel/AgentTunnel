---
layout: default
title: Models
description: Pick the model each harness runs, approve models for auto mode, and check usage and cost.
---


Each harness reports the models it can use. OpenCode, Pi, and Command Code list their models through their own CLIs. Claude and Codex have no usable listing command, so add their models yourself:

```sh
agenttunnel models ls                   # every known model, with its variants and role defaults
agenttunnel models ls --harness claude
```

Add a model in the app's Models panel, or through `models.add`. Refresh discovered lists with `models.refresh`.

**Auto mode** lets the orchestrator choose each task's model. It only picks from models you have approved for that harness (`approvedByHarness` in settings). Turn it on per thread with `thread.setAutoMode`.

Variants (for example a reasoning level) are chosen per role or per job, where the harness supports them.

## Usage and cost

The **Token usage** panel shows tokens and cost for the thread, split between the orchestrator and subagents. `agenttunnel usage` prints the same figures from a terminal. Add `--limits` to show your subscription limits where the provider exposes them (Claude and Command Code).

