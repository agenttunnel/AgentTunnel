import { useCallback, useEffect, useMemo, useState } from "react";
import type { Rpc } from "../lib/rpc";
import { switchProps } from "../lib/switchProps";
import {
  pairUri,
  phoneRelayUrl,
  type PairOffer,
  type RelayDevice,
  type RelayStatus,
  type TunnelStatus,
} from "../lib/relayPairing";
import {
  REMOTE_DOT,
  qrSrc,
  remoteStateLabel,
  type RemoteItem,
  type RemoteStatus,
  type RemoteState,
} from "../lib/remote";

/** Poll only while a connection is settling, and never in a hidden window. */
const POLL_MS = 5000;

type PhonePairing = PairOffer & { url: string };

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** phoneItem mirrors the daemon's derivation for daemons that predate remote.status. */
function phoneItem(tunnel: TunnelStatus, relay: RelayStatus): RemoteItem {
  let state: RemoteState = "off";
  let detail = "No phone paired";
  if (tunnel.listening) {
    state = "connected";
    detail = tunnel.lanUrl ? `Reachable at ${tunnel.lanUrl}` : "Paired phones can reach this daemon";
  } else if (relay.enabled && relay.connected) {
    state = "connected";
    detail = "Connected through the relay";
  } else if (relay.enabled) {
    state = "connecting";
    detail = "Relay enabled, waiting for a connection";
  }
  return { id: "phone", label: "Phone", state, detail };
}

/**
 * RemoteSection is the sidebar's bottom-left control for remotes other than
 * this machine: the phone tunnel and the WhatsApp bridge. The daemon owns all
 * state; a row only mirrors what the RPCs report, and clicking it toggles.
 */
