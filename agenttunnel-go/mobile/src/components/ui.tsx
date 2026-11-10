// Shared primitives. Hand-rolled from theme tokens: no UI kit.
import { useState, type ReactNode } from "react";
import { Pressable, StyleSheet, Text, View, type StyleProp, type ViewStyle } from "react-native";

import { colors, dot, dotColor, fonts, fontSize, radius, space, type DotStatus } from "../theme";
import { parseMarkdownLite, type Span } from "../lib/normalize";

export function StatusDot({ status }: { status: DotStatus }) {
  return <View style={[styles.dot, { backgroundColor: dotColor(status) }]} />;
}

export function Banner({ tone, children }: { tone: "warn" | "err" | "info"; children: ReactNode }) {
  const color = tone === "err" ? colors.err : tone === "warn" ? colors.warn : colors.dim;
  const bg = tone === "err" ? colors.errSoft : colors.surface2;
  return (
    <View style={[styles.banner, { backgroundColor: bg, borderColor: colors.border }]} accessibilityRole="alert">
      <Text style={[styles.bannerText, { color }]}>{children}</Text>
    </View>
  );
}

export function Button({
  label,
  onPress,
  disabled,
  tone = "neutral",
  style,
  accessibilityLabel,
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  tone?: "primary" | "neutral" | "danger";
  style?: StyleProp<ViewStyle>;
  accessibilityLabel?: string;
}) {
  const palette =
    tone === "primary"
      ? { bg: colors.accent, fg: colors.accentInk, border: colors.accent }
      : tone === "danger"
        ? { bg: colors.errSoft, fg: colors.err, border: colors.err }
        : { bg: colors.surface2, fg: colors.text, border: colors.border };
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel ?? label}
      accessibilityState={{ disabled: !!disabled }}
      disabled={disabled}
      onPress={onPress}
      style={({ pressed }) => [
        styles.button,
        { backgroundColor: palette.bg, borderColor: palette.border },
        disabled && styles.disabled,
        pressed && !disabled && styles.pressed,
        style,
      ]}
    >
      <Text style={[styles.buttonText, { color: palette.fg }]}>{label}</Text>
    </Pressable>
  );
}

/** Renders the markdown-lite subset: headings, bullets, paragraphs, code. */
export function Markdown({ source, dim }: { source: string; dim?: boolean }) {
  const blocks = parseMarkdownLite(source);
  return (
    <View style={styles.md}>
      {blocks.map((b, i) => {
        if (b.kind === "code") {
          return (
            <View key={i} style={styles.codeBlock}>
              <Text style={styles.codeText} selectable>
                {b.text}
              </Text>
            </View>
          );
        }
        if (b.kind === "heading") {
          return (
            <Text key={i} style={[styles.mdHeading, { fontSize: b.level === 1 ? fontSize.lg : fontSize.md }]}>
              {renderSpans(b.spans, dim)}
            </Text>
          );
        }
        if (b.kind === "bullet") {
          return (
            <View key={i} style={styles.bulletRow}>
              <Text style={[styles.mdText, dim && styles.dimText]}>{"•"}</Text>
              <Text style={[styles.mdText, styles.bulletBody, dim && styles.dimText]}>
                {renderSpans(b.spans, dim)}
              </Text>
            </View>
          );
        }
        return (
          <Text key={i} style={[styles.mdText, dim && styles.dimText]}>
            {renderSpans(b.spans, dim)}
          </Text>
        );
      })}
    </View>
  );
}

function renderSpans(spans: Span[], dim?: boolean): ReactNode {
  return spans.map((s, i) => {
    if (s.kind === "code") {
      return (
        <Text key={i} style={styles.inlineCode}>
          {s.text}
        </Text>
      );
    }
    if (s.kind === "bold") {
      return (
        <Text key={i} style={[styles.bold, dim && styles.dimText]}>
          {s.text}
        </Text>
      );
    }
    return s.text;
  });
}

