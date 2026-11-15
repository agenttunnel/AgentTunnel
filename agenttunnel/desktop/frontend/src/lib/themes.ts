// Appearance themes. The stylesheet owns the actual tokens; this module names
// them, supplies the swatches the settings sheet shows, and remembers the
// choice. The choice is saved through the prefs store, so the desktop app and
// every web UI of one daemon show the same theme.

import { getPref, setPref } from "./prefs";

export type ThemeId = "warp" | "t3-chat" | "nord" | "rose-pine" | "solarized-light";

export type Theme = {
  id: ThemeId;
  name: string;
  note: string;
  /** The window chrome color (index.html's meta theme-color). */
  chrome: string;
  /** Three colors for the settings swatch, background first. */
  swatches: [string, string, string];
};

export const THEMES: Theme[] = [
  {
    id: "warp",
    name: "Warp",
    note: "Cool near-black with a cyan edge.",
    chrome: "#0b0d12",
    swatches: ["#0b0d12", "#1a1e27", "#19c3c8"],
  },
  {
    id: "t3-chat",
    name: "T3 Chat",
    note: "Deep plum and hot pink.",
    chrome: "#221d27",
    swatches: ["#221d27", "#362d3d", "#db2777"],
  },
  {
    id: "nord",
    name: "Nord",
    note: "Arctic blue-grey and frost.",
    chrome: "#2e3440",
    swatches: ["#2e3440", "#434c5e", "#88c0d0"],
  },
  {
    id: "rose-pine",
    name: "Rosé Pine",
    note: "Muted rose, iris, and gold.",
    chrome: "#191724",
    swatches: ["#191724", "#26233a", "#ebbcba"],
  },
  {
    id: "solarized-light",
    name: "Solarized Light",
    note: "Warm paper with a blue accent.",
    chrome: "#fdf6e3",
    swatches: ["#fdf6e3", "#eee8d5", "#268bd2"],
  },
];

export const DEFAULT_THEME: ThemeId = "warp";

const STORAGE_KEY = "at.theme";

function findTheme(id: string | null | undefined): Theme | undefined {
  return THEMES.find((t) => t.id === id);
}

export function loadTheme(): ThemeId {
  return findTheme(getPref(STORAGE_KEY))?.id ?? DEFAULT_THEME;
}

/** Apply a theme to the document and remember it. Safe to call on every render. */
export function applyTheme(id: ThemeId): void {
  const theme = findTheme(id) ?? findTheme(DEFAULT_THEME)!;
  document.documentElement.dataset.theme = theme.id;
  const meta = document.querySelector('meta[name="theme-color"]');
  meta?.setAttribute("content", theme.chrome);
  setPref(STORAGE_KEY, theme.id);
}
