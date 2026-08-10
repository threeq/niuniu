import React from 'react';
import { View, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';

interface ProgressBarProps {
  progress: number; // 0-100
}

export function ProgressBar({ progress }: ProgressBarProps) {
  const colors = useThemeColors();
  const clampedProgress = Math.min(100, Math.max(0, progress));

  return (
    <View style={[styles.track, { backgroundColor: colors.bg.muted }]}>
      <View style={[styles.fill, { backgroundColor: colors.brand.accent, width: `${clampedProgress}%` }]} />
    </View>
  );
}

const styles = StyleSheet.create({
  track: {
    height: 2,
    borderRadius: 1,
    overflow: 'hidden',
  },
  fill: {
    height: '100%',
    borderRadius: 1,
  },
});
