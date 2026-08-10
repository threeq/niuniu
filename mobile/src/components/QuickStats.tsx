import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { spacing } from '../theme/tokens';

interface StatItem {
  value: number;
  label: string;
  color?: string;
}

interface QuickStatsProps {
  items: StatItem[];
}

export function QuickStats({ items }: QuickStatsProps) {
  const colors = useThemeColors();

  return (
    <View style={styles.container}>
      {items.map((item) => (
        <View
          key={item.label}
          style={[styles.cell, { backgroundColor: colors.bg.surface, borderColor: colors.border.default }]}
        >
          <Text style={[styles.value, { color: item.color ?? colors.text.primary }]}>
            {item.value}
          </Text>
          <Text style={[styles.label, { color: colors.text.tertiary }]}>{item.label}</Text>
        </View>
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    gap: spacing.sm,
  },
  cell: {
    flex: 1,
    borderWidth: 1,
    borderRadius: 10,
    paddingVertical: spacing.md,
    paddingHorizontal: 10,
    alignItems: 'center',
  },
  value: {
    fontSize: 24,
    fontWeight: '700',
    lineHeight: 26,
    fontVariant: ['tabular-nums'],
  },
  label: {
    fontSize: 11,
    fontWeight: '400',
    marginTop: 2,
  },
});
