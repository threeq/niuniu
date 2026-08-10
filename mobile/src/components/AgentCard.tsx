import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import { useThemeColors } from '../theme/useTheme';
import { typography, spacing } from '../theme/tokens';
import { StatusBadge } from './StatusBadge';
import { Card } from './Card';
import { formatRelativeTime } from '../utils/formatTime';

// Keep mirrored with mobile/src/components/StatusBadge.tsx BadgeStatus.
type AgentStatus =
  | 'running'
  | 'completed'
  | 'failed'
  | 'idle'
  | 'needs_review'
  | 'attention'
  | 'created';

interface AgentCardProps {
  name: string;
  status: AgentStatus;
  summary: string;
  costUsd?: number;
  turns?: number;
  updatedAt: string;
  onPress?: () => void;
  unread?: boolean;
}

export function AgentCard({ name, status, summary, costUsd, turns, updatedAt, onPress, unread }: AgentCardProps) {
  const colors = useThemeColors();

  const statusDotColor = {
    running:      colors.status.success,
    completed:    colors.status.success,
    failed:       colors.status.error,
    idle:         colors.text.tertiary,
    needs_review: colors.status.warning,
    attention:    colors.status.error,
    created:      colors.text.tertiary,
  }[status];

  return (
    <Pressable onPress={onPress}>
      <Card style={!unread && { opacity: 0.6 }}>
        <View style={styles.header}>
          <View style={[styles.dot, { backgroundColor: statusDotColor }]} />
          <Text style={[styles.name, { color: colors.text.primary, fontWeight: unread ? '600' : '500' }]} numberOfLines={1}>
            {name}
          </Text>
          {unread && <View style={[styles.unreadDot, { backgroundColor: colors.brand.accent }]} />}
          <Text style={[styles.time, { color: colors.text.tertiary }]}>{formatRelativeTime(updatedAt)}</Text>
        </View>
        <Text style={[styles.summary, { color: colors.text.secondary }]} numberOfLines={1}>
          {summary}
        </Text>
        <View style={styles.footer}>
          <StatusBadge status={status} />
          {costUsd != null && turns != null && (
            <Text style={[styles.meta, { color: colors.text.tertiary }]}>
              ${costUsd.toFixed(2)} · {turns} turns
            </Text>
          )}
        </View>
      </Card>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    marginBottom: spacing.sm,
  },
  dot: {
    width: 8,
    height: 8,
    borderRadius: 4,
  },
  name: {
    ...typography.bodyMedium,
    flex: 1,
  },
  unreadDot: {
    width: 6,
    height: 6,
    borderRadius: 3,
  },
  time: {
    ...typography.caption,
  },
  summary: {
    ...typography.label,
    marginBottom: spacing.sm,
  },
  footer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
  },
  meta: {
    ...typography.caption,
  },
});
