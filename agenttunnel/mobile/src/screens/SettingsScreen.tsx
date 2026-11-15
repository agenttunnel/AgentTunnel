// Settings: this phone's label for the instance, the relay address used for the
// next pairing, the connection state, and unpair.
import { useState, type ReactNode } from "react";
import { Alert, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { Banner, Button, StatusDot } from "../components/ui";
import { normalizeRelayUrl, RelayError } from "../lib/relay";
import { errorMessage } from "../lib/normalize";
import type { DaemonState } from "../lib/useDaemon";
import { colors, fonts, fontSize, radius, space } from "../theme";

export function SettingsScreen({
  instanceName,
  relayUrl,
  pairedVia,
  status,
  onSaveName,
  onSaveRelay,
  onUnpair,
  onBack,
}: {
  instanceName: string;
  relayUrl: string;
  pairedVia: string | null;
  status: DaemonState["status"];
  onSaveName: (name: string) => Promise<void>;
  onSaveRelay: (url: string) => Promise<void>;
  onUnpair: () => Promise<void>;
  onBack: () => void;
}) {
  const insets = useSafeAreaInsets();
  const [name, setName] = useState(instanceName);
  const [relay, setRelay] = useState(relayUrl);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const saveName = async () => {
    setError(null);
    try {
      await onSaveName(name.trim());
      setNotice("Name saved");
    } catch (e) {
      setError(errorMessage(e));
    }
  };

  const saveRelay = async () => {
    setError(null);
    try {
      const url = normalizeRelayUrl(relay);
      await onSaveRelay(url);
      setRelay(url);
      setNotice("Relay address saved. It applies the next time you pair.");
    } catch (e) {
      setError(e instanceof RelayError ? e.message : errorMessage(e));
    }
  };

  const confirmUnpair = () => {
    Alert.alert(
      "Unpair this phone?",
      "This removes the saved token from this device. You can pair again with a new code.",
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Unpair",
          style: "destructive",
          onPress: () => {
            onUnpair().catch((e: unknown) => setError(errorMessage(e)));
          },
        },
      ],
    );
  };

  return (
    <ScrollView
      style={styles.root}
      contentContainerStyle={[styles.body, { paddingTop: insets.top + space.md, paddingBottom: insets.bottom + space.xl }]}
      keyboardShouldPersistTaps="handled"
    >
      <View style={styles.topRow}>
        <Pressable accessibilityRole="button" accessibilityLabel="Back to threads" onPress={onBack} style={styles.back}>
          <Text style={styles.backText}>Back</Text>
        </Pressable>
        <Text style={styles.title}>Settings</Text>
      </View>

      {error ? <Banner tone="err">{error}</Banner> : null}
      {notice ? <Banner tone="info">{notice}</Banner> : null}

      <Section title="Instance">
        <Text style={styles.label}>Name on this phone</Text>
        <TextInput
          value={name}
          onChangeText={setName}
          placeholder="My desktop"
          placeholderTextColor={colors.faint}
          style={styles.input}
          accessibilityLabel="Instance name"
          maxLength={64}
        />
        <Button label="Save name" onPress={saveName} />
      </Section>

      <Section title="Relay">
        <Text style={styles.label}>Relay address</Text>
        <TextInput
          value={relay}
          onChangeText={setRelay}
          placeholder="https://relay.example.com"
          placeholderTextColor={colors.faint}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
          style={styles.input}
          accessibilityLabel="Relay address"
        />
        <Button label="Save relay" onPress={saveRelay} />
      </Section>

      <Section title="Connection">
        <View style={styles.statusLine}>
          <StatusDot status={status === "open" ? "succeeded" : status === "closed" ? "failed" : "pending"} />
          <Text style={styles.value}>{statusWord(status)}</Text>
        </View>
        {pairedVia ? <Text style={styles.mono} numberOfLines={1}>{pairedVia}</Text> : null}
      </Section>

      <Section title="Pairing">
        <Button label="Unpair this phone" tone="danger" onPress={confirmUnpair} />
      </Section>
    </ScrollView>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <View style={styles.section}>
      <Text style={styles.sectionTitle}>{title}</Text>
      <View style={styles.sectionBody}>{children}</View>
    </View>
  );
}

function statusWord(status: DaemonState["status"]): string {
  switch (status) {
    case "open":
      return "Connected";
    case "connecting":
      return "Connecting";
    case "reconnecting":
      return "Reconnecting";
    case "closed":
      return "Disconnected";
    default:
      return "Not connected";
  }
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.bg,
  },
  body: {
    paddingHorizontal: space.lg,
    gap: space.md,
  },
  topRow: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.md,
    marginBottom: space.sm,
  },
  back: {
    paddingVertical: space.xs,
  },
  backText: {
    color: colors.accent,
    fontSize: fontSize.body,
  },
  title: {
    color: colors.text,
    fontSize: fontSize.md,
    fontWeight: "600",
  },
  section: {
    gap: space.xs,
  },
  sectionTitle: {
    color: colors.faint,
    fontSize: fontSize.xs,
    textTransform: "uppercase",
    letterSpacing: 0.6,
    marginLeft: space.xs,
  },
  sectionBody: {
    backgroundColor: colors.surface1,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.lg,
    padding: space.md,
    gap: space.sm,
  },
  label: {
    color: colors.dim,
    fontSize: fontSize.sm,
  },
  input: {
    backgroundColor: colors.bg,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
    color: colors.text,
    fontSize: fontSize.body,
  },
  statusLine: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.sm,
  },
  value: {
    color: colors.text,
    fontSize: fontSize.body,
  },
  mono: {
    color: colors.faint,
    fontSize: fontSize.sm,
    fontFamily: fonts.mono,
  },
});
