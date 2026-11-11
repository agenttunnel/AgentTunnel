import { useEffect, useState } from "react";
import type { TunnelStatus } from "../lib/relayPairing";
import type { Rpc } from "../lib/rpc";

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** Starts or stops the in-app phone tunnel. It sits in the titlebar so it is
 * one click away on every screen. */
export function TunnelButton({ rpc }: { rpc: Rpc | null }) {
  const [tunnel, setTunnel] = useState<TunnelStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!rpc) return;
    rpc
      .call<TunnelStatus>("relay.tunnel.status")
      .then(setTunnel)
      .catch((e: unknown) => setError(errorText(e)));
  }, [rpc]);

  const on = !!tunnel?.listening;

  const toggle = async () => {
    if (!rpc || busy) return;
    setBusy(true);
    setError(null);
    try {
      setTunnel(await rpc.call<TunnelStatus>(on ? "relay.tunnel.stop" : "relay.tunnel.start"));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const hint = error
    ? error
    : on && tunnel?.lanUrl
      ? `Phones on this network connect to ${tunnel.lanUrl}`
      : "Let phones on this network reach AgentTunnel";

  return (
    <button
      type="button"
      className={`tunnel-chip${on ? " on" : ""}`}
      onClick={toggle}
      disabled={!rpc || !tunnel || busy}
      title={hint}
    >
      <span className="led" />
      {on ? "Phone tunnel on" : "Phone tunnel off"}
    </button>
  );
}
