import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { typography, spacing, radius } from '../theme/tokens';

interface EmptyStateProps {
  title: string;
  description?: string;
  actionLabel?: string;
  onAction?: () => void;
  /**
   * Optional escape-hatch action rendered as a tertiary text button below
   * the primary one. Used by error states (e.g. workspaces "加载失败")
   * to offer "switch / re-pair desktop" without making the user hunt
   * through settings or deep-link by hand.
   */
  secondaryActionLabel?: string;
  onSecondaryAction?: () => void;
}

export function EmptyState({
  title,
  description,
  actionLabel,
  onAction,
  secondaryActionLabel,
  onSecondaryAction,
}: EmptyStateProps) {
  const colors = useThemeColors();

  return (
    <View style={styles.container}>
      <Text style={[typography.sectionHead, { color: colors.text.primary, textAlign: 'center' }]}>{title}</Text>
      {description && (
        <Text style={[typography.body, { color: colors.text.secondary, textAlign: 'center', marginTop: spacing.sm }]}>
          {description}
        </Text>
      )}
      {actionLabel && onAction && (
        <Pressable
          onPress={onAction}
          style={[styles.button, { backgroundColor: colors.brand.accent }]}
        >
          <Text style={styles.buttonText}>{actionLabel}</Text>
        </Pressable>
      )}
      {secondaryActionLabel && onSecondaryAction && (
        <Pressable onPress={onSecondaryAction} style={styles.secondary}>
          <Text style={[styles.secondaryText, { color: colors.text.secondary }]}>
            {secondaryActionLabel}
          </Text>
        </Pressable>
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    padding: spacing['2xl'],
  },
  button: {
    marginTop: spacing.lg,
    paddingVertical: 10,
    paddingHorizontal: spacing.lg,
    borderRadius: radius.sm,
  },
  buttonText: {
    color: '#FFFFFF',
    fontSize: 13,
    fontWeight: '500',
  },
  secondary: {
    marginTop: spacing.md,
    paddingVertical: 6,
    paddingHorizontal: spacing.md,
  },
  secondaryText: {
    fontSize: 13,
    textDecorationLine: 'underline',
  },
});
