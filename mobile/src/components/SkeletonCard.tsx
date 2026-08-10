import React from 'react';
import { View, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';

export function SkeletonCard() {
  const colors = useThemeColors();

  return (
    <View style={[styles.card, { backgroundColor: colors.bg.muted }]}>
      <View style={[styles.block, { width: '70%', backgroundColor: colors.border.subtle }]} />
      <View style={[styles.block, { width: '50%', backgroundColor: colors.border.subtle }]} />
      <View style={[styles.bar, { backgroundColor: colors.border.subtle }]}>
        <View style={[styles.barFill, { width: '40%', backgroundColor: colors.border.subtle }]} />
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  card: { borderRadius: radius.md, padding: spacing.lg, gap: spacing.sm },
  block: { height: 16, borderRadius: 4 },
  bar: { height: 6, borderRadius: 3, marginTop: 4 },
  barFill: { height: 6, borderRadius: 3 },
});
