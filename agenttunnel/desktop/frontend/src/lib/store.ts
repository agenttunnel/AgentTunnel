import { useCallback, useRef, useSyncExternalStore } from "react";

/** The same shape as a React state setter: a value or an updater. */
export type SetState<T> = (next: T | ((prev: T) => T)) => void;

/**
 * A small external store for state that changes often but is read by few
 * components. Only components that read a field re-render when it changes,
 * so a keystroke or a streamed chunk does not re-render the whole app.
 * Setters behave like React's, so state moved here keeps its call sites.
 */
export type Store<T extends object> = {
  get: () => T;
  subscribe: (listener: () => void) => () => void;
  setter: <K extends keyof T>(key: K) => SetState<T[K]>;
};

export function createStore<T extends object>(initial: T): Store<T> {
  let state = initial;
  const listeners = new Set<() => void>();
  return {
    get: () => state,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    setter: (key) => (next) => {
      const prev = state[key];
      const value = typeof next === "function" ? (next as (p: typeof prev) => typeof prev)(prev) : next;
      if (Object.is(value, prev)) return;
      state = { ...state, [key]: value };
      for (const listener of listeners) listener();
    },
  };
}

/** A callback whose identity never changes but always runs the latest `fn`.
 * Pass it to a memoized child so the parent's renders do not re-render it. */
export function useLatestCallback<A extends unknown[], R>(fn: (...args: A) => R): (...args: A) => R {
  const ref = useRef(fn);
  ref.current = fn;
  return useCallback((...args: A) => ref.current(...args), []);
}

/** Subscribes a component to one field of a store. */
export function useStoreField<T extends object, K extends keyof T>(store: Store<T>, key: K): T[K] {
  return useSyncExternalStore(store.subscribe, () => store.get()[key]);
}
