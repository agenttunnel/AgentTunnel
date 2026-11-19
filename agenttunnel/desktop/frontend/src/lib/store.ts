import { useCallback, useRef, useSyncExternalStore } from "react";
import { getPref, setPref } from "./prefs";

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

/**
 * Per-thread UI state outside React, with the same rules as App's
 * useThreadScoped: a thread with no value of its own reads the default, a
 * write sets only that thread's value, and a write with no thread changes the
 * default. `storageKey` keeps the default across launches. Living outside
 * React, it outlives the panel that edits it, and an edit re-renders only the
 * components that read it.
 */
export type ThreadScoped<T> = {
  get: (threadId: string | null) => T;
  set: (threadId: string | null, next: T | ((prev: T) => T)) => void;
  setDefault: (value: T) => void;
  subscribe: (listener: () => void) => () => void;
};

export function createThreadScoped<T>(initial: () => T, storageKey?: string): ThreadScoped<T> {
  let fallback = initial();
  if (storageKey) {
    try {
      const raw = getPref(storageKey);
      if (raw !== null) fallback = JSON.parse(raw) as T;
    } catch {
      /* corrupt: use the built-in default */
    }
  }
  let byThread: Record<string, T> = {};
  const listeners = new Set<() => void>();
  const emit = () => listeners.forEach((l) => l());
  const setDefault = (value: T) => {
    if (Object.is(value, fallback)) return;
    fallback = value;
    if (storageKey) setPref(storageKey, JSON.stringify(value));
    emit();
  };
  return {
    get: (id) => (id !== null && id in byThread ? byThread[id] : fallback),
    set: (id, next) => {
      const apply = (prev: T) => (typeof next === "function" ? (next as (p: T) => T)(prev) : next);
      if (id === null) {
        setDefault(apply(fallback));
        return;
      }
      byThread = { ...byThread, [id]: apply(id in byThread ? byThread[id] : fallback) };
      emit();
    },
    setDefault,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}

/** Subscribes a component to one thread's value of a ThreadScoped store. */
export function useThreadScopedValue<T>(scoped: ThreadScoped<T>, threadId: string | null): T {
  return useSyncExternalStore(scoped.subscribe, () => scoped.get(threadId));
}
