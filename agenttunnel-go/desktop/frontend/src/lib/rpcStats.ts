import { useCallback, useEffect, useState } from "react";
import type { Rpc } from "./rpc";

/**
 * Loads one stats RPC when the page mounts, and again only on refresh. Stats
 * are rollups over the whole environment, so they never poll.
 */
export function useStatsRpc<T>(rpc: Rpc | null, method: string, empty: T) {
  const [data, setData] = useState<T>(empty);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    if (!rpc) return;
    let live = true;
    setLoading(true);
    setError(null);
    rpc
      .call<T>(method)
      .then((next) => live && setData(next))
      .catch((e: unknown) => live && setError(e instanceof Error ? e.message : String(e)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [rpc, method, reload]);

  const refresh = useCallback(() => setReload((n) => n + 1), []);
  return { data, loading, error, refresh };
}
