// Wire types for the daemon's relay RPCs (internal/transport/rpc.go).

export type RelayStatus = {
  enabled: boolean;
  url: string;
  connected: boolean;
  instance?: string;
  /** This machine's private IPv4 address, when it has one. */
  lanHost?: string;
};

export type PairOffer = {
  code: string;
  /** RFC3339. The relay stops accepting the code at this time. */
  expires: string;
};

export type RelayDevice = {
  clientId: string;
  name: string;
  pairedAt: string;
};

/**
 * pairUri is the QR payload for a pairing code. It carries the same {relay,
 * code} pair a phone takes from manual entry, so both paths feed one `pair` frame.
 */
export function pairUri(relay: string, code: string): string {
  return `agenttunnel://pair?relay=${encodeURIComponent(relay)}&code=${encodeURIComponent(code)}`;
}

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "localhost", "[::1]"]);

/**
 * phoneRelayUrl is the relay address a phone should dial. A saved loopback
 * address is unreachable from a phone, so it swaps in this machine's LAN host,
 * keeping the port. Any other address, or no LAN host, is returned unchanged.
 * The saved URL itself never changes.
 */
export function phoneRelayUrl(relay: string, lanHost: string | undefined): string {
  if (!lanHost) return relay;
  let u: URL;
  try {
    u = new URL(relay);
  } catch {
    return relay;
  }
  if (!LOOPBACK_HOSTS.has(u.hostname)) return relay;
  u.hostname = lanHost;
  return u.toString().replace(/\/$/, "");
}
