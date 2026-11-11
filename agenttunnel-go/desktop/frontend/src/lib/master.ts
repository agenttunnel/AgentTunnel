import type { Rpc } from "./rpc";

/** One prior exchange sent with a master.send request, oldest first. */
export type MasterMessage = { role: "user" | "assistant"; text: string };

/** The thread the master orchestrator proposes to start from a conversation. */
export type MasterAction = {
  type: "start_thread";
  projectId: string;
  title: string;
  prompt: string;
  harness?: string;
  model?: string;
  variant?: string;
};

/**
 * The master orchestrator's answer. `action` is what it proposed, `threadId`
 * is only set when the daemon actually created the thread and started its
 * first turn, and `error` is set when the proposal failed validation.
 */
export type MasterResult = {
  reply: string;
  action: MasterAction | null;
  threadId?: string;
  projectId?: string;
  error?: string;
};

/** Sends one turn to the home-screen orchestrator; history is prior turns only. */
export function sendMaster(
  rpc: Rpc,
  text: string,
  history: MasterMessage[],
): Promise<MasterResult> {
  return rpc.call<MasterResult>("master.send", { text, history });
}
