// Undo and redo for reversible changes (rename, archive, auto mode, peer
// messages). Each entry carries closures that issue the forward and inverse
// RPCs, so the stack knows nothing about threads or the wire. A step moves the
// entry before its RPC runs and moves it back if the RPC is refused, so a
// failed step leaves both stacks as they were. Steps are serialized.

import { useCallback, useRef, useState } from "react";
import { useLatestCallback } from "./store";

export type HistoryEntry = {
  /** What the change was, e.g. "rename" or "auto mode on". */
  label: string;
  /** Reverts the change. Rejects when the daemon refuses the call. */
  undo: () => Promise<void>;
  /** Applies the change again. */
  redo: () => Promise<void>;
};

type Dir = "undo" | "redo";
type Stacks = Record<Dir, HistoryEntry[]>;

/** Oldest changes fall off first. */
export const HISTORY_LIMIT = 50;

/** Moves an entry from one stack to the other, by identity. */
function shift(s: Stacks, entry: HistoryEntry, from: Dir): Stacks {
  const to: Dir = from === "undo" ? "redo" : "undo";
  return { ...s, [from]: s[from].filter((e) => e !== entry), [to]: [...s[to], entry] };
}

export function useHistory({ onError, onApplied }: { onError: (message: string) => void; onApplied: () => void }) {
  const [stacks, setStacks] = useState<Stacks>({ undo: [], redo: [] });
  const ref = useRef(stacks);
  const reportError = useLatestCallback(onError);
  const applied = useLatestCallback(onApplied);

  const commit = useCallback((next: Stacks) => {
    ref.current = next;
    setStacks(next);
  }, []);

  /** A new change ends the redo branch, as in an editor. */
  const record = useCallback(
    (entry: HistoryEntry) => {
      const cur = ref.current;
      commit({ undo: [...cur.undo, entry].slice(-HISTORY_LIMIT), redo: [] });
    },
    [commit],
  );

  // Steps run one at a time, so quick repeated presses each take the next
  // entry rather than being dropped. A refused step cancels the ones queued
  // behind it, which would only hit the same entry again.
  const queue = useRef<Promise<void>>(Promise.resolve());
  const epoch = useRef(0);

  const step = useCallback(
    (dir: Dir) => {
      const queuedAt = epoch.current;
      const run = async () => {
        if (queuedAt !== epoch.current) return;
        const cur = ref.current;
        const entry = cur[dir][cur[dir].length - 1];
        if (!entry) return;
        commit(shift(cur, entry, dir));
        try {
          await entry[dir]();
          applied();
        } catch (e) {
          epoch.current++;
          commit(shift(ref.current, entry, dir === "undo" ? "redo" : "undo"));
          reportError(e instanceof Error ? e.message : String(e));
        }
      };
      queue.current = queue.current.then(run);
      return queue.current;
    },
    [commit, applied, reportError],
  );

  const undo = useCallback(() => step("undo"), [step]);
  const redo = useCallback(() => step("redo"), [step]);
  const top = (dir: Dir) => stacks[dir][stacks[dir].length - 1];

  return {
    canUndo: stacks.undo.length > 0,
    canRedo: stacks.redo.length > 0,
    undoLabel: top("undo")?.label ?? "",
    redoLabel: top("redo")?.label ?? "",
    record,
    undo,
    redo,
  };
}
