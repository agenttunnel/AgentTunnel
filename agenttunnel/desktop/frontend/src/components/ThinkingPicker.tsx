import { Dropdown, type DropdownOption } from "./Dropdown";

/** Thinking levels each harness takes as its variant. Harnesses not listed
 * ignore the variant, so their rows get no picker. */
const THINKING_LEVELS = new Map<string, string[]>([
  ["codex", ["minimal", "low", "medium", "high", "xhigh"]],
  ["pi", ["off", "minimal", "low", "medium", "high", "xhigh", "max"]],
  // opencode variants depend on the model; these are the common ones.
  ["opencode", ["low", "medium", "high", "max"]],
]);

export function supportsThinking(harness: string): boolean {
  return THINKING_LEVELS.has(harness);
}

/** The harness default plus its levels. A current value outside the list stays selectable. */
function thinkingOptions(harness: string, current: string): DropdownOption[] {
  const levels = THINKING_LEVELS.get(harness) ?? [];
  const merged = current && !levels.includes(current) ? [current, ...levels] : levels;
  return [
    { value: "", label: "default" },
    ...merged.map((l) => ({ value: l, label: l })),
  ];
}

type ThinkingPickerProps = {
  harness: string;
  value: string;
  disabled?: boolean;
  onChange: (value: string) => void;
};

/** The thinking-level picker for one runtime. Harnesses without levels get an
 * empty cell so the row's columns stay aligned. */
export function ThinkingPicker({ harness, value, disabled, onChange }: ThinkingPickerProps) {
  if (!supportsThinking(harness)) return <span aria-hidden />;
  return (
    <Dropdown
      value={value}
      options={thinkingOptions(harness, value)}
      placeholder="thinking"
      disabled={disabled}
      onChange={onChange}
    />
  );
}
