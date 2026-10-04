/**
 * The AgentTunnel terminal theme: semantic color names, SGR painting, and the
 * only width arithmetic the TUI does. Pure and dependency-free, so the frame
 * renderer can be exercised without a terminal.
 */

/**
 * The foreground roles. `primary` and `accent` are the product's indigo family
 * and carry identity: indigo for the agent and the chrome, violet for the fleet
 * (one stable hue per sub-agent), cyan for links and machine facts. Warm and
 * cool status tones are reserved for state that actually failed or is running.
 */
export type TuiColor =
  | "primary"
  | "accent"
  | "info"
  | "text"
  | "muted"
  | "faint"
  | "line"
  | "ok"
  | "warn"
  | "err";

export const DARK_PALETTE: Readonly<Record<TuiColor, string>> = {
  primary: "#d97757",
  accent: "#c9a2ff",
  info: "#8ad4ff",
  text: "#f2f2f7",
  muted: "#a8a8c0",
  faint: "#74748c",
  line: "#42425c",
  ok: "#7fd88f",
  warn: "#f5a742",
  err: "#ff6b81",
};

/**
 * The accent is the one colour that carries the product's voice: the prompt
 * chevron, the assistant's bullet, the brand mark. It is chosen by name so it
 * can be changed while the client runs, and each name has a light-terminal
 * variant so a swap on paper stays readable.
 */
export const ACCENTS = {
  clay: { dark: "#d97757", light: "#b4532a" },
  indigo: { dark: "#7aa2f7", light: "#3b5bdb" },
  violet: { dark: "#bb9af7", light: "#7048e8" },
  mint: { dark: "#5fe3c0", light: "#0b7285" },
  amber: { dark: "#f5a742", light: "#9a6b16" },
  rose: { dark: "#ff6b81", light: "#c02b45" },
} as const satisfies Readonly<Record<string, { dark: string; light: string }>>;

export type AccentName = keyof typeof ACCENTS;

export const ACCENT_NAMES = Object.keys(ACCENTS) as ReadonlyArray<AccentName>;

/** The accent in force: a built-in name, or a `#rrggbb` the user typed. */
let accent: string = "clay";

export const accentName = (): string => accent;

const resolveAccent = (mode: "dark" | "light"): string => {
  if (accent.startsWith("#")) return accent;
  const entry = ACCENTS[accent as AccentName] ?? ACCENTS.clay;
  return mode === "light" ? entry.light : entry.dark;
};

