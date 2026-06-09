import React, { useEffect, useState } from 'react';
import { Box, Text, useInput, type Key } from 'ink';
import { colors } from './theme.js';

export interface TextInputBoxProps {
  value: string;
  onChange: (next: string) => void;
  onSubmit: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
}

/**
 * Pure function used by TextInputBox — exposed for unit testing.
 * Returns the next state given an input event, or null if the event
 * should be passed through to the parent.
 */
export function applyKey(prev: string, input: string, key: Key): { next: string; submit?: true } | null {
  if (key.return) {
    return { next: prev, submit: true };
  }
  if (key.backspace || key.delete) {
    if (prev.length === 0) return { next: prev };
    return { next: prev.slice(0, -1) };
  }
  if (
    key.upArrow ||
    key.downArrow ||
    key.leftArrow ||
    key.rightArrow ||
    key.tab ||
    key.escape ||
    key.ctrl ||
    key.meta ||
    key.shift
  ) {
    return null;
  }
  if (input && input.length > 0) {
    return { next: prev + input };
  }
  return null;
}

/**
 * A minimal, line-oriented text input.
 *
 * Unlike `ink-text-input`, this component:
 *   - Does NOT consume ↑/↓ keys, so the parent component can use them
 *     for history navigation.
 *   - Does NOT support in-line cursor movement (left/right arrows are
 *     also passed through to the parent).
 *   - Treats the value as a single line — Enter submits, Backspace
 *     deletes the last character.
 *
 * The "cursor" is rendered as a small inverse block at the end of the
 * current value (or, if the value is empty, at the start of the
 * placeholder).
 */
export function TextInputBox({
  value,
  onChange,
  onSubmit,
  placeholder = '',
  disabled = false,
}: TextInputBoxProps): React.ReactElement {
  // Local state so the component can append characters immediately,
  // even though `value` is the source of truth from the parent.
  const [localValue, setLocalValue] = useState(value);

  useEffect(() => {
    // When the parent changes `value` (e.g. history recall), mirror it.
    setLocalValue(value);
  }, [value]);

  useInput((input, key) => {
    if (disabled) return;
    const result = applyKey(localValue, input, key);
    if (result === null) return; // pass through to parent
    if (result.submit) {
      onSubmit(result.next);
      return;
    }
    if (result.next !== localValue) {
      setLocalValue(result.next);
      onChange(result.next);
    }
  });

  // (No tick state needed: useEffect above mirrors external `value`
  // changes into localValue, which drives the rendered output.)
  const showPlaceholder = localValue.length === 0 && placeholder.length > 0;
  const cursor = localValue.length === 0 ? '' : ' ';

  return (
    <Box>
      {showPlaceholder ? (
        <Text color={colors.dim}>{placeholder}</Text>
      ) : (
        <Text color={colors.white}>{localValue}</Text>
      )}
      <Text backgroundColor={colors.primary} color={colors.onPrimary}>
        {cursor || ' '}
      </Text>
    </Box>
  );
}
