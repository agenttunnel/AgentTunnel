// Wire types for the daemon's relay RPCs (internal/transport/rpc.go).

export type RelayStatus = {
  enabled: boolean;
  url: string;
  connected: boolean;
  instance?: string;
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