/** Take a built-in name or any `#rrggbb`; false when the value makes no sense. */
export function applyAccent(value: string): boolean {
  const trimmed = value.trim();
  const named = trimmed.toLowerCase();
  const chosen = named in ACCENTS ? named : trimmed;
  if (!chosen.startsWith("#") && !(chosen in ACCENTS)) return false;
  if (chosen.startsWith("#") && !/^#[0-9a-f]{3}$|^#[0-9a-f]{6}$/iu.test(chosen)) return false;
  accent = chosen;
  // Choosing an accent is an explicit act, so it clears a theme.json override of
  // the same role instead of being silently outranked by it.
  const { primary: _cleared, ...rest } = paletteOverrides;
  paletteOverrides = rest;
  applyTerminalTheme({});
  return true;
}

/** The next accent after this one, so `/theme` cycles without an argument. */
export function nextAccent(current: string = accent): string {
  const index = ACCENT_NAMES.indexOf(current as AccentName);
  return ACCENT_NAMES[(index + 1) % ACCENT_NAMES.length] ?? "clay";
}

/**
 * The same roles on a light terminal. Every reference client decides this by the
 * terminal's own background rather than assuming dark, and so do we: a palette
 * tuned for black is unreadable on paper, and its surfaces invert into smudges.
 */
export const LIGHT_PALETTE: Readonly<Record<TuiColor, string>> = {
  primary: "#b4532a",
  accent: "#7048e8",
  info: "#0b7285",
  text: "#1b1b24",
  muted: "#5c5c6b",
  faint: "#8a8a99",
  line: "#d3d3dd",
  ok: "#2f7d43",
  warn: "#9a6b16",
  err: "#c02b45",
};

/** The active palette. Replaced in place so importers keep their reference. */
export const palette: Record<TuiColor, string> = { ...DARK_PALETTE };

/**
 * Backgrounds, kept apart from the foreground roles. They are *derived* from the
 * terminal's own background rather than hardcoded: a fixed near-black strip is
 * invisible on one terminal and a grey smear on another. Codex blends its user
 * band the same way (12% toward white on dark, 4% toward black on light).
 */
export type TuiSurface = "raised" | "high";

export const surfaces: Record<TuiSurface, string> = {
  raised: "#171725",
  high: "#20202f",
};

/** The assumed background when the terminal will not say (or is pure black). */
const ASSUMED_DARK_BACKGROUND = "#0a0a0c";

const parseHex = (hex: string): readonly [number, number, number] => {
  const digits = hex.startsWith("#") ? hex.slice(1) : hex;
  const wide = digits.length === 3 ? digits.replace(/./gu, (c) => c + c) : digits;
  return [
    Number.parseInt(wide.slice(0, 2), 16),
    Number.parseInt(wide.slice(2, 4), 16),
    Number.parseInt(wide.slice(4, 6), 16),
  ];
};

const toHex = (rgb: readonly [number, number, number]): string =>
  `#${rgb
    .map((channel) =>
      Math.max(0, Math.min(255, Math.round(channel)))
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`;

/** `blend("#ff0000", "#0000ff", 0.5)` is half way from the first to the second. */
export function blend(from: string, toward: string, alpha: number): string {
  const a = parseHex(from);
  const b = parseHex(toward);
  const mix = (index: 0 | 1 | 2): number => a[index] + (b[index] - a[index]) * alpha;
  return toHex([mix(0), mix(1), mix(2)]);
}

/** Relative luminance, the same 0.299/0.587/0.114 test Codex and Gemini use. */
export function isLightBackground(hex: string): boolean {
  const [red, green, blue] = parseHex(hex);
  return 0.299 * red + 0.587 * green + 0.114 * blue > 128;
}

export interface TerminalTheme {
  /** The terminal's own background, when it answered our query. */
  readonly background: string | null;
  readonly mode: "dark" | "light";
  /** False when the environment asks for no colour at all. */
  readonly colors: boolean;
}

const terminal: { background: string | null; mode: "dark" | "light"; colors: boolean } = {
  background: null,
  mode: "dark",
  colors: true,
};

/** What the theme currently believes about the terminal, for tests and callers. */
export const terminalTheme = (): TerminalTheme => ({ ...terminal });

/** Overrides from the user's `theme.json`, re-applied whenever the mode changes. */
let paletteOverrides: Partial<Record<TuiColor, string>> = {};

/**
 * Adopt the terminal's background: pick the palette for its lightness and build
 * the surface ramp by blending toward white (dark) or black (light), so chrome
 * is always visible against whatever the user actually runs.
 */
export function applyTerminalTheme(input: {
  readonly background?: string | null | undefined;
  readonly mode?: "dark" | "light" | "none" | undefined;
}): void {
  if (input.background !== undefined && input.background !== null) {
    terminal.background = input.background;
  }
  const base = input.background ?? terminal.background ?? ASSUMED_DARK_BACKGROUND;
  if (input.mode === "none") {
    terminal.colors = false;
    return;
  }
  terminal.colors = true;
  terminal.mode = input.mode ?? (isLightBackground(base) ? "light" : "dark");
  Object.assign(palette, terminal.mode === "light" ? LIGHT_PALETTE : DARK_PALETTE);
  // The accent outranks the palette's own primary, and survives a mode change.
  palette.primary = resolveAccent(terminal.mode);
  const toward = terminal.mode === "light" ? "#000000" : "#ffffff";
  // Pronounced on purpose: chrome you cannot see is chrome that is not there.
  surfaces.raised = blend(base, toward, terminal.mode === "light" ? 0.07 : 0.1);
  surfaces.high = blend(base, toward, terminal.mode === "light" ? 0.14 : 0.18);
  // A user's own colours outrank anything derived from the terminal.
  Object.assign(palette, paletteOverrides);
  rebuildColors();
}

/** `#rrggbb` overrides for any role, layered on top of the derived palette. */
export function applyPaletteOverrides(overrides: Partial<Record<TuiColor, string>>): void {
  paletteOverrides = { ...paletteOverrides, ...overrides };
  Object.assign(palette, overrides);
  rebuildColors();
}

/**
 * Read the terminal's answer to an OSC 11 query (`rgb:rrrr/gggg/bbbb`, or the
 * 8-bit form). Returns null for anything that is not that reply, so the app can
 * pass the bytes on to the key parser untouched.
 */
export function parseBackgroundReply(chunk: string): string | null {
  // eslint-disable-next-line no-control-regex -- ANSI escapes are control characters by construction.
  const match = /\u001b\]11;rgb:([0-9a-f]{1,4})\/([0-9a-f]{1,4})\/([0-9a-f]{1,4})/iu.exec(chunk);
  if (match === null) return null;
  const channel = (value: string): number => {
    const parsed = Number.parseInt(value, 16);
    return value.length <= 2 ? parsed : Math.round((parsed / 0xffff) * 255);
  };
  return toHex([channel(match[1] ?? "0"), channel(match[2] ?? "0"), channel(match[3] ?? "0")]);
}

