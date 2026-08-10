import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';

// Union of server WorkspaceStatus + agent-process fallbacks (failed / idle).
// Keep mirrored with mobile/src/api/types.ts WorkspaceStatus and the web's
// zh-CN labels in server/web/src/i18n/locales/zh-CN/workspaces.json status.*.
type BadgeStatus =
  | 'running'
  | 'completed'
  | 'failed'
  | 'idle'
  | 'needs_review'
  | 'attention'
  | 'created';

interface StatusBadgeProps {
  status: BadgeStatus;
  label?: string;
}

export function StatusBadge({ status, label }: StatusBadgeProps) {
  const colors = useThemeColors();

  const colorMap = {
    running:      { text: colors.status.success, bg: colors.status.successBg, defaultLabel: '运行中' },
    completed:    { text: colors.status.success, bg: colors.status.successBg, defaultLabel: '已完成' },
    failed:       { text: colors.status.error,   bg: colors.status.errorBg,   defaultLabel: '失败' },
    idle:         { text: colors.priority.p4,    bg: colors.priority.p4Bg,    defaultLabel: '空闲' },
    needs_review: { text: colors.status.warning, bg: colors.status.warningBg, defaultLabel: '待审核' },
    attention:    { text: colors.status.error,   bg: colors.status.errorBg,   defaultLabel: '需关注' },
    created:      { text: colors.priority.p4,    bg: colors.priority.p4Bg,    defaultLabel: '已创建' },
  } as const;

  const { text: textColor, bg: bgColor, defaultLabel } = colorMap[status];

  return (
    <View style={[styles.badge, { backgroundColor: bgColor }]}>
      <Text style={[styles.text, { color: textColor }]}>{label ?? defaultLabel}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  badge: {
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
    borderRadius: radius.xs,
  },
  text: {
    fontSize: 11,
    fontWeight: '500',
  },
});
