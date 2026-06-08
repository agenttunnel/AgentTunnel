export type SessionStatus = 'idle' | 'running' | 'completed' | 'failed';

export type ViewMode = 'main' | 'config' | 'sub-agents' | 'sub-agent-detail';

export type OutputLineType =
  | 'user'
  | 'assistant'
  | 'system'
  | 'error'
  | 'tool'
  | 'tool_output';

export interface OutputLine {
  id: number;
  text: string;
  timestamp: number;
  type: OutputLineType;
  codeBlock?: boolean;
}

export type PipelinePhaseType =
  | 'idle'
  | 'orchestrating'
  | 'dispatching'
  | 'executing'
  | 'reviewing'
  | 'completed'
  | 'failed'
  | 'cancelled';

export interface PipelinePhase {
  type: PipelinePhaseType;
  label: string;
  progress?: { current: number; total: number };
}

export interface RoleConfig {
  harness: string;
  model: string;
}

export type RoleName = 'orchestrator' | 'executor' | 'reviewer';

export interface AppConfig {
  orchestrator: RoleConfig;
  executor: RoleConfig;
  reviewer: RoleConfig;
}

export interface HintItem {
  command: string;
  description: string;
}

export interface SubAgentInfo {
  id: string;
  role: RoleName;
  pid?: number;
  status: SessionStatus;
  task: string;
  output: string[];
  startedAt: number;
  endedAt?: number;
}

export function defaultConfig(): AppConfig {
  return {
    orchestrator: { harness: 'opencode', model: '' },
    executor: { harness: 'opencode', model: '' },
    reviewer: { harness: 'opencode', model: '' },
  };
}
