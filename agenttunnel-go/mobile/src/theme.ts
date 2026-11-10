// Theme tokens ported from the desktop's :root block in
// desktop/frontend/public/style.css. Fixed dark theme: there is no light mode
// and no second visual language. Names keep the desktop's meaning; the
// `surface*` names are the desktop's --surface-1..3 (its --panel aliases).
import { Platform } from "react-native";

export const colors = {
  bg: "#0b0d12",
  surface1: "#12151c",
  surface2: "#1a1e27",
  surface3: "#242935",
  border: "rgba(255, 255, 255, 0.08)",
  borderSoft: "rgba(255, 255, 255, 0.05)",
  borderStrong: "rgba(255, 255, 255, 0.16)",

  text: "#e6e9f0",
  dim: "#adb4c4",
  faint: "#8c93a6",

  // One accent for focus and selection. Violet is reserved for agent mode.
  accent: "#19c3c8",
  accent2: "#5fd9dd",
  accentInk: "#04191b",
  accentSoft: "rgba(25, 195, 200, 0.12)",
  accentLine: "rgba(25, 195, 200, 0.45)",
  agent: "#a78bfa",
  agentSoft: "rgba(167, 139, 250, 0.12)",
  agentLine: "rgba(167, 139, 250, 0.45)",

  ok: "#5fd39b",
  warn: "#e5b567",
  err: "#f08585",
  errSoft: "rgba(240, 133, 133, 0.07)",
} as const;

export const radius = {
  sm: 4,
  md: 6,
  lg: 10,
  pill: 999,
} as const;

/** Desktop font sizes, in px. The most common desktop sizes are 11-12.5px. */
export const fontSize = {
  micro: 9.5,
  xs: 10.5,
  sm: 11.5,
  base: 12.5,
  body: 13.5,
  md: 15,
  lg: 17,
  xl: 22,
} as const;

/** Desktop CSS declares no spacing variables; this is a 4px grid. */
export const space = {
  xs: 4,
  sm: 8,
  md: 12,
  lg: 16,
  xl: 20,
  xxl: 24,
} as const;

/**
 * JetBrains Mono is not bundled. Platform monospace stands in for the desktop's
 * --mono stack so code and paths stay monospace on every device.
 */
export const fonts = {
  mono: Platform.select({ ios: "Menlo", android: "monospace", default: "monospace" }) as string,
  sans: undefined as string | undefined,
} as const;

export const dot = {
  size: 7,
} as const;

/** Status dot colors, from the desktop's .dot.<status> rules. */
export type DotStatus =
  | "running"
  | "succeeded"
  | "failed"
  | "pending"
  | "cancelled"
  | "clean"
  | "dirty"
  | "missing";

export function dotColor(status: DotStatus): string {
  switch (status) {
    case "running":
    case "dirty":
      return colors.warn;
    case "succeeded":
    case "clean":
      return colors.ok;
    case "failed":
      return colors.err;
    case "pending":
    case "cancelled":
    case "missing":
      return colors.faint;
  }
}
