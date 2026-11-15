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
 * turn, and `error` joins every reason an action did not run. `masterThreadId`
 * is the conversation the exchange was recorded into, set when the request named
 * one. `warning` is a non-fatal follow-up problem (the exchange ran, but a
 * side effect like saving the reply failed).
 */
export type MasterResult = {
  reply: string;
  action: MasterAction | null;
  actions?: MasterAction[];
  results?: MasterActionResult[];
  threadId?: string;
  projectId?: string;
  masterThreadId?: string;
  warning?: string;
  error?: string;
};

/** The master chat's runtime: the stored override over the Orchestrator role. */
export type MasterRuntime = {
  /** Stored override fields; empty means inherit the Orchestrator role. */
  harness: string;
  model: string;
  variant: string;
  /** What a master chat actually runs on today; pre-fills the picker. */
  effective: { harness: string; model: string; variant: string };
};

/** One past master conversation, as master.history.list reports it. */
export type MasterThreadSummary = {
  id: string;
  title: string;
  updatedAt: string;
  messages: number;
};

/** One message of a stored master conversation, oldest first. */
export type MasterHistoryMessage = MasterMessage & { at?: string };

/** master.history.open's payload: a conversation ready to reopen. */
export type MasterThreadDetail = {
  id: string;
  title: string;
  updatedAt: string;
  messages: MasterHistoryMessage[];
};

/**
 * Sends one turn to the home-screen orchestrator. With a `threadId` the server
 * records the exchange into that conversation and loads its own history;
 * without one the request is stateless and `history` is the prior turns.
 */
export function sendMaster(
  rpc: Rpc,
  text: string,
  threadId: string | null,
  history: MasterMessage[] = [],
): Promise<MasterResult> {
  return rpc.call<MasterResult>(
    "master.send",
    threadId ? { text, threadId } : { text, history },
  );
}

/** master.history.new: starts a durable master conversation. */
export function newMasterThread(rpc: Rpc, title?: string): Promise<MasterThreadDetail> {
  return rpc.call<MasterThreadDetail>("master.history.new", title ? { title } : {});
}

/** master.history.list: every master conversation, newest activity first. */
export function listMasterThreads(rpc: Rpc): Promise<MasterThreadSummary[]> {
  return rpc.call<MasterThreadSummary[]>("master.history.list", {});
}

/** master.history.open: one conversation's recorded messages, oldest first. */
export function openMasterThread(rpc: Rpc, threadId: string): Promise<MasterThreadDetail> {
  return rpc.call<MasterThreadDetail>("master.history.open", { threadId });
}

/** master.history.delete: removes a conversation permanently. */
export async function deleteMasterThread(rpc: Rpc, threadId: string): Promise<void> {
  await rpc.call("master.history.delete", { threadId });
}

/** master.runtime.get: the master chat's stored override and effective runtime. */
export function getMasterRuntime(rpc: Rpc): Promise<MasterRuntime> {
  return rpc.call<MasterRuntime>("master.runtime.get", {});
}

/** master.runtime.set: persists the full override; empty fields inherit. */
export function setMasterRuntime(
  rpc: Rpc,
  runtime: { harness: string; model: string; variant: string },
): Promise<MasterRuntime> {
  return rpc.call<MasterRuntime>("master.runtime.set", runtime);
}

/** master.reporter.get: whether the master reports status over WhatsApp. */
export async function getMasterReporter(rpc: Rpc): Promise<boolean> {
  const res = await rpc.call<{ enabled: boolean }>("master.reporter.get", {});
  return res.enabled;
}

/** master.reporter.set: turns reporter mode on or off. */
export async function setMasterReporter(rpc: Rpc, enabled: boolean): Promise<boolean> {
  const res = await rpc.call<{ enabled: boolean }>("master.reporter.set", { enabled });
  return res.enabled;
}
