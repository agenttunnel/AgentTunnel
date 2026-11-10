// Root: loads the saved session, owns which screen is up, and keeps the daemon
// connection alive while paired. Navigation is a single view state; there is
// no router dependency for four screens.
import { StatusBar } from "expo-status-bar";
import { useCallback, useEffect, useState } from "react";
import { BackHandler, StyleSheet, View } from "react-native";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { ChatScreen } from "./src/screens/ChatScreen";
import { PairScreen } from "./src/screens/PairScreen";
import { SettingsScreen } from "./src/screens/SettingsScreen";
import { ThreadsScreen } from "./src/screens/ThreadsScreen";
import { errorMessage } from "./src/lib/normalize";
import { type Pairing } from "./src/lib/relay";
import {
  clearPairing,
  loadSession,
  saveInstanceName,
  saveLastSelection,
  savePairing,
  saveRelayUrl,
} from "./src/lib/storage";
import { useDaemon } from "./src/lib/useDaemon";
import { colors } from "./src/theme";

type Screen = "pair" | "threads" | "chat" | "settings";

export default function App() {
  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      <Root />
    </SafeAreaProvider>
  );
}

function Root() {
  const [ready, setReady] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [pairing, setPairing] = useState<Pairing | null>(null);
  const [relayUrl, setRelayUrl] = useState("");
  const [instanceName, setInstanceName] = useState("");
  const [initialThreadId, setInitialThreadId] = useState<string | null>(null);
  const [view, setView] = useState<Screen>("pair");

  const [daemon, actions] = useDaemon(pairing, initialThreadId);

  useEffect(() => {
    loadSession()
      .then((s) => {
        setPairing(s.pairing);
        setRelayUrl(s.relayUrl);
        setInstanceName(s.instanceName);
        setInitialThreadId(s.last.threadId ?? null);
        // Resume the last thread on launch; the chat shows its own connecting state.
        setView(s.pairing ? (s.last.threadId ? "chat" : "threads") : "pair");
        setReady(true);
      })
      .catch((e: unknown) => {
        setLoadError(errorMessage(e));
        setReady(true);
      });
  }, []);

  // Android back: leave a sub-screen before the app closes.
  useEffect(() => {
    const sub = BackHandler.addEventListener("hardwareBackPress", () => {
      if (view === "chat" || view === "settings") {
        setView("threads");
        return true;
      }
      return false;
    });
    return () => sub.remove();
  }, [view]);

  const onPaired = useCallback(async (next: Pairing) => {
    await savePairing(next);
    setPairing(next);
    setRelayUrl(next.relayUrl);
    setView("threads");
  }, []);

  const openThread = useCallback(
    (threadId: string, projectId?: string) => {
      actions.selectThread(threadId);
      const selection = { projectId, threadId };
      saveLastSelection(selection).catch(() => undefined);
      setView("chat");
    },
    [actions],
  );

  const unpair = useCallback(async () => {
    await clearPairing();
    setInitialThreadId(null);
    setPairing(null);
    setView("pair");
  }, []);

  const saveName = useCallback(async (name: string) => {
    await saveInstanceName(name);
    setInstanceName(name);
  }, []);

  const saveRelay = useCallback(async (url: string) => {
    await saveRelayUrl(url);
    setRelayUrl(url);
  }, []);

  if (!ready) return <View style={styles.root} />;

  if (!pairing || view === "pair") {
    return <PairScreen initialRelayUrl={relayUrl} notice={loadError} onPaired={onPaired} />;
  }

  const title = instanceName.trim() || "AgentTunnel";
  const activeThread = daemon.threads.find((t) => t.id === daemon.activeId);

  if (view === "settings") {
    return (
      <SettingsScreen
        instanceName={instanceName}
        relayUrl={relayUrl}
        pairedVia={pairing.relayUrl}
        status={daemon.status}
        onSaveName={saveName}
        onSaveRelay={saveRelay}
        onUnpair={unpair}
        onBack={() => setView("threads")}
      />
    );
  }

  if (view === "chat") {
    return (
      <ChatScreen
        title={activeThread?.title ?? ""}
        state={daemon}
        actions={actions}
        onBack={() => setView("threads")}
      />
    );
  }

  return (
    <ThreadsScreen
      title={title}
      state={daemon}
      actions={actions}
      onOpenThread={openThread}
      onOpenSettings={() => setView("settings")}
    />
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.bg,
  },
});
