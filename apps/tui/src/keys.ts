/**
 * Raw terminal input → the keys `reduceKey` speaks.
 *
 * The TUI reads stdin itself rather than through a component tree, so the one
 * thing that has to be right is this translation: legacy control bytes for the
 * common keys, CSI sequences for the arrows, and the kitty keyboard protocol
 * (`ESC[105;5u`) for the ctrl keys that legacy encoding cannot express apart
 * from their plain counterparts — ctrl-i and tab are the same byte otherwise.
 */

export interface TuiKey {
  readonly name?: string;
  readonly text?: string;
}

const CONTROL_KEYS: Readonly<Record<string, string>> = {
  "\u0001": "ctrl-a",
  "\u0003": "ctrl-c",
  "\u0004": "ctrl-d",
  "\u0009": "tab",
  "\u000a": "enter",
  "\u000b": "ctrl-k",
  "\u000d": "enter",
  "\u0010": "ctrl-p",
  "\u0012": "ctrl-r",
  "\u0018": "ctrl-x",
  "\u001b": "escape",
  "\u007f": "backspace",
};

/** CSI letter codes for the arrow keys and friends. */
const CSI_KEYS: Readonly<Record<string, string>> = {
  A: "up",
  B: "down",
  C: "right",
  D: "left",
  H: "home",
  F: "end",
};

/** kitty keyboard protocol codepoints we care about, with the ctrl modifier. */
const KITTY_CTRL: Readonly<Record<number, string>> = {
  105: "ctrl-i",
  97: "ctrl-a",
  99: "ctrl-c",
  114: "ctrl-r",
  112: "ctrl-p",
};

/** CSI `<n>~` keys: PageUp and PageDown, which take over transcript scrolling. */
const TILDE_KEYS: Record<string, string> = {
  "3": "delete",
  "5": "pageup",
  "6": "pagedown",
};

const PASTE_START = "\u001b[200~";
const PASTE_END = "\u001b[201~";

/**
 * Split one stdin chunk into keys. Returns the keys in order, with pasted text
 * as a single key carrying its body and the paste markers dropped.
 */
export function parseKeys(chunk: string): ReadonlyArray<TuiKey> {
  const keys: TuiKey[] = [];
  let index = 0;
  while (index < chunk.length) {
    const rest = chunk.slice(index);

    const paste = rest.startsWith(PASTE_START) ? rest.indexOf(PASTE_END) : -1;
    if (paste >= 0) {
      const body = rest.slice(PASTE_START.length, paste);
      if (body.length > 0) keys.push({ text: body.replace(/\r\n?/gu, "\n") });
      index += paste + PASTE_END.length;
      continue;
    }

    const kitty = /^\u001b\[(\d+)(?:;(\d+))?u/u.exec(rest);
    if (kitty !== null) {
      const codepoint = Number(kitty[1]);
      // Kitty reports `modifier + 1`, where the modifier is a bitmask: 4 is ctrl.
      const modifiers = kitty[2] === undefined ? 1 : Number(kitty[2]);
      const withCtrl = ((modifiers - 1) & 4) !== 0;
      const named = withCtrl ? KITTY_CTRL[codepoint] : undefined;
      if (named !== undefined) keys.push({ name: named });
      else if (codepoint >= 32) keys.push({ text: String.fromCodePoint(codepoint) });
      index += kitty[0].length;
      continue;
    }

    const csi = /^\u001b\[([0-9;]*)([A-Za-z~])/u.exec(rest);
    if (csi !== null) {
      const named = CSI_KEYS[csi[2] ?? ""];
      if (named !== undefined) keys.push({ name: named });
      else if (csi[2] === "~") {
        const tilde = TILDE_KEYS[csi[1] ?? ""];
        if (tilde !== undefined) keys.push({ name: tilde });
      }
      index += csi[0].length;
      continue;
    }

    const character = rest[0] ?? "";
    const named = CONTROL_KEYS[character];
    if (named !== undefined) keys.push({ name: named });
    else if (character >= " ") keys.push({ text: character });
    index += character.length;
  }
  return keys;
}
