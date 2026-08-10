import { useEffect, useRef } from 'react';
import { Animated, StyleSheet, Text } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';

type Props = {
  message: string | null;
  onDismiss: () => void;
  durationMs?: number;
};

/**
 * Top-of-screen error toast. Renders an absolutely positioned, fading
 * banner when `message` is non-null; auto-dismisses after `durationMs`
 * (default 3s) by fading out for 200ms then calling `onDismiss` so the
 * parent can clear `message` back to null.
 *
 * `pointerEvents="none"` keeps the banner from intercepting taps —
 * this is informational only, not an interactive element.
 */
export function InlineToast({ message, onDismiss, durationMs = 3000 }: Props) {
  const colors = useThemeColors();
  const opacity = useRef(new Animated.Value(0)).current;

  useEffect(() => {
    if (!message) return;
    Animated.timing(opacity, {
      toValue: 1,
      duration: 200,
      useNativeDriver: true,
    }).start();
    const t = setTimeout(() => {
      Animated.timing(opacity, {
        toValue: 0,
        duration: 200,
        useNativeDriver: true,
      }).start(() => {
        onDismiss();
      });
    }, durationMs);
    return () => clearTimeout(t);
  }, [message, durationMs, onDismiss, opacity]);

  if (!message) return null;
  return (
    <Animated.View
      pointerEvents="none"
      style={[styles.wrap, { opacity, backgroundColor: colors.status.error }]}
    >
      <Text style={[styles.text, { color: '#fff' }]}>{message}</Text>
    </Animated.View>
  );
}

const styles = StyleSheet.create({
  wrap: {
    position: 'absolute',
    top: 60,
    left: spacing.lg,
    right: spacing.lg,
    padding: spacing.md,
    borderRadius: radius.md,
    zIndex: 999,
  },
  text: {
    ...typography.bodyMedium,
    textAlign: 'center',
  },
});
