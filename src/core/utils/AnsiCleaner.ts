const CSI_PATTERN = /\x1b\[[0-9;?]*[ -/]*[@-~]/g;
const OSC_PATTERN = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g;
const COMBINED = new RegExp(`${CSI_PATTERN.source}|${OSC_PATTERN.source}`, 'g');

export function stripAnsi(input: string): string {
  return input.replace(COMBINED, '');
}

export function hasAnsi(input: string): boolean {
  COMBINED.lastIndex = 0;
  return COMBINED.test(input);
}

export function splitAnsi(input: string): string[] {
  return input.split(COMBINED).filter((s) => s.length > 0);
}
