import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useThemeColors } from '../theme/useTheme';
import { typography, spacing, radius } from '../theme/tokens';
import { Badge } from './Badge';
import type { Workspace } from '../api/types';

interface IssueCardProps {
  title: string;
  issueKey: string;
  priority?: number;
  hasAgent?: boolean;
  workspace?: Workspace;
  onPress?: () => void;
  // Tapping the inline workspace badge fires this with the workspace id.
  // Optional — when omitted the badge is rendered but not pressable.
  onWorkspacePress?: (workspaceId: string) => void;
}

// Tinted card style per backend WorkspaceStatus, mirroring web SPA's
// kanban/IssueCard.tsx wsCardStyles. Falls back to "created" for unknown.
type WorkspaceStatusKey =
  | 'created' | 'running' | 'needs_review' | 'attention' | 'completed';

export function IssueCard({
  title,
  issueKey,
  priority,
  hasAgent,
  workspace,
  onPress,
  onWorkspacePress,
}: IssueCardProps) {
  const colors = useThemeColors();

  // Workspace-status palette is intentionally hardcoded (not via theme tokens)
  // so it stays in lockstep with the web SPA's wsCardStyles in
  // server/web/src/components/shared/kanban/IssueCard.tsx — semantic colors
  // for workflow state shouldn't drift with brand theme changes.
  // Pattern: bg = color-50 (very light tint), text = color-700, border = color-200.
  const wsStatusStyle: Record<WorkspaceStatusKey, { text: string; bg: string; border: string; label: string }> = {
    created:      { text: colors.text.secondary, bg: colors.bg.muted, border: colors.border.default, label: '已创建' },
    running:      { text: '#15803D',             bg: '#F0FDF4',       border: '#BBF7D0',             label: '运行中' },
    needs_review: { text: '#C2410C',             bg: '#FFF7ED',       border: '#FED7AA',             label: '待审核' },
    attention:    { text: '#B91C1C',             bg: '#FEF2F2',       border: '#FECACA',             label: '需关注' },
    completed:    { text: '#1D4ED8',             bg: '#EFF6FF',       border: '#BFDBFE',             label: '已完成' },
  };

  // Backend semantic: 0=low, 1=medium, 2=high, 3=critical (matches web SPA).
  // Visual P1..P4 inverts: highest urgency (3) shows as P1.
  const priorityMap: Record<number, { label: string; color: string; bg: string }> = {
    3: { label: 'P1', color: colors.priority.p1, bg: colors.priority.p1Bg },
    2: { label: 'P2', color: colors.priority.p2, bg: colors.priority.p2Bg },
    1: { label: 'P3', color: colors.priority.p3, bg: colors.priority.p3Bg },
    0: { label: 'P4', color: colors.priority.p4, bg: colors.priority.p4Bg },
  };

  const p = priority != null ? priorityMap[priority] : null;

  // Resolve workspace status — fall back to "created" for any unknown
  // value so the badge still renders rather than disappearing silently.
  const wsKey: WorkspaceStatusKey = workspace
    ? ((['created', 'running', 'needs_review', 'attention', 'completed'] as const).includes(
        workspace.status as WorkspaceStatusKey,
      )
        ? (workspace.status as WorkspaceStatusKey)
        : 'created')
    : 'created';
  const wsStyle = workspace ? wsStatusStyle[wsKey] : null;

  return (
    <Pressable onPress={onPress}>
      <View style={[styles.card, { backgroundColor: colors.bg.base, borderColor: colors.border.default }]}>
        <Text style={[styles.title, { color: colors.text.primary }]} numberOfLines={2}>{title}</Text>

        {workspace && wsStyle && (
          <Pressable
            onPress={(e) => {
              // Keep the workspace tap from also opening the issue detail sheet.
              e.stopPropagation();
              onWorkspacePress?.(workspace.id);
            }}
            style={[
              styles.workspaceBar,
              { backgroundColor: wsStyle.bg, borderColor: wsStyle.border },
            ]}
          >
            <Ionicons name="cube-outline" size={12} color={wsStyle.text} />
            <Text
              style={[styles.workspaceName, { color: wsStyle.text }]}
              numberOfLines={1}
            >
              {workspace.name}
            </Text>
            <View style={[styles.workspaceDot, { backgroundColor: wsStyle.text }]} />
            <Text style={[styles.workspaceStatus, { color: wsStyle.text }]}>
              {wsStyle.label}
            </Text>
          </Pressable>
        )}

        <View style={styles.footer}>
          {p && <Badge label={p.label} color={p.color} bgColor={p.bg} />}
          <Text style={[styles.key, { color: colors.text.tertiary }]}>{issueKey}</Text>
          {hasAgent && (
            <View style={styles.agentIndicator}>
              <View style={[styles.agentDot, { backgroundColor: colors.status.success }]} />
              <Text style={[styles.agentLabel, { color: colors.text.secondary }]}>agent</Text>
            </View>
          )}
        </View>
      </View>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  card: {
    borderWidth: 1,
    borderRadius: 10,
    padding: spacing.md,
    marginBottom: spacing.sm,
  },
  title: {
    fontSize: 14,
    fontWeight: '500',
    lineHeight: 18,
    marginBottom: 6,
  },
  workspaceBar: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    paddingHorizontal: spacing.sm,
    paddingVertical: 4,
    borderRadius: radius.sm,
    borderWidth: 1,
    marginBottom: 6,
  },
  workspaceName: {
    fontSize: 11,
    flex: 1,
  },
  workspaceDot: {
    width: 6,
    height: 6,
    borderRadius: 3,
  },
  workspaceStatus: {
    fontSize: 11,
    fontWeight: '500',
  },
  footer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
  },
  key: {
    ...typography.caption,
  },
  agentIndicator: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    marginLeft: 'auto',
  },
  agentDot: {
    width: 6,
    height: 6,
    borderRadius: 3,
  },
  agentLabel: {
    fontSize: 10,
  },
});
