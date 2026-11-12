import type { Rpc } from "./rpc";

/** One prior exchange sent with a master.send request, oldest first. */
export type MasterMessage = { role: "user" | "assistant"; text: string };

/** The operations the master orchestrator may propose. */
export type MasterActionType =
  | "start_thread"
  | "send_message"
  | "stop_thread"
  | "rename_thread"
  | "set_peer_mailbox"
  | "delete_thread"
  | "read_thread";

/** One proposed operation. Only the fields its type uses are set. */
export type MasterAction = {
  type: MasterActionType;
  projectId?: string;
  threadId?: string;
  title?: string;
  prompt?: string;
  harness?: string;
  model?: string;
  variant?: string;
  enabled?: boolean;
  confirm?: boolean;
};

/** What happened to one proposed action; `ok` false carries the reason. */
export type MasterActionResult = {
  type: MasterActionType;
  threadId?: string;
  projectId?: string;
  ok: boolean;
  error?: string;
};

/**
 * The master orchestrator's answer. `actions` are the validated proposals and
 * `results` holds one entry per rejected proposal and per executed action. `action`
 * is the first validated action, kept for clients that read a single one.
 * `threadId` is only set when the daemon created a thread and started its first
 * turn, and `error` joins every reason an action did not run.
 */
export type MasterResult = {
  reply: string;
  action: MasterAction | null;
  actions?: MasterAction[];
  results?: MasterActionResult[];
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