/**
 * Tool and command output stays collapsed to its first line; tapping expands
 * the full text in monospace. Keeps long tool dumps from swamping the chat.
 */
export function CollapsedOutput({ label, text, tone }: { label: string; text: string; tone: "tool" | "error" }) {
  const [open, setOpen] = useState(false);
  const firstLine = text.split("\n").find((l) => l.trim() !== "") ?? "";
  const hasMore = text.trim().split("\n").length > 1 || firstLine.length > 120;
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`${label} output, ${open ? "expanded" : "collapsed"}`}
      onPress={() => setOpen((v) => !v)}
      style={[styles.toolBox, tone === "error" && { borderColor: colors.err }]}
    >
      <View style={styles.toolHead}>
        <Text style={[styles.toolLabel, tone === "error" && { color: colors.err }]} numberOfLines={1}>
          {label}
        </Text>
        {!open ? (
          <Text style={styles.toolPreview} numberOfLines={1}>
            {firstLine}
          </Text>
        ) : null}
        {hasMore ? <Text style={styles.toolToggle}>{open ? "hide" : "show"}</Text> : null}
      </View>
      {open ? (
        <Text style={styles.toolBody} selectable>
          {text}
        </Text>
      ) : null}
    </Pressable>
  );
}

export const styles = StyleSheet.create({
  dot: {
    width: dot.size,
    height: dot.size,
    borderRadius: dot.size / 2,
  },
  banner: {
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
    borderRadius: radius.md,
    borderWidth: StyleSheet.hairlineWidth,
  },
  bannerText: {
    fontSize: fontSize.sm,
  },
  button: {
    paddingHorizontal: space.lg,
    paddingVertical: space.sm,
    borderRadius: radius.md,
    borderWidth: StyleSheet.hairlineWidth,
    alignItems: "center",
    justifyContent: "center",
    minHeight: 36,
  },
  buttonText: {
    fontSize: fontSize.body,
    fontWeight: "600",
  },
  disabled: {
    opacity: 0.4,
  },
  pressed: {
    opacity: 0.75,
  },
  md: {
    gap: space.xs,
  },
  mdText: {
    color: colors.text,
    fontSize: fontSize.body,
    lineHeight: 19,
  },
  mdHeading: {
    color: colors.text,
    fontWeight: "600",
    marginTop: space.xs,
  },
  dimText: {
    color: colors.dim,
  },
  bold: {
    fontWeight: "700",
  },
  bulletRow: {
    flexDirection: "row",
    gap: space.sm,
  },
  bulletBody: {
    flex: 1,
  },
  inlineCode: {
    fontFamily: fonts.mono,
    fontSize: fontSize.sm,
    color: colors.accent2,
    backgroundColor: colors.surface2,
  },
  codeBlock: {
    backgroundColor: colors.surface1,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    padding: space.sm,
  },
  codeText: {
    fontFamily: fonts.mono,
    fontSize: fontSize.sm,
    color: colors.text,
    lineHeight: 16,
  },
  toolBox: {
    backgroundColor: colors.surface1,
    borderColor: colors.border,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingHorizontal: space.sm,
    paddingVertical: space.xs + 2,
  },
  toolHead: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.sm,
  },
  toolLabel: {
    fontFamily: fonts.mono,
    fontSize: fontSize.xs,
    color: colors.agent,
    flexShrink: 0,
  },
  toolPreview: {
    flex: 1,
    fontFamily: fonts.mono,
    fontSize: fontSize.xs,
    color: colors.faint,
  },
  toolToggle: {
    fontSize: fontSize.micro,
    color: colors.faint,
    textTransform: "uppercase",
  },
  toolBody: {
    marginTop: space.xs,
    fontFamily: fonts.mono,
    fontSize: fontSize.sm,
    color: colors.dim,
    lineHeight: 16,
  },
});
