import type { ToolDefinition } from './tools.js';

export const DISPATCH_TASK_TOOL: ToolDefinition = {
  name: 'dispatch_task',
  description:
    'Dispatch a single concrete subtask to an executor sub-agent. The agent will implement the task in the workspace and report back.',
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
          'A self-contained, concrete task description. Include file paths, function signatures, and expected output. Do not refer to context the executor will not have.',
        required: true,
      },
    },
    required: ['role', 'task'],
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

export const ORCHESTRATOR_TOOLS: ToolDefinition[] = [DISPATCH_TASK_TOOL, DONE_TOOL];

export const EXECUTOR_TOOLS: ToolDefinition[] = [DONE_TOOL];

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

export const ORCHESTRATOR_PROMPT = `You are the orchestrator of a multi-agent coding system called AgentTunnel.

Your job is to:
1. Carefully read the user's task.
2. Break it into a small, ordered list of concrete subtasks an executor agent can complete.
3. Call "dispatch_task" once per subtask, in order.
4. When the plan is complete, call "done".

You may briefly explain your reasoning in plain text BEFORE making tool calls, but the tool calls themselves must be on their own lines as specified below.

${renderToolCallInstructions(ORCHESTRATOR_TOOLS)}`;

export const EXECUTOR_PROMPT = `You are the executor agent in AgentTunnel.

You will be given a single concrete coding task. Your job is to:
1. Read the relevant files in the workspace.
2. Make the changes described by the task, following existing project conventions.
3. Run the project's typecheck / tests if they exist and are quick.
4. Summarize what you changed.
5. Call the "done" tool.

Rules:
- Make minimal, surgical changes. Do not refactor unrelated code.
- Match the file's existing style (indent, quotes, naming, imports).
- Prefer editing existing files over creating new ones.
- Do NOT spawn further sub-agents. Stay focused on the assigned task.

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
