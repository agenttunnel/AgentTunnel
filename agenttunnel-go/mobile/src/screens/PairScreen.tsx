// Pair: relay address plus the code shown on the desktop. The desktop's QR
// code fills both fields when scanned; typing them stays the fallback.
import { CameraView, useCameraPermissions, type BarcodeScanningResult } from "expo-camera";
import { useRef, useState } from "react";
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { Banner, Button } from "../components/ui";
import { errorMessage } from "../lib/normalize";
import {
  defaultDeviceName,
  exchangePairCode,
  normalizePairCode,
  normalizeRelayUrl,
  parsePairUri,
  type Pairing,
} from "../lib/relay";
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
  const [deviceName, setDeviceName] = useState(() => defaultDeviceName(Platform.OS));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [scanning, setScanning] = useState(false);
  const [permission, requestPermission] = useCameraPermissions();
  // A camera can report the same code several times before the view unmounts.
  const scanDone = useRef(false);

  const startScan = async () => {
    setError(null);
    if (!permission?.granted) {
      const answer = await requestPermission();
      if (!answer.granted) {
        setError("Camera access is off. Allow it in Settings, or type the code instead.");
        return;
      }
    }
    scanDone.current = false;
    setScanning(true);
  };

  // Scanning only fills the fields. The user still taps Pair, so a stray code
  // in view cannot start a pairing.
  const onScanned = ({ data }: BarcodeScanningResult) => {
    if (scanDone.current) return;
    scanDone.current = true;
    setScanning(false);
    try {
      const parsed = parsePairUri(data);
      setRelayUrl(parsed.relay);
      setCode(parsed.code);
    } catch (e) {
      setError(errorMessage(e));
    }
  };

  const canSubmit = !busy && relayUrl.trim() !== "" && code.trim() !== "";

  const pair = async () => {
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    try {
      const relay = normalizeRelayUrl(relayUrl);
      const pairing = await exchangePairCode(relay, normalizePairCode(code), { deviceName });
      await onPaired(pairing);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  if (scanning) {
    return (
      <View style={[styles.root, { paddingTop: insets.top, paddingBottom: insets.bottom }]}>
        <CameraView
          style={StyleSheet.absoluteFill}
          facing="back"
          barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
          onBarcodeScanned={onScanned}
        />
        <View style={styles.scanBar}>
          <Text style={styles.lead}>Point the camera at the QR code on your desktop.</Text>
          <Button label="Cancel" onPress={() => setScanning(false)} />
        </View>
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      style={[styles.root, { paddingTop: insets.top, paddingBottom: insets.bottom }]}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <ScrollView contentContainerStyle={styles.body} keyboardShouldPersistTaps="handled">
        <Text style={styles.brand}>AgentTunnel</Text>
        <Text style={styles.lead}>
          Pair this phone with your desktop. Open AgentTunnel on your computer, turn on the phone relay, and scan
          the QR code it shows, or enter the code.
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
        <Button label="Scan QR code" onPress={startScan} disabled={busy} style={styles.scan} />

        <Text style={styles.label}>Device name</Text>
        <TextInput
          value={deviceName}
          onChangeText={setDeviceName}
          placeholder="Phone"
          placeholderTextColor={colors.faint}
          autoCorrect={false}
          editable={!busy}
          maxLength={64}
          style={styles.input}
          accessibilityLabel="Device name"
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
  scan: {
    marginTop: space.sm,
    minHeight: 44,
  },
  scanBar: {
    position: "absolute",
    left: 0,
    right: 0,
    bottom: 0,
    padding: space.xxl,
    gap: space.sm,
    backgroundColor: colors.bg,
  },
});
