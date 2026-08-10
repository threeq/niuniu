import React from 'react';
import { TouchableOpacity, View, Text, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useThemeColors } from '../theme/useTheme';
import { typography, spacing } from '../theme/tokens';

export interface PageHeaderProps {
  title: string;
  subtitle?: string;
  leftIcon?: { name: keyof typeof Ionicons.glyphMap; onPress: () => void; accessibilityLabel: string };
  rightActions?: React.ReactNode;
}

/**
 * 页面头部行容器：左侧可选 icon、中部标题（可带副标题）、右侧 action slot。
 *
 * 5 个 tab 索引页（dashboard / projects / workspaces / repositories / inbox）
 * 共用此组件以保证视觉一致；右侧 actions 由各页自带（如搜索 toggle、create
 * 按钮、Mark all read 等），不强制抹平。
 */
export function PageHeader({ title, subtitle, leftIcon, rightActions }: PageHeaderProps) {
  const colors = useThemeColors();

  return (
    <View style={styles.container}>
      {leftIcon ? (
        <TouchableOpacity
          onPress={leftIcon.onPress}
          hitSlop={12}
          accessibilityRole="button"
          accessibilityLabel={leftIcon.accessibilityLabel}
        >
          <Ionicons name={leftIcon.name} size={24} color={colors.text.primary} />
        </TouchableOpacity>
      ) : null}
      <View style={styles.titleSlot}>
        <Text numberOfLines={1} style={[typography.pageTitle, { color: colors.text.primary }]}>
          {title}
        </Text>
        {subtitle ? (
          <Text
            numberOfLines={1}
            style={[typography.label, { color: colors.text.secondary, marginTop: 2 }]}
          >
            {subtitle}
          </Text>
        ) : null}
      </View>
      {rightActions ? <View style={styles.rightSlot}>{rightActions}</View> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
    gap: spacing.sm,
  },
  titleSlot: {
    flex: 1,
    minWidth: 0,
  },
  rightSlot: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
});
