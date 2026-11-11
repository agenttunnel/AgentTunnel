// Wire types for the daemon's remote RPCs (internal/transport/rpc.go). The
// phone tunnel still rides the relay RPCs; remote.status wraps both remotes
// into one list so the sidebar can render them the same way.

import qrcode from "qrcode-generator";

export type RemoteState = "off" | "connecting" | "pairing" | "connected" | "error";

export type RemoteItem = {
  id: "phone" | "whatsapp";
  label: string;
  state: RemoteState;
  detail?: string;
  /** Payload to show while pairing: a QR data URL, or the text to encode. */
  qr?: string;
  /** This surface cannot connect in this build (e.g. WhatsApp credentials unset). */
  unavailable?: boolean;
};

export type RemoteStatus = { items: RemoteItem[] };

/** Status classes on the shared .dot element, keyed by remote state. */
export const REMOTE_DOT: Record<RemoteState, string> = {
  off: "pending",
  connecting: "running",
  pairing: "running",
  connected: "succeeded",
  error: "failed",
};

export function remoteStateLabel(state: RemoteState): string {
  switch (state) {
    case "off":
      return "Off";
    case "connecting":
      return "Connecting…";
    case "pairing":
      return "Pairing…";
    case "connected":
      return "Connected";
    case "error":
      return "Error";
  }
}

/**
 * qrSrc renders a pairing payload as an image. The daemon may already send a
 * data URL; otherwise the payload is encoded here so the client needs no
 * round-trip or extra asset. Encoding is cached by payload: the section re-reads
 * status on every poll, and an unchanged code must not be rebuilt each time.
 */
const qrCache = new Map<string, string>();

export function qrSrc(payload: string): string {
  if (!payload) return "";
  if (payload.startsWith("data:image/")) return payload;
  const cached = qrCache.get(payload);
  if (cached) return cached;
  const q = qrcode(0, "M");
  q.addData(payload);
  q.make();
  const src = q.createDataURL(4, 2);
  if (qrCache.size >= 32) qrCache.clear();
  qrCache.set(payload, src);
  return src;
}