/** One stable hue per sub-agent, so an agent keeps its colour everywhere. */
export const FLEET_HUES: ReadonlyArray<TuiColor> = [
  "primary",
  "accent",
  "info",
  "ok",
  "warn",
  "err",
];

/** The hue a fleet member is known by, derived from its id so it never moves. */
export function fleetHue(id: string): TuiColor {
  let hash = 0;
  for (let index = 0; index < id.length; index += 1) {
    hash = (hash * 31 + id.charCodeAt(index)) % 100_003;
  }
  return FLEET_HUES[hash % FLEET_HUES.length] ?? "primary";
}

const ESCAPE = "\u001b";
const RESET = `${ESCAPE}[0m`;

/** CSI, OSC, and the two-character escapes a terminal client can meet. */
// eslint-disable-next-line no-control-regex -- ANSI escapes are control characters by construction.
const ANSI_SEQUENCE = /\u001b\[[0-9;?]*[ -/]*[@-~]|\u001b\][^\u0007\u001b]*(?:\u0007|\u001b\\)/g;

/** `38;2;r;g;b`, the truecolor foreground parameters for a `#rrggbb` value. */
function foregroundParams(hex: string): string {
  const digits = hex.startsWith("#") ? hex.slice(1) : hex;
  const r = Number.parseInt(digits.slice(0, 2), 16);
  const g = Number.parseInt(digits.slice(2, 4), 16);
  const b = Number.parseInt(digits.slice(4, 6), 16);
  return `38;2;${r};${g};${b}`;
}

let FOREGROUND: Record<TuiColor, string> = {} as Record<TuiColor, string>;
let BACKGROUND: Record<TuiSurface, string> = {} as Record<TuiSurface, string>;

/** `48;2;r;g;b`, the truecolor background parameters for a `#rrggbb` value. */
function backgroundParams(hex: string): string {
  return foregroundParams(hex).replace(/^38;/u, "48;");
}

/** Recompute the SGR parameters after the palette or the surfaces change. */
function rebuildColors(): void {
  const roles = Object.keys(palette) as ReadonlyArray<TuiColor>;
  const next: Partial<Record<TuiColor, string>> = {};
  for (const role of roles) next[role] = foregroundParams(palette[role]);
  FOREGROUND = next as Record<TuiColor, string>;
  BACKGROUND = {
    raised: backgroundParams(surfaces.raised),
    high: backgroundParams(surfaces.high),
  };
}

rebuildColors();

/** Wrap text in the color's SGR sequence, always ending with a reset. */
export function paint(
  text: string,
  color: TuiColor,
  options?: { readonly bold?: boolean; readonly bg?: TuiSurface },
): string {
  // NO_COLOR, a dumb terminal, or an explicit no-colour theme: the layout must
  // still hold, so the text is passed through and the width arithmetic ignores it.
  if (!terminal.colors) return text;
  const parameters = [FOREGROUND[color]];
  if (options?.bold === true) parameters.unshift("1");
  if (options?.bg !== undefined) parameters.push(BACKGROUND[options.bg]);
  return `${ESCAPE}[${parameters.join(";")}m${text}${RESET}`;
}

/**
 * A padded pill: a filled surface with one word in it. The footer uses these to
 * say where the work is happening without spending a whole row per fact.
 */
export function chip(text: string, color: TuiColor, bg: TuiSurface = "high"): string {
  return paint(` ${text} `, color, { bg });
}

/** A key binding: the key reads as a key, the verb stays quiet. */
export function keycap(key: string, label: string, options?: { readonly bg?: TuiSurface }): string {
  const keyStyle = options?.bg === undefined ? { bold: true } : { bold: true, bg: options.bg };
  const labelStyle = options?.bg === undefined ? {} : { bg: options.bg };
  return `${paint(key, "text", keyStyle)} ${paint(label, "faint", labelStyle)}`;
}

/**
 * Code points that occupy no column of their own: control characters, combining
 * marks, variation selectors, and the zero-width/bidi formatting characters.
 * Sorted by start so lookups can bisect.
 */
