// URL routing for the web UI: the open thread (or project board) lives in the
// path, so a reload restores it, Back works, and a thread can be linked. The
// daemon serves the app for any unknown path (internal/webui), so a deep link
// loads the same page. The desktop shell has no address bar and its asset
// server knows only real files, so routing stays off there.

import { useEffect, useRef } from "react";

export type Route = { kind: "thread"; id: string } | { kind: "project"; id: string } | { kind: "home" };

/** parseRoute reads /threads/<id> and /projects/<id>; anything else is home. */
export function parseRoute(path: string): Route {
  const m = /^\/(threads|projects)\/([^/]+)\/?$/.exec(path);
  if (!m) return { kind: "home" };
  let id: string;
  try {
    id = decodeURIComponent(m[2]);
  } catch {
    return { kind: "home" };
  }
  return { kind: m[1] === "threads" ? "thread" : "project", id };
}

/** routePath is the path for what the app shows: a thread wins over its project. */
export function routePath(projectId: string | null, threadId: string | null): string {
  if (threadId) return `/threads/${encodeURIComponent(threadId)}`;
  if (projectId) return `/projects/${encodeURIComponent(projectId)}`;
  return "/";
}

/**
 * useUrlRoute keeps the path and the app's place in step. `loaded` turns true
 * once threads and projects are known, which is when the path the page opened
 * on can be resolved. `open` applies a route and returns false when it names
 * nothing known; the path then falls back to what the app shows.
 */
export function useUrlRoute({
  enabled,
  loaded,
  projectId,
  threadId,
  open,
}: {
  enabled: boolean;
  loaded: boolean;
  projectId: string | null;
  threadId: string | null;
  open: (route: Route) => boolean;
}) {
  // The route the page was opened on, until the data to resolve it arrives.
  const initial = useRef<Route | null>(enabled ? parseRoute(window.location.pathname) : null);
  const openRef = useRef(open);
  openRef.current = open;
  const current = useRef(routePath(projectId, threadId));
  current.current = routePath(projectId, threadId);

  // State to URL. Declared before the initial resolve so the render that
  // resolves it does not push the still-empty state over the deep link.
  useEffect(() => {
    if (!enabled || initial.current) return;
    const path = routePath(projectId, threadId);
    if (path !== window.location.pathname) window.history.pushState(null, "", path);
  }, [enabled, projectId, threadId]);

  useEffect(() => {
    if (!enabled || !loaded || !initial.current) return;
    const route = initial.current;
    initial.current = null;
    if (route.kind === "home" || !openRef.current(route)) {
      window.history.replaceState(null, "", current.current);
    }
  }, [enabled, loaded]);

  // Back and Forward: the browser already moved the path; follow it.
  useEffect(() => {
    if (!enabled) return;
    const onPop = () => {
      if (initial.current) return;
      if (!openRef.current(parseRoute(window.location.pathname))) {
        window.history.replaceState(null, "", current.current);
      }
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, [enabled]);
}
