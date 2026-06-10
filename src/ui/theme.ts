/**
 * Tron-inspired theme for AgentTunnel.
 *
 * The theme is intentionally restrained: named 16-color values that look
 * correct on both light-on-dark and most default terminal palettes.
 * Components should import from here rather than hardcoding colors so
 * the theme can evolve in one place.
 */

import type { OutputLineType, SessionStatus } from '../types/index.js';

export const colors = {
  /** Primary brand color — Tron neon blue. */
  primary: 'cyan',
  /** Secondary highlight — amber/orange. Used for active/running. */
  accent: 'yellow',
  /** Faint grid / border color for inactive chrome. */
  grid: 'gray',
  /** Even fainter — used for separators, hints, dimmed text. */
  dim: 'blackBright',
  /** Dark surface (panels with backgrounds). */
  surface: 'black',
  success: 'green',
  danger: 'red',
  /** Foreground on cyan/light surfaces. */
  onPrimary: 'black',
  /** Foreground on amber surfaces. */
  onAccent: 'black',
  white: 'white',
  magenta: 'magenta',
} as const;

export const borders = {
  panel: 'round',
  heavy: 'double',
  thin: 'single',
} as const;

export function borderForStatus(status: SessionStatus): string {
  switch (status) {
    case 'running':
      return colors.accent;
    case 'failed':
      return colors.danger;
    case 'completed':
      return colors.success;
    default:
      return colors.primary;
  }
}

export function colorForStatus(status: SessionStatus): string {
  switch (status) {
    case 'running':
      return colors.accent;
    case 'failed':
      return colors.danger;
    case 'completed':
      return colors.success;
    default:
      return colors.grid;
  }
}

export function colorForOutputType(t: OutputLineType): string {
  switch (t) {
    case 'user':
      return colors.success;
    case 'assistant':
      return colors.primary;
    case 'system':
      return colors.grid;
    case 'error':
      return colors.danger;
    case 'tool':
      return colors.magenta;
    case 'tool_output':
      return colors.white;
  }
}

export function prefixForOutputType(t: OutputLineType): string {
  switch (t) {
    case 'user':
      return ' ▸ ';
    case 'assistant':
      return ' ◆ ';
    case 'system':
      return ' · ';
    case 'error':
      return ' ▲ ';
    case 'tool':
      return ' ⚙ ';
    case 'tool_output':
      return '   ';
  }
}

/** Decorative block characters used in the header / footer. */
export const glyphs = {
  /** Looks like a chunky light-bar. */
  blockFull: '█',
  blockHalf: '▌',
  /** Tron-style chevron. */
  chevron: '▶',
  arrow: '❯',
  /** Grid separators. */
  hLine: '─',
  vLine: '│',
  /** A "scanline" pattern used in the running indicator. */
  scanStart: '┏',
  scanMid: '━',
  scanEnd: '┛',
} as const;

/**
 * Build the decorative header line. Returns a string already padded so
 * `cols` columns are filled. We use a "logo bar" style with thin grid
 * characters instead of a hard background color so the header reads
 * well on any terminal.
 */
export function renderHeaderLine(cols: number, statusLabel: string, model: string): string {
  const left = `▰▰▰ AGENTTUNNEL ▰▰▰`;
  const right = `${statusLabel} • model=${model || 'default'}`;
  const minPad = 2;
  const totalLen = left.length + right.length + minPad;
  if (cols < totalLen) {
    const maxRight = Math.max(0, cols - left.length - minPad);
    return `${left} ${right.slice(0, maxRight)}`;
  }
  const available = Math.max(0, cols - left.length - right.length - minPad);
  const pad = '─'.repeat(available);
  return `${left} ${pad} ${right}`;
}

/** Bottom status / phase strip. */
export function renderFooterLine(cols: number, status: SessionStatus, phase: string): string {
  const left = `┗${'━'.repeat(Math.max(0, cols - phase.length - 4))}┛`;
  return `${phase.padEnd(cols - left.length + phase.length)} ${left}`.slice(0, cols);
}
