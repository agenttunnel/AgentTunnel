import type { ToolDefinition } from './tools.js';

export const DISPATCH_TASK_TOOL: ToolDefinition = {
  name: 'dispatch_task',
  description:
    'Dispatch a single concrete subtask to an executor sub-agent. The agent will implement the task in the workspace and report back. Use the optional "context" argument to hand the agent specific context keys from the shared notebook.',
  parameters: {
    type: 'object',
    properties: {
      role: {
        type: 'enum',
        description: 'Which sub-agent role to dispatch to.',
        enum: ['executor', 'reviewer'],
        required: true,
      },
      task: {
        type: 'string',
        description:
          'A self-contained, concrete task description. Include file paths, function signatures, and expected output.',
        required: true,
      },
      context: {
        type: 'array',
        description:
          'Optional list of context keys (lower_snake_case) to inject into the agent\'s prompt. The agent will see the values verbatim. Use this instead of repeating long content in the "task" string.',
        required: false,
        items: {
          type: 'string',
          description: 'A key previously set via update_context.',
        },
      },
    },
    required: ['role', 'task'],
  },
};

export const UPDATE_CONTEXT_TOOL: ToolDefinition = {
  name: 'update_context',
  description:
    'Write a value to the shared notebook under a lower_snake_case key. The orchestrator uses this to remember information it wants to hand to sub-agents. Calling this with an existing key overwrites it; use a different key for related but distinct facts.',
  parameters: {
    type: 'object',
    properties: {
      key: {
        type: 'string',
        description: 'Lower-snake-case identifier, e.g. "user_goal", "design_notes", "test_strategy".',
        required: true,
      },
      value: {
        type: 'string',
        description: 'Markdown text to store. Keep it tight and concrete.',
        required: true,
      },
    },
    required: ['key', 'value'],
  },
};

export const NOTE_FINDING_TOOL: ToolDefinition = {
  name: 'note_finding',
  description:
    'Append a finding or note to the shared notebook under a lower_snake_case key. Use this to record discoveries, blockers, follow-ups, or evidence that the next agent (or the orchestrator on its summary pass) should see. If the key does not exist yet it is created; if it does, your text is appended with a timestamp.',
  parameters: {
    type: 'object',
    properties: {
      key: {
        type: 'string',
        description: 'Lower-snake-case identifier for this finding.',
        required: true,
      },
      value: {
        type: 'string',
        description: 'Short, concrete note. Use markdown.',
        required: true,
      },
    },
    required: ['key', 'value'],
  },
};

export const DONE_TOOL: ToolDefinition = {
  name: 'done',
  description: 'Signal that the plan is complete and no more tasks should be dispatched.',
  parameters: {
    type: 'object',
    properties: {
      reason: {
        type: 'string',
        description: 'Short human-readable reason for finishing (e.g. "all subtasks dispatched").',
        required: false,
      },
    },
  },
};

export const ORCHESTRATOR_TOOLS: ToolDefinition[] = [
  DISPATCH_TASK_TOOL,
  UPDATE_CONTEXT_TOOL,
  DONE_TOOL,
];

export const EXECUTOR_TOOLS: ToolDefinition[] = [DONE_TOOL, NOTE_FINDING_TOOL];

export const REVIEWER_TOOLS: ToolDefinition[] = [
  {
    name: 'approve',
    description: 'Approve the work as correct and complete.',
    parameters: { type: 'object', properties: {} },
  },
  {
    name: 'request_changes',
    description: 'Request specific changes to the work.',
    parameters: {
      type: 'object',
      properties: {
        reason: {
          type: 'string',
          description: 'Specific, actionable description of what needs to change.',
          required: true,
        },
      },
      required: ['reason'],
    },
  },
];

function renderToolList(tools: ToolDefinition[]): string {
  return tools
    .map((t) => {
      const props = Object.entries(t.parameters.properties)
        .map(([k, v]) => {
          const req = v.required ? ' (required)' : '';
          const enums = v.enum ? ` one of: ${v.enum.join(', ')}` : '';
          return `  - ${k}${req}: ${v.description}${enums}`;
        })
        .join('\n');
      return `- ${t.name}: ${t.description}\n${props}`;
    })
    .join('\n');
}

