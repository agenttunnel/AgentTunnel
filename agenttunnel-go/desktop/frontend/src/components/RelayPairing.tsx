import { useCallback, useEffect, useMemo, useState, type KeyboardEvent } from "react";
import qrcode from "qrcode-generator";
import type { Rpc } from "../lib/rpc";
import {
  pairUri,
  type PairOffer,
  type RelayDevice,
  type RelayStatus,
} from "../lib/relayPairing";

/** Relay state changes arrive only by polling, so poll while something is pending. */
const POLL_MS = 2000;

type LiveOffer = PairOffer & {
  /** Paired-device count when the code was minted. A higher count means it was used. */
  deviceCount: number;
};

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/**
 * RelayPairing is the Settings section that turns the phone relay on, mints a
 * pairing code, and lists or revokes paired phones. The daemon owns all state;
 * this component only mirrors what the relay RPCs report.
 */
export function RelayPairing({ rpc }: { rpc: Rpc | null }) {
  const [status, setStatus] = useState<RelayStatus | null>(null);
  const [devices, setDevices] = useState<RelayDevice[]>([]);
  const [urlInput, setUrlInput] = useState("");
  const [offer, setOffer] = useState<LiveOffer | null>(null);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!rpc) return;
    try {
      const [s, d] = await Promise.all([
        rpc.call<RelayStatus>("relay.status"),
        rpc.call<{ devices: RelayDevice[] }>("relay.devices.list"),
      ]);
      setStatus(s);
      setDevices(d.devices);
      setUrlInput((cur) => cur || s.url);
    } catch (e) {
      setError(errorText(e));
    }
  }, [rpc]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const polling = !!status?.enabled && (!status.connected || !!offer);
  useEffect(() => {
    if (!polling) return;
    const id = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(id);
  }, [polling, refresh]);

  // A code is single use and expires. Drop it at its deadline, or once a phone
  // has paired with it, so the screen never shows a code that no longer works.
  useEffect(() => {
    if (!offer) return;
    const id = window.setTimeout(() => setOffer(null), Math.max(0, Date.parse(offer.expires) - Date.now()));
    return () => window.clearTimeout(id);
  }, [offer]);
  useEffect(() => {
    if (offer && devices.length > offer.deviceCount) {
      setOffer(null);
      setNotice("A phone paired with the code.");
    }
  }, [devices, offer]);

  const qr = useMemo(() => {
    if (!offer || !status?.url) return "";
    const q = qrcode(0, "M");
    q.addData(pairUri(status.url, offer.code));
    q.make();
    return q.createDataURL(4, 2);
  }, [offer, status?.url]);

  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await fn();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const setEnabled = (on: boolean) =>
    act(async () => {
      if (!rpc) return;
      if (on) {
        setStatus(await rpc.call<RelayStatus>("relay.enable", { url: urlInput.trim() }));
      } else {
        setStatus(await rpc.call<RelayStatus>("relay.disable"));
        setOffer(null);
      }
    });

  const saveUrl = () =>
    act(async () => {
      if (!rpc) return;
      const s = await rpc.call<RelayStatus>("relay.enable", { url: urlInput.trim() });
      setStatus(s);
      setUrlInput(s.url);
    });

  const newCode = () =>
    act(async () => {
      if (!rpc) return;
      const [o, d] = await Promise.all([
        rpc.call<PairOffer>("relay.pair.start"),
        rpc.call<{ devices: RelayDevice[] }>("relay.devices.list"),
      ]);
      setDevices(d.devices);
      setOffer({ ...o, deviceCount: d.devices.length });
    });

  const unpair = (target: string | "all") => {
    if (confirm !== target) {
      setConfirm(target);
      return;
    }
    setConfirm(null);
    act(async () => {
      if (!rpc) return;
      const params = target === "all" ? { all: true } : { clientId: target };
      const r = await rpc.call<{ devices: RelayDevice[] }>("relay.unpair", params);
      setDevices(r.devices);
    });
  };

  const enabled = !!status?.enabled;
  const connected = !!status?.connected;
  const urlChanged = enabled && urlInput.trim() !== "" && urlInput.trim() !== status?.url;
  const stateLabel = !status ? "" : !enabled ? "Off" : connected ? "Connected" : "Connecting…";

  return (
    <>
      <p className="hint">
        Pair a phone through a relay. Off by default. The daemon only dials out, so nothing new
        listens on your network. The relay operator can read traffic, so self-host it if that
        matters to you.
      </p>
      <div className="relay-row">
        <div
          className={`toggle ${enabled ? "on" : ""}`}
          role="switch"
          aria-checked={enabled}
          aria-disabled={busy || !status}
          tabIndex={0}
          onClick={() => !busy && status && setEnabled(!enabled)}
          onKeyDown={(e: KeyboardEvent<HTMLDivElement>) => {
            if ((e.key === "Enter" || e.key === " ") && !busy && status) {
              e.preventDefault();
              setEnabled(!enabled);
            }
          }}
        >
          <span className="sw" /> Phone relay
        </div>
        <span className={`relay-state ${connected ? "ok" : ""}`}>{stateLabel}</span>
      </div>
      <label className="field" style={{ marginTop: 12 }}>
        <span>Relay URL (wss://, or ws:// for a loopback or private host)</span>
        <div className="relay-url">
          <input
            value={urlInput}
            onChange={(e) => setUrlInput(e.target.value)}
            placeholder="wss://relay.example.com"
            disabled={busy}
          />
          {urlChanged && (
            <button onClick={saveUrl} disabled={busy}>
              Save URL
            </button>
          )}
        </div>
      </label>

      {enabled && (
        <>
          <h5 className="relay-sub">Pairing</h5>
          {!connected && (
            <p className="hint">Waiting for the relay to accept this daemon. Codes need a connection.</p>
          )}
          <div className="sheet-actions" style={{ marginTop: 0, marginBottom: 12 }}>
            <button onClick={newCode} disabled={busy || !connected}>
              {offer ? "New code" : "Generate pairing code"}
            </button>
          </div>
          {offer && (
            <div className="relay-pair">
              <img className="relay-qr" src={qr} alt="Pairing QR code" width={176} height={176} />
              <div className="relay-code-col">
                <div className="relay-code">{offer.code}</div>
                <p className="hint">
                  Scan the QR code, or enter the relay URL and this code on the phone. Single use.
                  Expires at{" "}
                  {new Date(offer.expires).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}.
                </p>
              </div>
            </div>
          )}
        </>
      )}

      <h5 className="relay-sub">Paired phones</h5>
      {devices.length === 0 && <p className="hint">No phones paired yet.</p>}
      {devices.map((d) => (
        <div className="relay-device" key={d.clientId}>
          <span className="rn">{d.name || "Unnamed phone"}</span>
          <span className="rd">
            paired {d.pairedAt ? new Date(d.pairedAt).toLocaleDateString() : ""}
          </span>
          <button
            className="danger"
            onClick={() => unpair(d.clientId)}
            disabled={busy}
          >
            {confirm === d.clientId ? "Confirm unpair" : "Unpair"}
          </button>
        </div>
      ))}
      {devices.length > 1 && (
        <div className="sheet-actions">
          <button className="danger" onClick={() => unpair("all")} disabled={busy}>
            {confirm === "all" ? "Confirm unpair all" : "Unpair all"}
          </button>
        </div>
      )}

      {notice && <p className="hint">{notice}</p>}
      {error && <p className="err-text">{error}</p>}
    </>
  );
}
