import React from 'react';
import {
  ActivityIndicator,
  Animated,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';
import { formatRelativeTime } from '../utils/formatTime';
import {
  getIdBadgeColors,
  hasAnyBgTask,
  mapWorkspaceStatus,
} from '../utils/workspaceCardHelpers';
import type { BgTaskAggregateDTO, Workspace } from '../api/types';
import { Card } from './Card';

// ─── ID badge ───────────────────────────────────────────────────

interface WorkspaceIdBadgeProps {
  id: string;
  status: string;
}

function WorkspaceIdBadge({ id, status }: WorkspaceIdBadgeProps) {
  const colors = useThemeColors();
  const { bg, text } = getIdBadgeColors(status, colors);
  return (
    <View style={[styles.idBadge, { backgroundColor: bg }]}>
      <Text style={[styles.idBadgeText, { color: text }]}>{id}</Text>
    </View>
  );
}

// ─── Background tasks indicator row ──────────────────────────────

const BG_TASK_ICONS: { key: string; icon: keyof typeof Ionicons.glyphMap }[] = [
  { key: 'agent', icon: 'chatbubble-outline' },
  { key: 'bash', icon: 'terminal-outline' },
  { key: 'wakeup', icon: 'time-outline' },
  { key: 'subagent', icon: 'hardware-chip-outline' },
  { key: 'cron', icon: 'calendar-outline' },
];

// usePulseOpacity loops opacity 1 → 0.4 → 1 every 1200ms while `active`
// is true, otherwise stops at 1. Used for agent_busy "live" indicator.
function usePulseOpacity(active: boolean): Animated.Value {
  const opacity = React.useRef(new Animated.Value(1)).current;
  React.useEffect(() => {
    if (!active) {
      opacity.setValue(1);
      return;
    }
    const loop = Animated.loop(
      Animated.sequence([
        Animated.timing(opacity, { toValue: 0.4, duration: 600, useNativeDriver: true }),
        Animated.timing(opacity, { toValue: 1.0, duration: 600, useNativeDriver: true }),
      ]),
    );
    loop.start();
    return () => loop.stop();
  }, [active, opacity]);
  return opacity;
}

interface WorkspaceBgTasksRowProps {
  data?: BgTaskAggregateDTO;
}

function WorkspaceBgTasksRow({ data }: WorkspaceBgTasksRowProps) {
  const colors = useThemeColors();
  const agentBusy = !!data?.agent_busy;
  const pulseOpacity = usePulseOpacity(agentBusy);

  if (!hasAnyBgTask(data)) return null;
  const d = data!;

  const items = [
    { key: 'agent',    count: d.agent_busy ? 1 : 0, pulse: d.agent_busy },
    { key: 'bash',     count: d.bash_count,         pulse: false },
    { key: 'wakeup',   count: d.wakeup_count,       pulse: false },
    { key: 'subagent', count: d.subagent_count,     pulse: false },
    { key: 'cron',     count: d.cron_count,         pulse: false },
  ];

  return (
    <View style={styles.bgTasksRow}>
      {items.map((item, i) => {
        const active = item.count > 0;
        const iconName = BG_TASK_ICONS[i].icon;
        const showCount = active && item.count > 1;
        const iconColor = active ? colors.text.primary : colors.text.tertiary;
        const iconNode = (
          <Ionicons name={iconName} size={12} color={iconColor} />
        );
        return (
          <View key={item.key} style={styles.bgTaskItem}>
            {item.pulse ? (
              <Animated.View style={{ opacity: pulseOpacity }}>{iconNode}</Animated.View>
            ) : (
              iconNode
            )}
            {showCount && (
              <Text style={[styles.bgTaskCount, { color: colors.text.primary }]}>
                {item.count}
              </Text>
            )}
          </View>
        );
      })}
    </View>
  );
}

// ─── Workspace card ───────────────────────────────────────────────

interface WorkspaceCardProps {
  ws: Workspace;
  onPress: () => void;
}

export function WorkspaceCard({ ws, onPress }: WorkspaceCardProps) {
  const colors = useThemeColors();
  const badgeStatus = mapWorkspaceStatus(ws);
  const taskStats = ws.task_stats;
  const hasTask = !!(taskStats && taskStats.total > 0);
  const currentTask = taskStats?.current_task;

  const ahead = ws.ahead_count ?? 0;
  const changes = ws.changes_count ?? 0;
  const schedules = ws.schedule_count ?? 0;
  const hasGitInfo = ahead > 0 || changes > 0 || schedules > 0;

  const showRow3 = !!ws.project_name || hasTask;

  return (
    <Pressable onPress={onPress}>
      <Card>
        {/* Row 1: ID pill + name + time */}
        <View style={styles.row1}>
          <WorkspaceIdBadge id={ws.id} status={badgeStatus} />
          <Text style={[styles.wsName, { color: colors.text.primary }]} numberOfLines={1}>
            {ws.name}
          </Text>
          <Text style={[styles.timeText, { color: colors.text.tertiary }]}>
            {formatRelativeTime(ws.updated_at)}
          </Text>
        </View>

        {/* Row 2: git status (optional) */}
        {hasGitInfo && (
          <View style={styles.row2}>
            {ahead > 0 && (
              <View style={styles.metaItem}>
                <Ionicons name="git-pull-request-outline" size={12} color={colors.status.info} />
                <Text style={[styles.metaText, { color: colors.status.info }]}>
                  {ahead} ahead
                </Text>
              </View>
            )}
            {changes > 0 && (
              <View style={styles.metaItem}>
                <Ionicons name="document-text-outline" size={12} color={colors.status.warning} />
                <Text style={[styles.metaText, { color: colors.status.warning }]}>
                  {changes} changes
                </Text>
              </View>
            )}
            {schedules > 0 && (
              <View style={styles.metaItem}>
                <Ionicons name="time-outline" size={12} color={colors.status.info} />
                <Text style={[styles.metaText, { color: colors.status.info }]}>
                  {schedules}
                </Text>
              </View>
            )}
          </View>
        )}

        {/* Row 3: project · task progress (optional) */}
        {showRow3 && (
          <View style={styles.row3}>
            {ws.project_name && (
              <>
                <Text style={[styles.projectText, { color: colors.text.tertiary }]} numberOfLines={1}>
                  {ws.project_name}
                </Text>
                {hasTask && (
                  <Text style={[styles.dotSep, { color: colors.text.tertiary }]}>·</Text>
                )}
              </>
            )}
            {hasTask && (
              <>
                {currentTask ? (
                  <ActivityIndicator size="small" color={colors.status.info} style={styles.spinner} />
                ) : null}
                <Text style={[styles.taskCount, { color: currentTask ? colors.status.info : colors.text.tertiary }]}>
                  {taskStats!.completed}/{taskStats!.total}
                </Text>
                {currentTask && (
                  <Text style={[styles.currentTask, { color: colors.status.info }]} numberOfLines={1}>
                    {currentTask}
                  </Text>
                )}
              </>
            )}
          </View>
        )}

        {/* Row 4: bg-tasks icon strip (optional) */}
        <WorkspaceBgTasksRow data={ws.bg_tasks} />
      </Card>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  idBadge: {
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: radius.xs,
    flexShrink: 0,
  },
  idBadgeText: {
    fontSize: 11,
    fontWeight: '500',
    fontVariant: ['tabular-nums'],
  },
  wsName: {
    fontSize: 16,
    fontWeight: '500',
    flex: 1,
  },
  timeText: {
    fontSize: 12,
    flexShrink: 0,
  },
  row1: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    marginBottom: spacing.xs,
  },
  row2: {
    flexDirection: 'row',
    alignItems: 'center',
    flexWrap: 'wrap',
    gap: spacing.sm,
    marginTop: spacing.xs,
  },
  row3: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    marginTop: spacing.xs,
    minWidth: 0,
  },
  metaItem: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 3,
  },
  metaText: {
    fontSize: 13,
    fontWeight: '500',
  },
  projectText: {
    fontSize: 13,
    maxWidth: 100,
  },
  dotSep: {
    fontSize: 13,
  },
  taskCount: {
    fontSize: 13,
    fontWeight: '500',
    fontVariant: ['tabular-nums'],
  },
  currentTask: {
    fontSize: 13,
    flexShrink: 1,
    opacity: 0.7,
  },
  spinner: {
    transform: [{ scale: 0.6 }],
  },
  bgTasksRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    marginTop: spacing.xs,
  },
  bgTaskItem: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 2,
  },
  bgTaskCount: {
    fontSize: 9,
    fontWeight: '500',
    fontVariant: ['tabular-nums'],
  },
});
