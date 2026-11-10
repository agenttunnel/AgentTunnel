// Chat: one thread's transcript, streamed output, peer mailbox, turn status,
// stop, and the composer. Stop only shows while a turn is running; the
// composer steers when a turn is already running (as the desktop does).
import { useMemo, useRef, useState } from "react";
import {
  FlatList,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
  type NativeScrollEvent,
  type NativeSyntheticEvent,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { Banner, Button, CollapsedOutput, Markdown, StatusDot } from "../components/ui";
import {
  agentLabel,
  isTurnActive,
  mailboxLine,
  type LiveLine,
  type ThreadSnapshot,
  type WireMessage,
  type WireMailbox,
} from "../lib/normalize";
import type { DaemonActions, DaemonState } from "../lib/useDaemon";
import { colors, fonts, fontSize, radius, space } from "../theme";

type Item =
  | { key: string; kind: "message"; msg: WireMessage }
  | { key: string; kind: "mail"; mail: WireMailbox }
  | { key: string; kind: "live"; line: LiveLine };

/** Within the transcript, messages and mailbox lines interleave by time. */
function buildItems(snapshot: ThreadSnapshot | null, live: LiveLine[]): Item[] {
  const timed: { at: number; item: Item }[] = [];
  if (snapshot) {
    snapshot.messages.forEach((msg, i) =>
      timed.push({ at: Date.parse(msg.at) || 0, item: { key: `m${i}-${msg.id}`, kind: "message", msg } }),
    );
    snapshot.mailbox.forEach((mail, i) =>
      timed.push({ at: Date.parse(mail.at) || 0, item: { key: `x${i}-${mail.at}`, kind: "mail", mail } }),
    );
  }
  timed.sort((a, b) => a.at - b.at);
  const out = timed.map((t) => t.item);
  live.forEach((line, i) => out.push({ key: `l${i}`, kind: "live", line }));
  return out;
}

export function ChatScreen({
  title,
  state,
  actions,
  onBack,
}: {
  title: string;
  state: DaemonState;
  actions: DaemonActions;
  onBack: () => void;
}) {
  const insets = useSafeAreaInsets();
  const [draft, setDraft] = useState("");
  const [stopping, setStopping] = useState(false);
  const listRef = useRef<FlatList<Item>>(null);
  const stickRef = useRef(true);

  const snapshot = state.snapshot;
  const agents = snapshot?.agents ?? {};
  const active = snapshot ? isTurnActive(snapshot) : false;
  const busy = active || state.turnsInFlight > 0;
  const connected = state.status === "open";
  const canSend = !!state.activeId && draft.trim() !== "" && state.status !== "closed";

  const items = useMemo(() => buildItems(snapshot, state.live), [snapshot, state.live]);

  const onScroll = (e: NativeSyntheticEvent<NativeScrollEvent>) => {
    const { contentOffset, layoutMeasurement, contentSize } = e.nativeEvent;
    stickRef.current = contentOffset.y + layoutMeasurement.height >= contentSize.height - 80;
  };

  const send = async () => {
    const text = draft.trim();
    if (!text || !state.activeId) return;
    setDraft("");
    const ok = await actions.sendTurn(text, busy);
    if (!ok) setDraft((cur) => (cur === "" ? text : cur));
  };

  const stop = async () => {
    setStopping(true);
    try {
      await actions.stopTurn();
    } finally {
      setStopping(false);
    }
  };

  const renderItem = ({ item }: { item: Item }) => {
    if (item.kind === "message") {
      const m = item.msg;
      if (m.author === "user") {
        return (
          <View style={styles.userRow}>
            <View style={styles.userBubble}>
              <Text style={styles.userText} selectable>
                {m.text}
              </Text>
            </View>
          </View>
        );
      }
      if (m.author === "steering") {
        return (
          <View style={styles.steerRow}>
            <Text style={styles.steerLabel}>steer</Text>
            <Text style={styles.steerText}>{m.text}</Text>
          </View>
        );
      }
      return (
        <View style={styles.agentBlock}>
          <Text style={styles.agentName}>{agentLabel(agents[m.author], m.author)}</Text>
          <Markdown source={m.text} />
        </View>
      );
    }
    if (item.kind === "mail") {
      const { from, to } = mailboxLine(item.mail, agents);
      return (
        <View style={styles.mailRow}>
          <Text style={styles.mailHead} numberOfLines={1}>
            {from} <Text style={styles.mailArrow}>→</Text> {to}
            <Text style={styles.mailKind}>{`  ${item.mail.kind}`}</Text>
          </Text>
          <Text style={styles.mailBody}>{item.mail.body}</Text>
        </View>
      );
    }
    const { line } = item;
    const label = agentLabel(agents[line.agentId], line.agentId || "agent");
    if (line.kind === "usage") return null;
    if (line.kind === "error") {
      return <CollapsedOutput label={`${label} error`} text={line.text} tone="error" />;
    }
    if (line.kind === "tool" || line.kind === "raw") {
      return <CollapsedOutput label={`${label} · ${line.kind}`} text={line.text} tone="tool" />;
    }
    return (
      <View style={styles.agentBlock}>
        <Text style={styles.agentName}>{label}</Text>
        <Markdown source={line.text} dim={line.kind === "reasoning"} />
      </View>
    );
  };

  if (!state.activeId) {
    return (
      <View style={[styles.root, { paddingTop: insets.top }]}>
        <Header title={title} onBack={onBack} />
        <Text style={styles.empty}>{connected ? "No thread is open." : "Connecting…"}</Text>
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      style={[styles.root, { paddingTop: insets.top }]}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <Header
        title={snapshot?.title || title}
        onBack={onBack}
        dot={busy ? "running" : "pending"}
        turn={snapshot ? `Turn ${snapshot.turn}` : undefined}
      />

      {state.status !== "open" && state.status !== "idle" ? (
        <Banner tone={state.status === "closed" ? "err" : "warn"}>
          {state.statusError?.message ?? "Reconnecting to your instance…"}
          {state.queued > 0 ? ` ${state.queued} message${state.queued === 1 ? "" : "s"} waiting.` : ""}
        </Banner>
      ) : null}
      {state.status === "open" && state.daemon === "offline" ? (
        <Banner tone="warn">Your computer is offline. Sends fail until it reconnects.</Banner>
      ) : null}
      {state.snapshotError ? <Banner tone="err">{state.snapshotError}</Banner> : null}
      {state.sendError ? (
        <Pressable onPress={actions.clearSendError} accessibilityRole="button" accessibilityLabel="Dismiss error">
          <Banner tone="err">{state.sendError}</Banner>
        </Pressable>
      ) : null}
      {snapshot && !snapshot.exists ? <Banner tone="warn">This thread no longer exists.</Banner> : null}

      <FlatList
        ref={listRef}
        data={items}
        keyExtractor={(i) => i.key}
        renderItem={renderItem}
        onScroll={onScroll}
        scrollEventThrottle={64}
        onContentSizeChange={() => {
          if (stickRef.current) listRef.current?.scrollToEnd({ animated: false });
        }}
        initialNumToRender={24}
        maxToRenderPerBatch={24}
        windowSize={10}
        removeClippedSubviews
        contentContainerStyle={styles.list}
        keyboardShouldPersistTaps="handled"
        ListEmptyComponent={
          snapshot ? <Text style={styles.empty}>Send a message to start the turn.</Text> : undefined
        }
      />

      <View style={[styles.footer, { paddingBottom: Math.max(insets.bottom, space.sm) }]}>
        <View style={styles.statusRow}>
          {busy ? (
            <>
              <StatusDot status="running" />
              <Text style={styles.statusText} numberOfLines={1}>
                {`Running${state.turnsInFlight > 0 && !active ? " · sending" : ""}`}
              </Text>
              <Button label={stopping ? "Stopping…" : "Stop"} tone="danger" disabled={stopping} onPress={stop} style={styles.stop} />
            </>
          ) : (
            <Text style={styles.statusText} numberOfLines={1}>
              {connected ? (state.daemon === "offline" ? "Computer offline" : "Idle") : "Waiting for connection"}
            </Text>
          )}
        </View>

        <View style={styles.composer}>
          <TextInput
            value={draft}
            onChangeText={setDraft}
            placeholder={busy ? "Steer the running turn…" : "Message the team…"}
            placeholderTextColor={colors.faint}
            multiline
            style={styles.input}
            accessibilityLabel="Message"
          />
          <Button
            label={busy ? "Steer" : "Send"}
            tone="primary"
            disabled={!canSend}
            onPress={send}
            style={styles.sendButton}
          />
        </View>
      </View>
    </KeyboardAvoidingView>
  );
}

function Header({
  title,
  onBack,
  dot,
  turn,
}: {
  title: string;
  onBack: () => void;
  dot?: "running" | "pending";
  turn?: string;
}) {
  return (
    <View style={styles.header}>
      <Pressable accessibilityRole="button" accessibilityLabel="Back to threads" onPress={onBack} style={styles.back}>
        <Text style={styles.backText}>Back</Text>
      </Pressable>
      <View style={styles.headerText}>
        <View style={styles.titleRow}>
          {dot ? <StatusDot status={dot} /> : null}
          <Text style={styles.title} numberOfLines={1}>
            {title || "Untitled thread"}
          </Text>
        </View>
        {turn ? <Text style={styles.subtitle}>{turn}</Text> : null}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.bg,
  },
  header: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.md,
    paddingHorizontal: space.lg,
    paddingVertical: space.md,
    borderBottomColor: colors.border,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  back: {
    paddingVertical: space.xs,
    paddingRight: space.sm,
  },
  backText: {
    color: colors.accent,
    fontSize: fontSize.body,
  },
  headerText: {
    flex: 1,
    minWidth: 0,
  },
  titleRow: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.sm,
  },
  title: {
    color: colors.text,
    fontSize: fontSize.md,
    fontWeight: "600",
    flexShrink: 1,
  },
  subtitle: {
    color: colors.faint,
    fontSize: fontSize.sm,
    fontFamily: fonts.mono,
    marginTop: 2,
  },
  list: {
    padding: space.lg,
    gap: space.md,
  },
  empty: {
    color: colors.faint,
    fontSize: fontSize.body,
    textAlign: "center",
    padding: space.xxl,
  },
  userRow: {
    alignItems: "flex-end",
  },
  userBubble: {
    maxWidth: "85%",
    backgroundColor: colors.surface2,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.lg,
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
  },
  userText: {
    color: colors.text,
    fontSize: fontSize.body,
    lineHeight: 19,
  },
  steerRow: {
    flexDirection: "row",
    alignItems: "baseline",
    gap: space.sm,
    borderLeftColor: colors.accentLine,
    borderLeftWidth: 2,
    paddingLeft: space.sm,
  },
  steerLabel: {
    color: colors.accent,
    fontSize: fontSize.micro,
    textTransform: "uppercase",
  },
  steerText: {
    flex: 1,
    color: colors.dim,
    fontSize: fontSize.body,
  },
  agentBlock: {
    gap: space.xs,
  },
  agentName: {
    color: colors.agent,
    fontSize: fontSize.xs,
    fontFamily: fonts.mono,
  },
  mailRow: {
    backgroundColor: colors.agentSoft,
    borderColor: colors.agentLine,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    padding: space.sm,
    gap: 2,
  },
  mailHead: {
    color: colors.agent,
    fontSize: fontSize.sm,
    fontFamily: fonts.mono,
  },
  mailArrow: {
    color: colors.faint,
  },
  mailKind: {
    color: colors.faint,
    fontSize: fontSize.micro,
  },
  mailBody: {
    color: colors.text,
    fontSize: fontSize.body,
    lineHeight: 18,
  },
  footer: {
    borderTopColor: colors.border,
    borderTopWidth: StyleSheet.hairlineWidth,
    paddingHorizontal: space.lg,
    paddingTop: space.sm,
    gap: space.sm,
    backgroundColor: colors.surface1,
  },
  statusRow: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.sm,
    minHeight: 28,
  },
  statusText: {
    flex: 1,
    color: colors.dim,
    fontSize: fontSize.sm,
    fontFamily: fonts.mono,
  },
  stop: {
    minHeight: 28,
    paddingVertical: space.xs,
  },
  composer: {
    flexDirection: "row",
    alignItems: "flex-end",
    gap: space.sm,
  },
  input: {
    flex: 1,
    maxHeight: 140,
    minHeight: 40,
    backgroundColor: colors.bg,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
    color: colors.text,
    fontSize: fontSize.body,
  },
  sendButton: {
    minHeight: 40,
  },
});