const ZERO_WIDTH_RANGES: ReadonlyArray<readonly [number, number]> = [
  [0x0000, 0x001f], // C0 controls
  [0x007f, 0x009f], // DEL and C1 controls
  [0x0300, 0x036f], // combining diacritical marks
  [0x0483, 0x0489], // Cyrillic combining marks
  [0x0591, 0x05bd], // Hebrew points
  [0x05bf, 0x05c7], // Hebrew points (with the narrow gaps folded in)
  [0x0610, 0x061a], // Arabic marks
  [0x064b, 0x065f], // Arabic vowel signs
  [0x0670, 0x0670],
  [0x06d6, 0x06ed], // Arabic Koranic marks
  [0x0711, 0x0711],
  [0x0730, 0x074a], // Syriac points
  [0x07a6, 0x07b0],
  [0x07eb, 0x07f3],
  [0x0816, 0x082d], // Samaritan marks
  [0x0859, 0x085b],
  [0x08d4, 0x0903], // Arabic extended marks through Devanagari signs
  [0x093a, 0x094f], // Devanagari matras
  [0x0951, 0x0957],
  [0x0962, 0x0963],
  [0x0981, 0x0983], // Bengali signs
  [0x09bc, 0x09cd],
  [0x09d7, 0x09d7],
  [0x09e2, 0x09e3],
  [0x0a01, 0x0a03], // Gurmukhi
  [0x0a3c, 0x0a51],
  [0x0a70, 0x0a71],
  [0x0a75, 0x0a75],
  [0x0a81, 0x0a83], // Gujarati
  [0x0abc, 0x0acd],
  [0x0ae2, 0x0ae3],
  [0x0b01, 0x0b03], // Oriya
  [0x0b3c, 0x0b57],
  [0x0b62, 0x0b63],
  [0x0b82, 0x0b82], // Tamil
  [0x0bbe, 0x0bcd],
  [0x0bd7, 0x0bd7],
  [0x0c00, 0x0c04], // Telugu
  [0x0c3e, 0x0c56],
  [0x0c62, 0x0c63],
  [0x0c81, 0x0c83], // Kannada
  [0x0cbc, 0x0cd6],
  [0x0ce2, 0x0ce3],
  [0x0d01, 0x0d03], // Malayalam
  [0x0d3e, 0x0d4d],
  [0x0d57, 0x0d57],
  [0x0d62, 0x0d63],
  [0x0d82, 0x0d83], // Sinhala
  [0x0dca, 0x0df3],
  [0x0e31, 0x0e3a], // Thai
  [0x0e47, 0x0e4e],
  [0x0eb1, 0x0ebc], // Lao
  [0x0ec8, 0x0ecd],
  [0x0f18, 0x0f19], // Tibetan
  [0x0f35, 0x0f39],
  [0x0f71, 0x0f84],
  [0x0f86, 0x0f87],
  [0x0f8d, 0x0fbc],
  [0x0fc6, 0x0fc6],
  [0x102b, 0x103e], // Myanmar
  [0x1056, 0x1059],
  [0x105e, 0x1060],
  [0x1062, 0x1064],
  [0x1067, 0x106d],
  [0x1071, 0x1074],
  [0x1082, 0x108d],
  [0x108f, 0x108f],
  [0x109a, 0x109d],
  [0x135d, 0x135f], // Ethiopic combining marks
  [0x1712, 0x1714], // Tagalog
  [0x1732, 0x1734], // Hanunoo
  [0x1752, 0x1753], // Buhid
  [0x1772, 0x1773], // Tagbanwa
  [0x17b4, 0x17d3], // Khmer
  [0x17dd, 0x17dd],
  [0x180b, 0x180f], // Mongolian free variation selectors
  [0x1885, 0x1886],
  [0x18a9, 0x18a9],
  [0x1920, 0x193b], // Limbu
  [0x1a17, 0x1a1b], // Buginese
  [0x1a55, 0x1a7f], // Tai Tham
  [0x1ab0, 0x1aff], // combining diacritical marks extended
  [0x1b00, 0x1b04], // Balinese
  [0x1b34, 0x1b44],
  [0x1b6b, 0x1b73],
  [0x1b80, 0x1b82], // Sundanese
  [0x1ba1, 0x1bad],
  [0x1be6, 0x1bf3], // Batak
  [0x1c24, 0x1c37], // Lepcha
  [0x1cd0, 0x1cd2], // Vedic extensions
  [0x1cd4, 0x1ce8],
  [0x1ced, 0x1ced],
  [0x1cf2, 0x1cf4],
  [0x1cf8, 0x1cf9],
  [0x1dc0, 0x1dff], // combining diacritical marks supplement
  [0x200b, 0x200f], // zero-width space through right-to-left mark
  [0x202a, 0x202e], // bidi embedding controls
  [0x2060, 0x2064], // word joiner and invisible operators
  [0x2066, 0x206f],
  [0x20d0, 0x20f0], // combining marks for symbols
  [0x2cef, 0x2cf1], // Coptic combining marks
  [0x2d7f, 0x2d7f],
  [0x2de0, 0x2dff], // Cyrillic extended A
  [0x302a, 0x302f], // CJK tone marks
  [0x3099, 0x309a], // kana voiced sound marks
  [0xa66f, 0xa672], // Cyrillic extended B
  [0xa674, 0xa67d],
  [0xa69e, 0xa69f],
  [0xa6f0, 0xa6f1],
  [0xa802, 0xa806], // Syloti Nagri
  [0xa80b, 0xa82c],
  [0xa880, 0xa881], // Saurashtra
  [0xa8b4, 0xa8c5],
  [0xa8e0, 0xa8f1], // Devanagari extended
  [0xa926, 0xa92d], // Kayah Li
  [0xa947, 0xa953], // Rejang
  [0xa980, 0xa983], // Javanese
  [0xa9b3, 0xa9c0],
  [0xa9e5, 0xa9e5],
  [0xaa29, 0xaa36], // Cham
  [0xaa43, 0xaa43],
  [0xaa4c, 0xaa4d],
  [0xaa7b, 0xaa7d],
  [0xaab0, 0xaab0], // Tai Viet
  [0xaab2, 0xaab4],
  [0xaab7, 0xaab8],
  [0xaabe, 0xaabf],
  [0xaac1, 0xaac1],
  [0xaaeb, 0xaaef],
  [0xaaf5, 0xaaf6],
  [0xabe3, 0xabea], // Meetei Mayek
  [0xabec, 0xabed],
  [0xfb1e, 0xfb1e], // Hebrew presentation forms
  [0xfe00, 0xfe0f], // variation selectors
  [0xfe20, 0xfe2f], // combining half marks
  [0xfeff, 0xfeff], // byte order mark
  [0xfff9, 0xfffb], // interlinear annotation
  [0x101fd, 0x101fd], // Phaistos disc sign combining
  [0x102e0, 0x102e0], // Coptic epact thousands mark
  [0x10376, 0x1037a], // Old Permic
  [0x10a01, 0x10a0f], // Kharoshthi
  [0x10a38, 0x10a3f],
  [0x10ae5, 0x10ae6], // Manichaean
  [0x11000, 0x11002], // Brahmi
  [0x11038, 0x11046],
  [0x1107f, 0x11082],
  [0x110b0, 0x110ba], // Kaithi
  [0x11100, 0x11102], // Chakma
  [0x11127, 0x11134],
  [0x11173, 0x11173], // Mahajani
  [0x11180, 0x11182], // Sharada
  [0x111b3, 0x111c0],
  [0x1122c, 0x11237], // Khojki
  [0x112df, 0x112ea], // Khudawadi
  [0x11300, 0x11303], // Grantha
  [0x1133c, 0x1134d],
  [0x11357, 0x11357],
  [0x11362, 0x11374],
  [0x114b0, 0x114c3], // Tirhuta
  [0x115af, 0x115c0], // Siddham
  [0x116ab, 0x116b7], // Modi
  [0x1171d, 0x1172b], // Ahom
  [0x16af0, 0x16af4], // Bassa Vah
  [0x16b30, 0x16b36], // Pahawh Hmong
  [0x16f51, 0x16f92], // Miao
  [0x1bc9d, 0x1bc9e], // Duployan
  [0x1bca0, 0x1bca3],
  [0x1d165, 0x1d182], // musical symbols
  [0x1d185, 0x1d18b],
  [0x1d1aa, 0x1d1ad],
  [0x1d242, 0x1d244],
  [0x1da00, 0x1da36], // Sutton SignWriting
  [0x1da3b, 0x1da6c],
  [0x1da75, 0x1da75],
  [0x1da84, 0x1da84],
  [0x1da9b, 0x1daa1],
  [0x1e000, 0x1e02a], // Glagolitic supplement
  [0x1e8d0, 0x1e8d6], // Mende Kikakui
  [0x1e944, 0x1e94a], // Adlam
  [0xe0100, 0xe01ef], // variation selectors supplement
];

