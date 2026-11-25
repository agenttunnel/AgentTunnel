// The live fold keeps an agent's harness session current, so the Resume
// button appears without refetching the thread.
// Run with: npm test (bun test tests, no extra dependencies).
import { expect, test } from "bun:test";
import { foldThreadEvent } from "../src/lib/threadFold";

type State = Parameters<typeof foldThreadEvent>[0];

test("an observed session lands on its agent", () => {
  const state = {
    version: 4,
    agents: { a1: { id: "a1", harness: "claude", model: "", status: "running" } },
  } as unknown as State;
  const res = foldThreadEvent(state, {
    seq: 5,
    threadId: "t1",
    type: "agent.session-observed",
    at: "2026-11-25T10:00:00Z",
    payload: { agentId: "a1", session: "run-9" },
  });
  expect(res.kind).toBe("applied");
  if (res.kind !== "applied") return;
  expect((res.state.agents as Record<string, { harnessSession?: string }>).a1.harnessSession).toBe("run-9");
});

test("a session for an unknown agent asks for a refetch", () => {
  const state = { version: 0, agents: {} } as unknown as State;
  const res = foldThreadEvent(state, {
    seq: 1,
    threadId: "t1",
    type: "agent.session-observed",
    at: "2026-11-25T10:00:00Z",
    payload: { agentId: "ghost", session: "run-1" },
  });
  expect(res.kind).toBe("refetch");
});
