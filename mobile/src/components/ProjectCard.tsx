import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';
import { Card } from './Card';
import {
  splitStatsByPosition,
  summarizeWsStats,
} from '../utils/projectCardHelpers';
import type { Project } from '../api/types';

interface ProjectCardProps {
  project: Project;
  onPress?: () => void;
}

export function ProjectCard({ project, onPress }: ProjectCardProps) {
  const colors = useThemeColors();
  const { todo, inProgress, done } = splitStatsByPosition(project.issue_stats);
  const total = todo + inProgress + done;
  const progress = total > 0 ? Math.round((done / total) * 100) : 0;
  const ws = summarizeWsStats(project.ws_stats);
  const isEmpty = total === 0;

  return (
    <Pressable onPress={onPress}>
      <Card>
        {/* Row 1: name + percent */}
        <View style={styles.row1}>
          <Text style={[styles.name, { color: colors.text.primary }]} numberOfLines={1}>
            {project.name}
          </Text>
          {!isEmpty && (
            <Text style={[styles.percent, { color: colors.brand.accent }]}>
              {progress}%
            </Text>
          )}
        </View>

        {/* Row 2: segmented progress bar — 3 segments scaled by count.
            Hide when project has no issues (avoid an empty-grey track that
            looks like a loading state). */}
        {!isEmpty && (
          <View style={[styles.progressTrack, { backgroundColor: colors.bg.muted }]}>
            {todo > 0 && (
              <View style={[styles.progressSeg, { flex: todo, backgroundColor: colors.priority.p4 }]} />
            )}
            {inProgress > 0 && (
              <View style={[styles.progressSeg, { flex: inProgress, backgroundColor: colors.status.warning }]} />
            )}
            {done > 0 && (
              <View style={[styles.progressSeg, { flex: done, backgroundColor: colors.status.success }]} />
            )}
          </View>
        )}

        {/* Row 3: counts with status dots — color-coded so the eye can
            scan three numbers at once. Always visible (shows zeros when
            empty so empty projects read as "no issues" not "broken card"). */}
        <View style={styles.statsRow}>
          <View style={styles.statItem}>
            <View style={[styles.dot, { backgroundColor: colors.priority.p4 }]} />
            <Text style={[styles.statCount, { color: colors.text.primary }]}>{todo}</Text>
            <Text style={[styles.statLabel, { color: colors.text.tertiary }]}>待办</Text>
          </View>
          <View style={styles.statItem}>
            <View style={[styles.dot, { backgroundColor: colors.status.warning }]} />
            <Text style={[styles.statCount, { color: colors.text.primary }]}>{inProgress}</Text>
            <Text style={[styles.statLabel, { color: colors.text.tertiary }]}>进行中</Text>
          </View>
          <View style={styles.statItem}>
            <View style={[styles.dot, { backgroundColor: colors.status.success }]} />
            <Text style={[styles.statCount, { color: colors.text.primary }]}>{done}</Text>
            <Text style={[styles.statLabel, { color: colors.text.tertiary }]}>已完成</Text>
          </View>
        </View>

        {/* Row 4: workspace highlights (optional) — only renders when at
            least one workspace under this project is in attention /
            needs_review / running, so quiet projects stay visually quiet. */}
        {ws.hasAny && (
          <View style={[styles.wsRow, { borderTopColor: colors.border.subtle }]}>
            {ws.attention > 0 && (
              <View style={styles.wsItem}>
                <Ionicons name="alert-circle" size={13} color={colors.status.error} />
                <Text style={[styles.wsCount, { color: colors.status.error }]}>{ws.attention}</Text>
                <Text style={[styles.wsLabel, { color: colors.status.error }]}>需关注</Text>
              </View>
            )}
            {ws.needsReview > 0 && (
              <View style={styles.wsItem}>
                <Ionicons name="eye-outline" size={13} color={colors.status.warning} />
                <Text style={[styles.wsCount, { color: colors.status.warning }]}>{ws.needsReview}</Text>
                <Text style={[styles.wsLabel, { color: colors.status.warning }]}>待审核</Text>
              </View>
            )}
            {ws.running > 0 && (
              <View style={styles.wsItem}>
                <Ionicons name="play-circle-outline" size={13} color={colors.status.success} />
                <Text style={[styles.wsCount, { color: colors.status.success }]}>{ws.running}</Text>
                <Text style={[styles.wsLabel, { color: colors.status.success }]}>运行中</Text>
              </View>
            )}
          </View>
        )}
      </Card>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  row1: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: spacing.sm,
    gap: spacing.sm,
  },
  name: {
    flex: 1,
    fontSize: 16,
    fontWeight: '600',
    lineHeight: 21,
  },
  percent: {
    fontSize: 13,
    fontWeight: '700',
    fontVariant: ['tabular-nums'],
  },
  progressTrack: {
    height: 4,
    borderRadius: radius.xs,
    overflow: 'hidden',
    flexDirection: 'row',
    marginBottom: spacing.sm,
  },
  progressSeg: {
    height: '100%',
  },
  statsRow: {
    flexDirection: 'row',
    gap: spacing.lg,
    alignItems: 'center',
  },
  statItem: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
  },
  dot: {
    width: 6,
    height: 6,
    borderRadius: 3,
  },
  statCount: {
    fontSize: 13,
    fontWeight: '600',
    fontVariant: ['tabular-nums'],
  },
  statLabel: {
    fontSize: 12,
    fontWeight: '400',
  },
  wsRow: {
    flexDirection: 'row',
    gap: spacing.md,
    alignItems: 'center',
    marginTop: spacing.sm,
    paddingTop: spacing.sm,
    borderTopWidth: 1,
  },
  wsItem: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 3,
  },
  wsCount: {
    fontSize: 12,
    fontWeight: '600',
    fontVariant: ['tabular-nums'],
  },
  wsLabel: {
    fontSize: 11,
    fontWeight: '500',
  },
});