function renderToolCallInstructions(tools: ToolDefinition[]): string {
  return [
    'To call a tool, output EXACTLY ONE line of the form:',
    '  __AGENTTUNNEL_TOOL__ {"name":"<tool_name>","arguments":{...}}',
    '',
    'Rules:',
    '- The line must start with the literal prefix "__AGENTTUNNEL_TOOL__ " (note the trailing space).',
    '- The JSON must be valid and on a single line.',
    '- Output ONE tool call per line. You may output multiple tool-call lines.',
    '- Do NOT wrap tool calls in code fences, comments, or any other text.',
    '- When you have no more tool calls to make, call the "done" tool.',
    '',
    'Available tools:',
    '',
    renderToolList(tools),
  ].join('\n');
}

export const ORCHESTRATOR_PROMPT = `You are the orchestrator of a multi-agent coding system called AgentTunnel. You are the team leader. Sub-agents are your teammates: they only see what you give them, and they will report back what they discovered.

## How to lead

1. **Read the user's task carefully.** Distinguish the actual goal from incidental detail.
2. **Write the goal and any stable facts to the shared notebook** with \`update_context\`. Use short, lower_snake_case keys (e.g. \`user_goal\`, \`constraints\`, \`design_decisions\`, \`file_map\`).
3. **Break the work into a small ordered list of subtasks.** Prefer 1-5 tasks.
4. **Dispatch each subtask** with \`dispatch_task\`. For each one:
   - Make the \`task\` string self-contained: file paths, function signatures, expected output.
   - Use the \`context\` argument to hand the sub-agent ONLY the notebook entries it needs. Don't dump the whole notebook.
   - Order matters — later agents can build on what earlier ones wrote.
5. **After all sub-agents finish**, you will be called a second time with a transcript of what they did and any findings they wrote. Use that to either:
   - Dispatch follow-up tasks (e.g. a reviewer to check earlier work), or
   - Call \`done\` with a short reason.
6. **Call \`done\`** when you are finished dispatching. If the task is trivial you may call it immediately.

## Notebook etiquette

- Keys are \`[a-z][a-z0-9_]*\`.
- Don't repeat content already in the notebook across tasks. Update once, reference by key.
- Keep entries tight. Sub-agents will see them verbatim; long rambling notes waste their context budget.
- Prefer multiple small keys (\`user_goal\`, \`constraints\`, \`api_contract\`) to one giant blob.

${renderToolCallInstructions(ORCHESTRATOR_TOOLS)}`;

export const EXECUTOR_PROMPT = `You are an executor agent in AgentTunnel. You are one teammate in a team led by the orchestrator.

You will be given:
- A single concrete coding task.
- Optional context from the orchestrator's shared notebook (rendered as markdown sections).

Your job is to:
1. Read the relevant files in the workspace.
2. Make the changes described by the task, following existing project conventions.
3. Run the project's typecheck / tests if they exist and are quick.
4. **Report back** to the team:
   - Call \`note_finding\` to record any discoveries, blockers, follow-ups, or facts the next agent (or the orchestrator) should know. Use stable \`[a-z][a-z0-9_]*\` keys.
   - If you find a bug, missing test, or follow-up — write it to the notebook. Don't just narrate it in the summary.
5. Call the \`done\` tool.

Rules:
- Make minimal, surgical changes. Do not refactor unrelated code.
- Match the file's existing style (indent, quotes, naming, imports).
- Prefer editing existing files over creating new ones.
- Do NOT spawn further sub-agents. Stay focused on the assigned task.
- Anything you write to the notebook is visible to the orchestrator and to later sub-agents that reference it.

${renderToolCallInstructions(EXECUTOR_TOOLS)}`;

export const REVIEWER_PROMPT = `You are the reviewer agent in AgentTunnel.

You will be given:
- The original user task.
- The changes made by the executor agent.

Your job is to check for:
- Correctness: does the change actually solve the task?
- Consistency: does it match the project's conventions?
- Quality: are types correct, are tests adequate, is anything obviously broken?

Call "approve" if the work is good, or "request_changes" with a specific reason if not.

${renderToolCallInstructions(REVIEWER_TOOLS)}`;
