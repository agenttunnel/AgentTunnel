import type { ToolDefinition } from './tools.js';

export const DISPATCH_TASK_TOOL: ToolDefinition = {
  name: 'dispatch_task',
  description:
    'Dispatch a single concrete subtask to an executor sub-agent. Tasks are grouped into named pipelines. Tasks in the same pipeline run together according to the pipeline mode (parallel or sequential). Different pipelines run concurrently. Use the optional "depends_on" argument to gate a pipeline on the completion of other named pipelines.',
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
      pipeline: {
        type: 'string',
        description:
          'Lower-snake-case pipeline id. Tasks with the same pipeline id are grouped together. If omitted, a unique auto-id is used.',
        required: false,
      },
      mode: {
        type: 'enum',
        description:
          'How tasks within THIS pipeline are scheduled. "parallel" (default) runs them concurrently. "sequential" runs them in dispatch order. Different pipelines are always parallel to each other unless depends_on is set.',
        enum: ['parallel', 'sequential'],
        required: false,
      },
      depends_on: {
        type: 'array',
        description:
          'Optional list of pipeline ids that must complete before this task\'s pipeline starts. Use this to compose pipelines into a larger DAG.',
        required: false,
        items: { type: 'string', description: 'A pipeline id declared by an earlier dispatch_task call.' },
      },
      context: {
        type: 'array',
        description:
          'Optional list of context keys (lower_snake_case) to inject into the agent\'s prompt. The agent will see the values verbatim.',
        required: false,
        items: { type: 'string', description: 'A key previously set via update_context.' },
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
2. **Write the goal and any stable facts to the shared notebook** with \`update_context\`. Use short, lower_snake_case keys.
3. **Design your team.** Break the work into one or more **named pipelines** of subtasks:
   - Use multiple pipelines when subtasks are independent and benefit from running concurrently.
   - Use a single sequential pipeline for strict ordering.
   - Use a \`depends_on\` edge to gate a pipeline on another pipeline's completion.
4. **Dispatch each subtask** with \`dispatch_task\`:
   - Set \`pipeline\` to a lower_snake_case id. Tasks sharing an id are grouped.
   - Set \`mode\` to \`parallel\` (default; tasks in this pipeline run concurrently) or \`sequential\` (tasks run in dispatch order).
   - Set \`depends_on\` to a list of pipeline ids that must complete before this pipeline starts. Leave empty to run with no upstream.
   - Use the \`context\` argument to hand the sub-agent ONLY the notebook entries it needs.
5. **After all pipelines finish**, you will be called a second time with a transcript of what they did. You may:
   - Dispatch follow-up tasks (e.g. a reviewer) — they become new pipelines.
   - Call \`done\` to finish.

## Patterns

- **Fan-out research, then implement:**
  - pipeline "research" (parallel): two agents gather info on two topics
  - pipeline "implement" (sequential), depends_on=["research"]: one agent implements
- **Parallel independent workstreams:** A and B have no overlap → just two pipelines, no \`depends_on\`.
- **Strictly linear work:** one pipeline, mode \`sequential\`. Don't use \`depends_on\`.

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
