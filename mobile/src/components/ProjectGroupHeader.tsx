import React from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';
import type { ProjectSectionStats } from '../utils/groupWorkspaces';

interface ProjectGroupHeaderProps {
  sectionKey: string;
  title: string;
  count: number;
  stats: ProjectSectionStats;
  isExpanded: boolean;
  onToggle: (key: string) => void;
}

interface StatBadgeProps {
  testID: string;
  label: string;
  bg: string;
  fg: string;
}

function StatBadge({ testID, label, bg, fg }: StatBadgeProps) {
  return (
    <View
      testID={testID}
      style={[styles.statBadge, { backgroundColor: bg }]}
    >
      <Text style={[styles.statBadgeText, { color: fg }]}>{label}</Text>
    </View>
  );
}

export const ProjectGroupHeader = React.memo(function ProjectGroupHeader({
  sectionKey,
  title,
  count,
  stats,
  isExpanded,
  onToggle,
}: ProjectGroupHeaderProps) {
  const colors = useThemeColors();

  return (
    <Pressable
      testID="project-group-header"
      accessibilityRole="button"
      accessibilityLabel={`${title}, ${count} 个工作空间`} // i18n-todo: replace with i18n key
      accessibilityState={{ expanded: isExpanded }}
      onPress={() => onToggle(sectionKey)}
      style={[styles.row, { backgroundColor: colors.bg.muted }]}
    >
      <Text style={[styles.title, { color: colors.text.primary }]} numberOfLines={1}>
        {title}
      </Text>

      <View style={[styles.countPill, { backgroundColor: colors.border.default }]}>
        <Text style={[styles.countText, { color: colors.text.secondary }]}>
          {count}
        </Text>
      </View>

      <View style={styles.statsRow}>
        {stats.running > 0 && (
          <StatBadge
            testID="group-stat-running"
            label={`${stats.running} 运行`} // i18n-todo: replace with i18n key
            bg={colors.status.successBg}
            fg={colors.status.success}
          />
        )}
        {stats.needs_review > 0 && (
          <StatBadge
            testID="group-stat-needs_review"
            label={`${stats.needs_review} 待审`} // i18n-todo: replace with i18n key
            bg={colors.status.warningBg}
            fg={colors.status.warning}
          />
        )}
        {stats.attention > 0 && (
          <StatBadge
            testID="group-stat-attention"
            label={`${stats.attention} 关注`} // i18n-todo: replace with i18n key
            bg={colors.status.errorBg}
            fg={colors.status.error}
          />
        )}
      </View>

      <Ionicons
        name={isExpanded ? 'chevron-down' : 'chevron-forward'}
        size={16}
        color={colors.text.secondary}
        style={styles.chevron}
      />
    </Pressable>
  );
});

const styles = StyleSheet.create({
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    borderRadius: radius.sm,
    marginHorizontal: spacing.lg,
    marginTop: spacing.md,
    marginBottom: spacing.xs,
    gap: spacing.sm,
  },
  title: {
    fontSize: 14,
    fontWeight: '600',
    flexShrink: 1,
  },
  countPill: {
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
    borderRadius: radius.full,
  },
  countText: {
    fontSize: 11,
    fontWeight: '500',
  },
  statsRow: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
    flexWrap: 'wrap',
    gap: spacing.xs,
  },
  statBadge: {
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
    borderRadius: radius.xs,
  },
  statBadgeText: {
    fontSize: 11,
    fontWeight: '500',
  },
  chevron: {
    marginLeft: 'auto',
  },
});
