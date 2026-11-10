// Pair: relay address plus the code shown on the desktop. Code entry is the
// only path; QR scanning is out of scope (no camera dependency).
import { useState } from "react";
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { Banner, Button } from "../components/ui";
import { errorMessage } from "../lib/normalize";
import { exchangePairCode, normalizePairCode, normalizeRelayUrl, type Pairing } from "../lib/relay";
import { colors, fonts, fontSize, radius, space } from "../theme";

export function PairScreen({
  initialRelayUrl,
  notice,
  onPaired,
}: {
  initialRelayUrl: string;
  /** A failure from loading the saved session, shown until the user pairs. */
  notice?: string | null;
  onPaired: (pairing: Pairing) => Promise<void>;
}) {
  const insets = useSafeAreaInsets();
  const [relayUrl, setRelayUrl] = useState(initialRelayUrl);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const canSubmit = !busy && relayUrl.trim() !== "" && code.trim() !== "";

  const pair = async () => {
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    try {
      const relay = normalizeRelayUrl(relayUrl);
      const pairing = await exchangePairCode(relay, normalizePairCode(code));
      await onPaired(pairing);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <KeyboardAvoidingView
      style={[styles.root, { paddingTop: insets.top, paddingBottom: insets.bottom }]}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <ScrollView contentContainerStyle={styles.body} keyboardShouldPersistTaps="handled">
        <Text style={styles.brand}>AgentTunnel</Text>
        <Text style={styles.lead}>
          Pair this phone with your desktop. Open AgentTunnel on your computer, turn on remote access, and enter
          the code it shows.
        </Text>

        <Text style={styles.label}>Relay address</Text>
        <TextInput
          value={relayUrl}
          onChangeText={setRelayUrl}
          placeholder="https://relay.example.com"
          placeholderTextColor={colors.faint}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
          textContentType="URL"
          editable={!busy}
          style={styles.input}
          accessibilityLabel="Relay address"
        />

        <Text style={styles.label}>Pairing code</Text>
        <TextInput
          value={code}
          onChangeText={setCode}
          placeholder="ABCD-1234"
          placeholderTextColor={colors.faint}
          autoCapitalize="characters"
          autoCorrect={false}
          editable={!busy}
          onSubmitEditing={pair}
          returnKeyType="go"
          style={[styles.input, styles.code]}
          accessibilityLabel="Pairing code"
        />

        {error || notice ? (
          <View style={styles.errorWrap}>
            <Banner tone="err">{error ?? notice}</Banner>
          </View>
        ) : null}

        <Button
          label={busy ? "Pairing…" : error ? "Try again" : "Pair"}
          tone="primary"
          disabled={!canSubmit}
          onPress={pair}
          style={styles.submit}
        />
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.bg,
  },
  body: {
    padding: space.xxl,
    gap: space.sm,
    flexGrow: 1,
    justifyContent: "center",
  },
  brand: {
    color: colors.text,
    fontSize: fontSize.xl,
    fontWeight: "700",
    marginBottom: space.xs,
  },
  lead: {
    color: colors.dim,
    fontSize: fontSize.body,
    lineHeight: 19,
    marginBottom: space.lg,
  },
  label: {
    color: colors.faint,
    fontSize: fontSize.sm,
    textTransform: "uppercase",
    marginTop: space.sm,
  },
  input: {
    backgroundColor: colors.surface1,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingHorizontal: space.md,
    paddingVertical: space.sm + 2,
    color: colors.text,
    fontSize: fontSize.md,
  },
  code: {
    fontFamily: fonts.mono,
    letterSpacing: 2,
  },
  errorWrap: {
    marginTop: space.md,
  },
  submit: {
    marginTop: space.lg,
    minHeight: 44,
  },
});