export function RemoteSection({ rpc }: { rpc: Rpc | null }) {
  const [items, setItems] = useState<RemoteItem[]>([]);
  const [pairing, setPairing] = useState<PhonePairing | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [advanced, setAdvanced] = useState(false);
  const [relay, setRelay] = useState<RelayStatus | null>(null);
  const [urlInput, setUrlInput] = useState("");
  const [devices, setDevices] = useState<RelayDevice[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<string | null>(null);

  const loadPhone = useCallback(async (client: Rpc): Promise<RemoteItem> => {
    const [tunnel, relayStatus] = await Promise.all([
      client.call<TunnelStatus>("relay.tunnel.status"),
      client.call<RelayStatus>("relay.status"),
    ]);
    setRelay(relayStatus);
    // Keep whatever the user is typing; only seed an empty field.
    setUrlInput((cur) => cur || relayStatus.url);
    return phoneItem(tunnel, relayStatus);
  }, []);

  const refresh = useCallback(async () => {
    if (!rpc) return;
    try {
      const status = await rpc.call<RemoteStatus>("remote.status");
      const next = [...(status.items ?? [])];
      if (!next.some((i) => i.id === "phone")) next.unshift(await loadPhone(rpc));
      setItems(next);
    } catch {
      // Older daemons have no remote.status; the phone tunnel RPCs still work.
      try {
        setItems([await loadPhone(rpc)]);
      } catch {
        /* keep the last reading; a failed poll must not blank the section */
      }
    }
  }, [rpc, loadPhone]);

  const loadDevices = useCallback(async (client: Rpc) => {
    try {
      const res = await client.call<{ devices: RelayDevice[] }>("relay.devices.list");
      setDevices(res.devices ?? []);
    } catch {
      /* an unreachable relay leaves the last list instead of erroring the section */
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // remote.* pushes arrive without an id; re-read instead of trusting a delta.
  useEffect(() => {
    if (!rpc) return;
    return rpc.onNotification(({ method }) => {
      if (method.startsWith("remote.")) void refresh();
    });
  }, [rpc, refresh]);

  const polling =
    !!pairing || items.some((i) => i.state === "connecting" || i.state === "pairing");
  useEffect(() => {
    if (!polling) return;
    const id = window.setInterval(() => {
      // A background window must not keep a dead relay in a request loop.
      if (!document.hidden) void refresh();
    }, POLL_MS);
    return () => window.clearInterval(id);
  }, [polling, refresh]);

  useEffect(() => {
    const onVisible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [refresh]);

  // A pairing code is single use and expires; never show a dead one.
  useEffect(() => {
    if (!pairing) return;
    const id = window.setTimeout(() => setPairing(null), Math.max(0, Date.parse(pairing.expires) - Date.now()));
    return () => window.clearTimeout(id);
  }, [pairing]);

  const act = async (id: string, fn: () => Promise<void>) => {
    setBusy(id);
    setError(null);
    setNotice(null);
    try {
      await fn();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(null);
    }
  };

  const connectPhone = () =>
    act("phone", async () => {
      if (!rpc) return;
      const tunnel = await rpc.call<TunnelStatus>("relay.tunnel.start");
      const status = await rpc.call<RelayStatus>("relay.status");
      const offer = await rpc.call<PairOffer>("relay.pair.start");
      const url = tunnel.listening && tunnel.lanUrl ? tunnel.lanUrl : phoneRelayUrl(status.url, status.lanHost);
      setPairing({ ...offer, url });
      if (advanced) await loadDevices(rpc);
      await refresh();
    });

  const disconnectPhone = () =>
    act("phone", async () => {
      if (!rpc) return;
      setPairing(null);
      await rpc.call("relay.tunnel.stop").catch(() => undefined);
      const status = await rpc.call<RelayStatus>("relay.status");
      if (status.enabled) await rpc.call<RelayStatus>("relay.disable");
      await refresh();
    });

  const connectWhatsApp = () =>
    act("whatsapp", async () => {
      if (!rpc) return;
      await rpc.call("remote.whatsapp.connect");
      await refresh();
    });

  const disconnectWhatsApp = () =>
    act("whatsapp", async () => {
      if (!rpc) return;
      await rpc.call("remote.whatsapp.disconnect");
      await refresh();
    });

  const toggle = (item: RemoteItem) => {
    if (busy || item.unavailable) return;
    const on = item.state !== "off" && item.state !== "error";
    if (item.id === "phone") void (on ? disconnectPhone() : connectPhone());
    else void (on ? disconnectWhatsApp() : connectWhatsApp());
  };

  // Control is a separate, off-by-default grant: it lets the account's own
  // chat drive the orchestrator, so it changes nothing about connectivity.
  const toggleControl = (item: RemoteItem) =>
    act(item.id, async () => {
      if (!rpc) return;
      const res = await rpc.call<{ enabled: boolean }>("remote.whatsapp.control", {
        enabled: !item.control,
      });
      setItems((cur) => cur.map((i) => (i.id === item.id ? { ...i, control: res.enabled } : i)));
    });

  const toggleAdvanced = () => {
    const next = !advanced;
    setAdvanced(next);
    setNotice(null);
    setConfirm(null);
    if (next && rpc) void loadDevices(rpc);
  };

  const saveRelayUrl = () =>
    act("phone", async () => {
      if (!rpc) return;
      const status = await rpc.call<RelayStatus>("relay.enable", { url: urlInput.trim() });
      setRelay(status);
      setUrlInput(status.url);
      setNotice("Relay URL saved.");
      await refresh();
    });

  const mintCode = () =>
    act("phone", async () => {
      if (!rpc) return;
      const status = await rpc.call<RelayStatus>("relay.status");
      if (!status.connected) throw new Error("The relay is not connected yet.");
      const offer = await rpc.call<PairOffer>("relay.pair.start");
      setPairing({ ...offer, url: phoneRelayUrl(status.url, status.lanHost) });
      await loadDevices(rpc);
    });

  const disableRelay = () =>
    act("phone", async () => {
      if (!rpc) return;
      setPairing(null);
      const status = await rpc.call<RelayStatus>("relay.disable");
      setRelay(status);
      setNotice("Relay disabled.");
      await refresh();
    });

  const unpair = (target: string | "all") => {
    if (confirm !== target) {
      setConfirm(target);
      return;
    }
    setConfirm(null);
    void act("phone", async () => {
      if (!rpc) return;
      const params = target === "all" ? { all: true } : { clientId: target };
      const res = await rpc.call<{ devices: RelayDevice[] }>("relay.unpair", params);
      setDevices(res.devices ?? []);
      setNotice("Phone unpaired.");
      await refresh();
    });
  };

  const qrByItem = useMemo(() => {
    const out: Record<string, string> = {};
    for (const item of items) {
      const payload = item.id === "phone" && pairing ? pairUri(pairing.url, pairing.code) : item.qr;
      if (payload) out[item.id] = qrSrc(payload);
    }
    return out;
  }, [items, pairing]);

  if (items.length === 0) return null;

  const urlChanged = urlInput.trim() !== "" && urlInput.trim() !== (relay?.url ?? "");

  return (
    <div className="remote">
      <div className="remote-title">Remote</div>
      {items.map((item) => {
        const active = busy === item.id;
        const unavailable = item.unavailable === true;
        const qr = qrByItem[item.id];
        const showQr = !!qr && (item.state === "pairing" || (item.id === "phone" && !!pairing));
        return (
          <div className="remote-item" key={item.id}>
            <button
              type="button"
              className="remote-row"
              onClick={() => toggle(item)}
              disabled={active || !rpc || unavailable}
              title={item.detail || undefined}
              aria-label={`${item.label}: ${unavailable ? "Not configured" : remoteStateLabel(item.state)}. ${
                unavailable
                  ? "Unavailable"
                  : item.state === "off" || item.state === "error"
                    ? "Connect"
                    : "Disconnect"
              }`}
            >
              <span className={`dot ${REMOTE_DOT[item.state]}`} aria-hidden="true" />
              <span className="remote-label">{item.label}</span>
              <span className={`remote-state ${item.state}`}>
                {active ? "Working…" : unavailable ? "Not configured" : remoteStateLabel(item.state)}
              </span>
            </button>
            {item.id === "whatsapp" && !unavailable && item.controlSupported && (
              <div className="remote-control">
                <div
                  className={`toggle ${item.control ? "on" : ""}`}
                  {...switchProps(!!item.control, () => {
                    if (!busy && rpc) void toggleControl(item);
                  })}
                  aria-label="Let WhatsApp messages control the orchestrator"
                  aria-disabled={active || !rpc}
                >
                  <span className="sw" /> Control
                </div>
                <p className="hint warn">
                  Grants remote agent execution: text you send in your WhatsApp self-chat can start and
                  steer runs.
                </p>
              </div>
            )}
            {item.id === "phone" && (
              <button type="button" className="remote-adv" onClick={toggleAdvanced} disabled={!rpc}>
                {advanced ? "Hide relay settings" : "Relay settings"}
              </button>
            )}
            {item.id === "phone" && advanced && (
              <div className="remote-adv-panel">
                <p className="hint">The relay operator can read traffic. Self-host it if that matters.</p>
                <label className="field">
                  <span>Relay URL (wss://, or ws:// for a private host)</span>
                  <div className="relay-url">
                    <input
                      value={urlInput}
                      onChange={(e) => setUrlInput(e.target.value)}
                      placeholder="wss://relay.example.com"
                      disabled={busy === "phone" || !!relay?.connected}
                    />
                    {urlChanged && !relay?.connected && (
                      <button onClick={saveRelayUrl} disabled={busy === "phone"}>
                        Save URL
                      </button>
                    )}
                  </div>
                </label>
                <div className="sheet-actions" style={{ marginTop: 0, marginBottom: 8 }}>
                  <button onClick={mintCode} disabled={busy === "phone" || !relay?.connected}>
                    New pairing code
                  </button>
                  {relay?.enabled && (
                    <button className="danger" onClick={disableRelay} disabled={busy === "phone"}>
                      Disable relay
                    </button>
                  )}
                </div>
                <h5 className="relay-sub">Paired phones</h5>
                {devices.length === 0 && <p className="hint">No phones paired yet.</p>}
                {devices.map((d) => (
                  <div className="relay-device" key={d.clientId}>
                    <span className="rn">{d.name || "Unnamed phone"}</span>
                    <span className="rd">
                      paired {d.pairedAt ? new Date(d.pairedAt).toLocaleDateString() : ""}
                    </span>
                    <button className="danger" onClick={() => unpair(d.clientId)} disabled={busy === "phone"}>
                      {confirm === d.clientId ? "Confirm unpair" : "Unpair"}
                    </button>
                  </div>
                ))}
                {devices.length > 1 && (
                  <div className="sheet-actions">
                    <button className="danger" onClick={() => unpair("all")} disabled={busy === "phone"}>
                      {confirm === "all" ? "Confirm unpair all" : "Unpair all"}
                    </button>
                  </div>
                )}
                {notice && <p className="hint">{notice}</p>}
              </div>
            )}
            {showQr && (
              <div className="remote-pair">
                <img className="remote-qr" src={qr} alt={`${item.label} pairing QR code`} width={132} height={132} />
                {item.id === "phone" && pairing && <span className="remote-code">{pairing.code}</span>}
              </div>
            )}
          </div>
        );
      })}
      {error && <p className="err-text remote-err">{error}</p>}
    </div>
  );
}
