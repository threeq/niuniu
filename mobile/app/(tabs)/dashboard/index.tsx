import React, { useCallback, useState } from 'react';
import { View, ScrollView, RefreshControl, StyleSheet, Pressable, Text } from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useRouter } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useThemeColors } from '../../../src/theme/useTheme';
import { spacing } from '../../../src/theme/tokens';
import { useApi } from '../../../src/hooks/useApi';
import { api } from '../../../src/api/client';
import { Project, Workspace } from '../../../src/api/types';
import { splitStatsByPosition } from '../../../src/utils/projectCardHelpers';
import { useNotificationStore } from '../../../src/stores/notificationStore';
import { QuickStats } from '../../../src/components/QuickStats';
import { WorkspaceCard } from '../../../src/components/WorkspaceCard';
import { ProjectCard } from '../../../src/components/ProjectCard';
import { SectionHeader } from '../../../src/components/SectionHeader';
import { ActiveDesktopSwitcher } from '../../../src/components/ActiveDesktopSwitcher';
import { FloatingNotificationButton } from '../../../src/components/FloatingNotificationButton';
import { PageHeader } from '../../../src/components/PageHeader';

export default function DashboardScreen() {
  const colors = useThemeColors();
  const router = useRouter();
  const insets = useSafeAreaInsets();
  const unreadCount = useNotificationStore((s) => s.unreadCount);

  const { data: projects, loading: loadingProjects, refetch: refetchProjects } = useApi<Project[]>(
    () => api.get<Project[]>('/projects'),
    []
  );

  const { data: workspaces, loading: loadingWorkspaces, refetch: refetchWorkspaces } = useApi<Workspace[]>(
    () => api.get<Workspace[]>('/workspaces'),
    []
  );

  const loading = loadingProjects || loadingWorkspaces;

  const onRefresh = useCallback(() => {
    refetchProjects();
    refetchWorkspaces();
  }, [refetchProjects, refetchWorkspaces]);

  const [runningCollapsed, setRunningCollapsed] = useState(true);

  // Surface workspaces that the user can act on: actively running, awaiting
  // review, or needing attention. Mirrors web's project-layout.tsx
  // visibleWsStatuses = ['attention', 'needs_review', 'running']. Skip
  // completed / created — those are not "calls to action".
  // Split into two groups: urgent (attention + needs_review) always-visible;
  // running collapsible (default collapsed) since it's just "in progress",
  // not a call to action.
  const sortByUpdated = (a: Workspace, b: Workspace) =>
    new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
  const urgentWorkspaces = (workspaces ?? [])
    .filter((w) => w.status === 'attention' || w.status === 'needs_review')
    .sort((a, b) => {
      // attention first, then needs_review; tied → updated_at desc.
      if (a.status !== b.status) return a.status === 'attention' ? -1 : 1;
      return sortByUpdated(a, b);
    });
  const runningWorkspaces = (workspaces ?? [])
    .filter((w) => w.status === 'running')
    .sort(sortByUpdated);
  const totalActiveWorkspaces = urgentWorkspaces.length + runningWorkspaces.length;

  const sortedProjects = (projects ?? []).sort(
    (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
  );

  const totalOpenIssues = sortedProjects.reduce((sum, p) => {
    const { todo, inProgress } = splitStatsByPosition(p.issue_stats);
    return sum + todo + inProgress;
  }, 0);

  const runningCount = (workspaces ?? []).filter((w) => w.status === 'running').length;

  return (
    <View style={[styles.screen, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
      <PageHeader
        title="首页"
        leftIcon={{
          name: 'settings-outline',
          onPress: () => router.push('/settings'),
          accessibilityLabel: '设置',
        }}
        rightActions={<ActiveDesktopSwitcher />}
      />
      <ScrollView
        contentContainerStyle={styles.scrollContent}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={onRefresh} />}
      >
        {/* Quick Stats */}
        <View style={styles.section}>
          <QuickStats
            items={[
              { value: totalOpenIssues, label: 'Open' },
              { value: runningCount, label: 'Running', color: colors.status.success },
              { value: unreadCount, label: 'Unread', color: colors.brand.accent },
            ]}
          />
        </View>

        {/* Workspaces section — urgent (attention + needs_review) always
            visible at top; running listed below as a collapsible sub-group
            (default collapsed) since it's "in progress", not a call to
            action. Sort within urgent: attention > needs_review > updated. */}
        {totalActiveWorkspaces > 0 && (
          <View style={styles.section}>
            <SectionHeader
              title="WORKSPACES"
              actionLabel="View all →"
              onAction={() => router.push('/(tabs)/workspaces')}
            />
            <View style={{ gap: spacing.sm }}>
              {urgentWorkspaces.map((w) => (
                <WorkspaceCard
                  key={w.id}
                  ws={w}
                  onPress={() => router.push(`/workspace/${w.id}`)}
                />
              ))}
              {runningWorkspaces.length > 0 && (
                <>
                  <Pressable
                    onPress={() => setRunningCollapsed((v) => !v)}
                    hitSlop={8}
                    accessibilityRole="button"
                    accessibilityLabel={runningCollapsed ? '展开运行中列表' : '折叠运行中列表'}
                    style={styles.subGroupHeader}
                  >
                    <Ionicons
                      name={runningCollapsed ? 'chevron-forward' : 'chevron-down'}
                      size={14}
                      color={colors.text.secondary}
                    />
                    <Text style={[styles.subGroupTitle, { color: colors.text.secondary }]}>
                      运行中 ({runningWorkspaces.length})
                    </Text>
                  </Pressable>
                  {!runningCollapsed && runningWorkspaces.map((w) => (
                    <WorkspaceCard
                      key={w.id}
                      ws={w}
                      onPress={() => router.push(`/workspace/${w.id}`)}
                    />
                  ))}
                </>
              )}
            </View>
          </View>
        )}

        {/* Projects */}
        <View style={styles.section}>
          <SectionHeader title="PROJECTS" actionLabel="View all →" onAction={() => router.push('/(tabs)/projects')} />
          <View style={{ gap: spacing.sm }}>
            {sortedProjects.map((p) => (
              <ProjectCard
                key={p.id}
                project={p}
                onPress={() => router.push(`/project/${p.id}`)}
              />
            ))}
          </View>
        </View>

      </ScrollView>
      <FloatingNotificationButton />
    </View>
  );
}

const styles = StyleSheet.create({
  screen: {
    flex: 1,
  },
  scrollContent: {
    paddingBottom: spacing['2xl'],
  },
  section: {
    paddingHorizontal: spacing.lg + 4,
    marginBottom: spacing.lg,
  },
  subGroupHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.xs,
    paddingVertical: spacing.xs,
    marginTop: spacing.xs,
  },
  subGroupTitle: {
    fontSize: 13,
    fontWeight: '500',
  },
});
