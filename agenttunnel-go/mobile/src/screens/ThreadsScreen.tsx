// Threads: the instance's threads grouped by project, most recent first.
import { useCallback, useMemo, useState } from "react";
import { Pressable, RefreshControl, SectionList, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { Banner, StatusDot } from "../components/ui";
import { groupThreads, relativeTime, type ThreadRow, type ThreadSection } from "../lib/normalize";
import type { DaemonActions, DaemonState } from "../lib/useDaemon";
import { colors, fonts, fontSize, radius, space } from "../theme";

export function ThreadsScreen({
  title,
  state,
  actions,
  onOpenThread,
  onOpenSettings,
}: {
  title: string;
  state: DaemonState;
  actions: DaemonActions;
  onOpenThread: (threadId: string, projectId?: string) => void;
  onOpenSettings: () => void;
}) {
  const insets = useSafeAreaInsets();
  const [refreshing, setRefreshing] = useState(false);
  const sections = useMemo(() => groupThreads(state.threads, state.projects), [state.threads, state.projects]);

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await actions.refresh();
    } finally {
      setRefreshing(false);
    }
  }, [actions]);

  const now = Date.now();

  return (
    <View style={[styles.root, { paddingTop: insets.top }]}>
      <View style={styles.header}>
        <View style={styles.headerText}>
          <Text style={styles.title} numberOfLines={1}>
            {title}
          </Text>
          <Text style={styles.subtitle} numberOfLines={1}>
            {state.status === "open" ? (state.daemon === "offline" ? "Computer offline" : "Connected") : statusLabel(state.status)}
          </Text>
        </View>
        <Pressable accessibilityRole="button" accessibilityLabel="Settings" onPress={onOpenSettings} style={styles.headerButton}>
          <Text style={styles.headerButtonText}>Settings</Text>
        </Pressable>
      </View>

      {state.status !== "open" && state.status !== "idle" ? (
        <Banner tone={state.status === "closed" ? "err" : "warn"}>
          {state.statusError?.message ?? statusLabel(state.status)}
        </Banner>
      ) : null}
      {state.status === "open" && state.daemon === "offline" ? (
        <Banner tone="warn">Your computer is offline. Sends fail until it reconnects.</Banner>
      ) : null}
      {state.listError ? <Banner tone="err">{state.listError}</Banner> : null}

      <SectionList<ThreadRow, ThreadSection>
        sections={sections}
        keyExtractor={(t) => t.id}
        renderSectionHeader={({ section }) => (
          <Text style={styles.sectionHeader} numberOfLines={1}>
            {section.name}
          </Text>
        )}
        renderItem={({ item }) => (
          <ThreadRowView
            thread={item}
            now={now}
            onPress={() => onOpenThread(item.id, item.projectId)}
          />
        )}
        contentContainerStyle={[styles.list, { paddingBottom: insets.bottom + space.xl }]}
        stickySectionHeadersEnabled={false}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.dim} />}
        ListEmptyComponent={
          state.threads.length === 0 && state.status === "open" ? (
            <Text style={styles.empty}>
              No threads yet. Start one on your desktop, then pull down to refresh.
            </Text>
          ) : undefined
        }
      />
    </View>
  );
}

function ThreadRowView({ thread, now, onPress }: { thread: ThreadRow; now: number; onPress: () => void }) {
  const running = thread.runningAgents > 0;
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`${thread.title || "Untitled thread"}${running ? ", running" : ""}`}
      onPress={onPress}
      style={({ pressed }) => [styles.row, { paddingLeft: space.lg + thread.depth * space.xl }, pressed && styles.rowPressed]}
    >
      <StatusDot status={running ? "running" : "pending"} />
      <View style={styles.rowText}>
        <Text style={styles.rowTitle} numberOfLines={1}>
          {thread.title || "Untitled thread"}
        </Text>
        <Text style={styles.rowMeta} numberOfLines={1}>
          {running ? `${thread.runningAgents} running` : `${thread.messages} messages`}
        </Text>
      </View>
      <Text style={styles.rowTime}>{relativeTime(thread.updatedAt, now)}</Text>
    </Pressable>
  );
}

function statusLabel(status: DaemonState["status"]): string {
  switch (status) {
    case "connecting":
      return "Connecting…";
    case "reconnecting":
      return "Reconnecting…";
    case "closed":
      return "Disconnected";
    case "open":
      return "Connected";
    default:
      return "Not connected";
  }
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.bg,
  },
  header: {
    flexDirection: "row",
    alignItems: "center",
    paddingHorizontal: space.lg,
    paddingVertical: space.md,
    borderBottomColor: colors.border,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  headerText: {
    flex: 1,
  },
  title: {
    color: colors.text,
    fontSize: fontSize.md,
    fontWeight: "600",
  },
  subtitle: {
    color: colors.faint,
    fontSize: fontSize.sm,
    marginTop: 2,
  },
  headerButton: {
    paddingHorizontal: space.sm,
    paddingVertical: space.xs,
    borderRadius: radius.md,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
  },
  headerButtonText: {
    color: colors.dim,
    fontSize: fontSize.sm,
  },
  list: {
    paddingTop: space.sm,
  },
  sectionHeader: {
    color: colors.faint,
    fontSize: fontSize.xs,
    textTransform: "uppercase",
    letterSpacing: 0.6,
    fontFamily: fonts.mono,
    paddingHorizontal: space.lg,
    paddingTop: space.lg,
    paddingBottom: space.xs,
  },
  row: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.md,
    paddingRight: space.lg,
    paddingVertical: space.md,
    borderBottomColor: colors.borderSoft,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  rowPressed: {
    backgroundColor: colors.surface2,
  },
  rowText: {
    flex: 1,
    minWidth: 0,
  },
  rowTitle: {
    color: colors.text,
    fontSize: fontSize.body,
  },
  rowMeta: {
    color: colors.faint,
    fontSize: fontSize.sm,
    marginTop: 2,
  },
  rowTime: {
    color: colors.faint,
    fontSize: fontSize.sm,
    fontFamily: fonts.mono,
  },
  empty: {
    color: colors.faint,
    fontSize: fontSize.body,
    textAlign: "center",
    padding: space.xxl,
    lineHeight: 19,
  },
});
