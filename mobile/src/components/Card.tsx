import React from 'react';
import { View, StyleSheet, ViewProps } from 'react-native';
import { useThemeColors, useIsDark } from '../theme/useTheme';
import { radius, shadows } from '../theme/tokens';

interface CardProps extends ViewProps {
  children: React.ReactNode;
}

export function Card({ children, style, ...props }: CardProps) {
  const colors = useThemeColors();
  const isDark = useIsDark();

  return (
    <View
      style={[
        styles.card,
        {
          backgroundColor: colors.bg.surface,
          borderColor: colors.border.default,
        },
        !isDark && shadows.sm,
        style,
      ]}
      {...props}
    >
      {children}
    </View>
  );
}

const styles = StyleSheet.create({
  card: {
    borderWidth: 1,
    borderRadius: radius.md,
    padding: 14,
  },
});