/**
 * East Asian Wide and Fullwidth ranges. Ambiguous-width characters are
 * deliberately absent: a terminal in a non-CJK locale renders them one column
 * wide, which is what the app's own glyph set relies on.
 */
const WIDE_RANGES: ReadonlyArray<readonly [number, number]> = [
  [0x1100, 0x115f], // Hangul Jamo initial consonants
  [0x2e80, 0x303e], // CJK radicals, Kangxi, CJK symbols and punctuation
  [0x3041, 0x33ff], // kana, Bopomofo, Hangul compatibility, enclosed CJK
  [0x3400, 0x4dbf], // CJK unified ideographs extension A
  [0x4e00, 0x9fff], // CJK unified ideographs
  [0xa000, 0xa4cf], // Yi
  [0xa960, 0xa97f], // Hangul Jamo extended A
  [0xac00, 0xd7a3], // Hangul syllables
  [0xf900, 0xfaff], // CJK compatibility ideographs
  [0xfe10, 0xfe19], // vertical forms
  [0xfe30, 0xfe6b], // CJK compatibility forms
  [0xff00, 0xff60], // fullwidth forms
  [0xffe0, 0xffe6], // fullwidth signs
  [0x1f1e6, 0x1f1ff], // regional indicators
  [0x1f300, 0x1f64f], // pictographs and emoticons
  [0x1f680, 0x1f6ff], // transport and map symbols
  [0x1f900, 0x1f9ff], // supplemental symbols and pictographs
  [0x1fa70, 0x1faff], // symbols and pictographs extended A
  [0x20000, 0x3fffd], // CJK extensions B and beyond
];

