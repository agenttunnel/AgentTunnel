---
layout: default
title: Job templates
description: A job template is a Markdown file that starts a thread with a task and per-role models.
---

## Job templates

A **job template** is a Markdown file. The body is the task. Optional YAML at the top sets the thread name, the project, and model overrides per role:

```markdown
---
name: Fix flaky login test
project: my-project
modelConfig:
  executor:
    harness: codex
    model: gpt-5-codex
    variant: high
---

Find out why the login test fails intermittently and fix it.
```

Create a starter file with `agenttunnel template`, then start it with `agenttunnel run`. Unknown YAML keys are an error, so a typo does not pass silently.

