import { useEffect, useRef } from 'react';
import { StyleSheet, TextInput, View } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';

const LENGTH = 6;
const COMPLETE_DELAY_MS = 200;

type Props = {
  value: string;
  onChange: (next: string) => void;
  onComplete: (code: string) => void;
  hasError?: boolean;
};

/**
 * 6-digit code input. Renders as 6 separate cells; auto-advances focus on
 * digit entry, retreats on backspace, and accepts a pasted 6-digit string
 * into the first cell (which has maxLength=6 so iOS/Android can deliver
 * the full SMS-OTP autofill into one TextInput).
 *
 * Fires `onComplete` 200ms after `value` reaches LENGTH so a paste-and-tap
 * doesn't double-submit before the parent has settled state.
 */
export function CodeInput({ value, onChange, onComplete, hasError = false }: Props) {
  const colors = useThemeColors();
  const refs = useRef<(TextInput | null)[]>([]);

  // Fire onComplete with debounce when full.
  useEffect(() => {
    if (value.length !== LENGTH) return;
    const t = setTimeout(() => onComplete(value), COMPLETE_DELAY_MS);
    return () => clearTimeout(t);
  }, [value, onComplete]);

  const handleChange = (idx: number, text: string) => {
    // Strip non-digits.
    const digits = text.replace(/\D/g, '');
    if (digits.length === 0 && value.length > idx) {
      // Backspace: drop char at idx and refocus prev.
      const next = value.slice(0, idx) + value.slice(idx + 1);
      onChange(next);
      if (idx > 0) refs.current[idx - 1]?.focus();
      return;
    }
    if (digits.length > 1) {
      // Paste: distribute across remaining cells.
      const merged = (value.slice(0, idx) + digits).slice(0, LENGTH);
      onChange(merged);
      const focusIdx = Math.min(merged.length, LENGTH - 1);
      refs.current[focusIdx]?.focus();
      return;
    }
    // Single digit insert.
    const next = (value.slice(0, idx) + digits + value.slice(idx + 1)).slice(0, LENGTH);
    onChange(next);
    if (idx < LENGTH - 1) refs.current[idx + 1]?.focus();
  };

  const cells = Array.from({ length: LENGTH });
  return (
    <View style={styles.row}>
      {cells.map((_, i) => (
        <TextInput
          key={i}
          ref={(r) => {
            refs.current[i] = r;
          }}
          testID={`code-cell-${i}`}
          style={[
            styles.cell,
            {
              borderColor: hasError ? colors.status.error : colors.border.default,
              color: colors.text.primary,
              backgroundColor: colors.bg.surface,
            },
          ]}
          value={value[i] ?? ''}
          onChangeText={(t) => handleChange(i, t)}
          keyboardType="number-pad"
          maxLength={i === 0 ? LENGTH : 1} // first cell accepts paste of full code
          textContentType="oneTimeCode"
          autoComplete="sms-otp"
          autoFocus={i === 0}
          selectTextOnFocus
        />
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  row: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    gap: spacing.sm,
  },
  cell: {
    flex: 1,
    height: 56,
    borderRadius: radius.sm,
    borderWidth: 1,
    textAlign: 'center',
    ...typography.bodyMedium,
    fontSize: 24,
  },
});