/** Whether `codePoint` falls in one of `ranges`, which must be sorted by start. */
function inRanges(codePoint: number, ranges: ReadonlyArray<readonly [number, number]>): boolean {
  let low = 0;
  let high = ranges.length - 1;
  while (low <= high) {
    const middle = (low + high) >> 1;
    const range = ranges[middle];
    if (range === undefined) return false;
    if (codePoint < range[0]) high = middle - 1;
    else if (codePoint > range[1]) low = middle + 1;
    else return true;
  }
  return false;
}

/** Display columns a single code point occupies: 0, 1, or 2. */
function codePointWidth(codePoint: number): number {
  if (codePoint < 0x0300) {
    const control = codePoint < 0x0020 || (codePoint >= 0x007f && codePoint <= 0x009f);
    return control ? 0 : 1;
  }
  if (inRanges(codePoint, ZERO_WIDTH_RANGES)) return 0;
  return inRanges(codePoint, WIDE_RANGES) ? 2 : 1;
}

/** Display columns, ignoring ANSI escapes: 2 for East Asian wide/fullwidth, 0 for combining marks, 1 otherwise. */
export function visibleWidth(text: string): number {
  let width = 0;
  for (const character of text.replace(ANSI_SEQUENCE, "")) {
    width += codePointWidth(character.codePointAt(0) ?? 0);
  }
  return width;
}

/** Cut to `width` display columns, appending `…` when anything was dropped. Must not slice inside an ANSI sequence. */
export function truncate(text: string, width: number): string {
  if (width <= 0) return "";
  if (visibleWidth(text) <= width) return text;

  const budget = width - 1;
  let out = "";
  let used = 0;
  let cut = false;

  const take = (chunk: string): void => {
    for (const character of chunk) {
      if (cut) return;
      const characterWidth = codePointWidth(character.codePointAt(0) ?? 0);
      if (used + characterWidth > budget) {
        cut = true;
        return;
      }
      out += character;
      used += characterWidth;
    }
  };

  let cursor = 0;
  for (const match of text.matchAll(ANSI_SEQUENCE)) {
    const start = match.index;
    take(text.slice(cursor, start));
    if (cut) break;
    out += match[0];
    cursor = start + match[0].length;
  }
  if (!cut) take(text.slice(cursor));

  if (!cut) return out;
  // The ellipsis takes the column the content did not, and any color left open
  // by the cut is closed so the padding that follows stays unpainted.
  return text.includes(ESCAPE) ? `${out}…${RESET}` : `${out}…`;
}

/** Pad with spaces to exactly `width` display columns (never truncates; callers truncate first). */
export function pad(text: string, width: number): string {
  const missing = width - visibleWidth(text);
  return missing > 0 ? `${text}${" ".repeat(missing)}` : text;
}
