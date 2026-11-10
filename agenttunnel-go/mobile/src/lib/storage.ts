// Device persistence. The pairing token lives in the platform keystore
// (expo-secure-store). Everything else is ordinary preference data in
// AsyncStorage. Nothing here logs, and no error message carries the token.
import AsyncStorage from "@react-native-async-storage/async-storage";
import * as SecureStore from "expo-secure-store";

import type { Pairing } from "./relay";

const TOKEN_KEY = "at.token";
const PAIRING_KEY = "at.pairing";
const RELAY_KEY = "at.relayUrl";
const NAME_KEY = "at.instanceName";
const LAST_KEY = "at.last";

export type LastSelection = { projectId?: string; threadId?: string };

export type Session = {
  pairing: Pairing | null;
  /** Relay address prefilled on the Pair screen. */
  relayUrl: string;
  instanceName: string;
  last: LastSelection;
};

type StoredPairing = { relayUrl: string; wsUrl: string };

export async function loadSession(): Promise<Session> {
  const [token, rawPairing, relayUrl, instanceName, rawLast] = await Promise.all([
    SecureStore.getItemAsync(TOKEN_KEY),
    AsyncStorage.getItem(PAIRING_KEY),
    AsyncStorage.getItem(RELAY_KEY),
    AsyncStorage.getItem(NAME_KEY),
    AsyncStorage.getItem(LAST_KEY),
  ]);
  const stored = parseJSON<StoredPairing>(rawPairing);
  const pairing: Pairing | null =
    token && stored?.wsUrl && stored.relayUrl
      ? { relayUrl: stored.relayUrl, wsUrl: stored.wsUrl, token }
      : null;
  return {
    pairing,
    relayUrl: relayUrl ?? "",
    instanceName: instanceName ?? "",
    last: parseJSON<LastSelection>(rawLast) ?? {},
  };
}

export async function savePairing(pairing: Pairing): Promise<void> {
  await SecureStore.setItemAsync(TOKEN_KEY, pairing.token);
  const meta: StoredPairing = { relayUrl: pairing.relayUrl, wsUrl: pairing.wsUrl };
  await AsyncStorage.setItem(PAIRING_KEY, JSON.stringify(meta));
}

/** Forgets this device's pairing. The last-selection memory goes with it. */
export async function clearPairing(): Promise<void> {
  await SecureStore.deleteItemAsync(TOKEN_KEY);
  await AsyncStorage.multiRemove([PAIRING_KEY, LAST_KEY]);
}

export async function saveRelayUrl(relayUrl: string): Promise<void> {
  await AsyncStorage.setItem(RELAY_KEY, relayUrl);
}

export async function saveInstanceName(name: string): Promise<void> {
  await AsyncStorage.setItem(NAME_KEY, name);
}

export async function saveLastSelection(last: LastSelection): Promise<void> {
  await AsyncStorage.setItem(LAST_KEY, JSON.stringify(last));
}

function parseJSON<T>(raw: string | null): T | null {
  if (!raw) return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}
