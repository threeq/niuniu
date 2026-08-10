import { FlashList } from '@shopify/flash-list';
import { Stack, useLocalSearchParams, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  ActivityIndicator,
  Pressable,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { api } from '../../src/api/client';
import type { Column, ExecutionPlan, Issue, Project, Workspace } from '../../src/api/types';
import { Card, ConfirmDialog, FilterChips, IssueCard, SegmentedControl } from '../../src/components';
import { IssueDetailSheet } from '../../src/components/IssueDetailSheet';
import { CreateIssueSheet } from '../../src/components/CreateIssueSheet';
import { BottomSheetModal } from '@gorhom/bottom-sheet';
import { useSSE } from '../../src/hooks/useSSE';
import { useThemeColors } from '../../src/theme/useTheme';
import { typography, spacing, radius } from '../../src/theme/tokens';

// ─── Constants ────────────────────────────────────────────────────

const VIEW_MODE_OPTIONS = ['看板', '列表', '统计'];

type ViewMode = 'kanban' | 'list' | 'stats';
const VIEW_MODE_VALUES: ViewMode[] = ['kanban', 'list', 'stats'];

const lifecycleLabels: Record<string, string> = {
  created: '待处理',
  spec: '规格定义',
  'spec-review': '规格评审',
  plan: '计划中',
  'plan-review': '计划评审',
  implement: '实现中',
  'implement-review': '代码评审',
  test: '测试中',
  completed: '已完成',
};

// ─── Kanban View ──────────────────────────────────────────────────

function KanbanView({
  columns,
  issuesByColumn,
  workspaceByIssueId,
  onIssuePress,
  onWorkspacePress,
}: {
  columns: Column[];
  issuesByColumn: Map<number, Issue[]>;
  workspaceByIssueId: Record<string, Workspace>;
  onIssuePress?: (issue: Issue) => void;
  onWorkspacePress?: (workspaceId: string) => void;
}) {
  const colors = useThemeColors();
  const [selectedColumnId, setSelectedColumnId] = useState<number | null>(null);

  // Auto-select first column
  useEffect(() => {
    if (columns.length > 0 && selectedColumnId === null) {
      setSelectedColumnId(columns[0].id);
    }
  }, [columns, selectedColumnId]);

  const columnOptions = useMemo(
    () =>
      columns.map((c) => ({
        label: `${c.name} (${issuesByColumn.get(c.id)?.length ?? 0})`,
        value: String(c.id),
      })),
    [columns, issuesByColumn],
  );

  const selectedColumn = columns.find((c) => c.id === selectedColumnId);
  const columnIssues = selectedColumnId
    ? (issuesByColumn.get(selectedColumnId) ?? [])
    : [];

  if (columns.length === 0) {
    return (
      <View style={styles.emptyView}>
        <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>暂无看板列</Text>
      </View>
    );
  }

  return (
    <View style={styles.kanbanContainer}>
      <View style={styles.columnPickerWrap}>
        <FilterChips
          options={columnOptions}
          selected={String(selectedColumnId ?? '')}
          onSelect={(v) => setSelectedColumnId(Number(v))}
        />
      </View>
      {selectedColumn && (
        <FlashList
          data={columnIssues}
          keyExtractor={(item) => String(item.id)}
          renderItem={({ item }) => (
            <IssueCard
              title={item.title}
              issueKey={`#${item.id}`}
              priority={item.priority}
              workspace={workspaceByIssueId[String(item.id)]}
              onPress={() => onIssuePress?.(item)}
              onWorkspacePress={onWorkspacePress}
            />
          )}
          contentContainerStyle={styles.kanbanListContent}
          showsVerticalScrollIndicator={false}
          ListEmptyComponent={
            <View style={styles.columnEmpty}>
              <Text style={[styles.columnEmptyText, { color: colors.text.tertiary }]}>
                「{selectedColumn.name}」暂无 Issue
              </Text>
            </View>
          }
        />
      )}
    </View>
  );
}

// ─── List View ────────────────────────────────────────────────────

function ListView({
  columns,
  issuesByColumn,
  workspaceByIssueId,
  onIssuePress,
  onWorkspacePress,
}: {
  columns: Column[];
  issuesByColumn: Map<number, Issue[]>;
  workspaceByIssueId: Record<string, Workspace>;
  onIssuePress?: (issue: Issue) => void;
  onWorkspacePress?: (workspaceId: string) => void;
}) {
  const colors = useThemeColors();
  const [collapsedColumns, setCollapsedColumns] = useState<Set<number>>(new Set());

  // Collapse "completed" columns by default
  useEffect(() => {
    const completedCols = columns
      .filter((c) => c.lifecycle_mapping === 'done')
      .map((c) => c.id);
    if (completedCols.length > 0) {
      setCollapsedColumns(new Set(completedCols));
    }
  }, [columns]);

  const toggleCollapse = useCallback((columnId: number) => {
    setCollapsedColumns((prev) => {
      const next = new Set(prev);
      if (next.has(columnId)) {
        next.delete(columnId);
      } else {
        next.add(columnId);
      }
      return next;
    });
  }, []);

  // Build flat list with section headers
  const listData = useMemo(() => {
    const items: Array<{ type: 'header' | 'issue'; column: Column; issue?: Issue }> = [];
    for (const col of columns) {
      items.push({ type: 'header', column: col });
      const issues = issuesByColumn.get(col.id) ?? [];
      if (!collapsedColumns.has(col.id)) {
        for (const issue of issues) {
          items.push({ type: 'issue', column: col, issue });
        }
      }
    }
    return items;
  }, [columns, issuesByColumn, collapsedColumns]);

  return (
    <FlashList
      data={listData}
      keyExtractor={(item, index) =>
        item.type === 'header'
          ? `h-${item.column.id}`
          : `i-${item.issue!.id}`
      }
      renderItem={({ item }) => {
        if (item.type === 'header') {
          const count = issuesByColumn.get(item.column.id)?.length ?? 0;
          const isCollapsed = collapsedColumns.has(item.column.id);
          return (
            <Pressable
              style={[styles.sectionHeader, { backgroundColor: colors.bg.base }]}
              onPress={() => toggleCollapse(item.column.id)}
            >
              <View style={styles.sectionHeaderLeft}>
                <View style={[styles.statusSquare, { backgroundColor: colors.brand.accent }]} />
                <Text style={[styles.sectionHeaderText, { color: colors.text.primary }]}>
                  {item.column.name}
                </Text>
              </View>
              <View style={styles.sectionHeaderRight}>
                <Text style={[styles.sectionHeaderCount, { color: colors.text.tertiary }]}>{count}</Text>
                <Ionicons
                  name={isCollapsed ? 'chevron-forward' : 'chevron-down'}
                  size={16}
                  color={colors.text.tertiary}
                />
              </View>
            </Pressable>
          );
        }
        return (
          <IssueCard
            title={item.issue!.title}
            issueKey={`#${item.issue!.id}`}
            priority={item.issue!.priority}
            workspace={workspaceByIssueId[String(item.issue!.id)]}
            onPress={() => onIssuePress?.(item.issue!)}
            onWorkspacePress={onWorkspacePress}
          />
        );
      }}
      contentContainerStyle={styles.listViewContent}
      showsVerticalScrollIndicator={false}
    />
  );
}

// ─── Stats View ───────────────────────────────────────────────────

function StatsView({
  issues,
  columns,
  executionPlans,
}: {
  issues: Issue[];
  columns: Column[];
  executionPlans: ExecutionPlan[];
}) {
  const colors = useThemeColors();

  // Backend semantic: 0=low, 1=medium, 2=high, 3=critical (matches web SPA).
  // Visual P1..P4 inverts: highest urgency (3) shows as P1.
  const priorityColors: Record<number, string> = {
    3: colors.priority.p1,
    2: colors.priority.p2,
    1: colors.priority.p3,
    0: colors.priority.p4,
  };

  // Priority distribution — keyed by backend value 0..3
  const priorityDist = useMemo(() => {
    const dist: Record<number, number> = { 0: 0, 1: 0, 2: 0, 3: 0 };
    for (const issue of issues) {
      const p = issue.priority >= 0 && issue.priority <= 3 ? issue.priority : 0;
      dist[p]++;
    }
    return dist;
  }, [issues]);

  // Lifecycle distribution
  const lifecycleDist = useMemo(() => {
    const dist: Record<string, number> = {};
    for (const issue of issues) {
      const label = lifecycleLabels[issue.lifecycle_status] ?? issue.lifecycle_status;
      dist[label] = (dist[label] ?? 0) + 1;
    }
    return dist;
  }, [issues]);

  return (
    <ScrollView
      contentContainerStyle={styles.statsContent}
      showsVerticalScrollIndicator={false}
    >
      {/* Priority distribution — visual P1..P4 maps to backend value 3..0 */}
      <Text style={[styles.statsSectionTitle, { color: colors.text.primary }]}>优先级分布</Text>
      <View style={styles.statsGrid}>
        {([['P1', 3], ['P2', 2], ['P3', 1], ['P4', 0]] as const).map(([label, value]) => (
          <Card key={label} style={[styles.statCard, { borderWidth: 1, borderColor: colors.border.default }]}>
            <View style={[styles.statDot, { backgroundColor: priorityColors[value] }]} />
            <Text style={[styles.statLabel, { color: colors.text.secondary }]}>{label}</Text>
            <Text style={[styles.statValue, { color: colors.text.primary }]}>{priorityDist[value]}</Text>
          </Card>
        ))}
      </View>

      {/* Lifecycle distribution */}
      <Text style={[styles.statsSectionTitle, { color: colors.text.primary }]}>生命周期状态</Text>
      <Card style={[styles.lifecycleCard, { borderWidth: 1, borderColor: colors.border.default }]}>
        {Object.entries(lifecycleDist).map(([label, count]) => (
          <View key={label} style={[styles.lifecycleRow, { borderBottomColor: colors.border.subtle }]}>
            <Text style={[styles.lifecycleLabel, { color: colors.text.primary }]}>{label}</Text>
            <Text style={[styles.lifecycleValue, { color: colors.text.secondary }]}>{count}</Text>
          </View>
        ))}
        {Object.keys(lifecycleDist).length === 0 && (
          <Text style={[styles.noDataText, { color: colors.text.tertiary }]}>暂无数据</Text>
        )}
      </Card>

      {/* Execution plans */}
      <Text style={[styles.statsSectionTitle, { color: colors.text.primary }]}>执行计划</Text>
      {executionPlans.length === 0 ? (
        <Card style={[styles.lifecycleCard, { borderWidth: 1, borderColor: colors.border.default }]}>
          <Text style={[styles.noDataText, { color: colors.text.tertiary }]}>暂无执行计划</Text>
        </Card>
      ) : (
        executionPlans.map((plan) => (
          <Card key={plan.id} style={[styles.planCard, { borderWidth: 1, borderColor: colors.border.default }]}>
            <Text style={[styles.planName, { color: colors.text.primary }]}>{plan.name}</Text>
            <View style={styles.planMeta}>
              <View
                style={[
                  styles.planStatusBadge,
                  {
                    backgroundColor:
                      plan.status === 'completed'
                        ? colors.status.successBg
                        : plan.status === 'in_progress'
                          ? colors.status.infoBg
                          : colors.bg.muted,
                  },
                ]}
              >
                <Text
                  style={[
                    styles.planStatusText,
                    {
                      color:
                        plan.status === 'completed'
                          ? colors.status.success
                          : plan.status === 'in_progress'
                            ? colors.status.info
                            : colors.text.secondary,
                    },
                  ]}
                >
                  {plan.status === 'completed'
                    ? '已完成'
                    : plan.status === 'in_progress'
                      ? '进行中'
                      : plan.status}
                </Text>
              </View>
              {plan.groups && (
                <Text style={[styles.planGroupCount, { color: colors.text.tertiary }]}>
                  {plan.groups.length} 个分组
                </Text>
              )}
            </View>
          </Card>
        ))
      )}
    </ScrollView>
  );
}

// ─── Main Screen ──────────────────────────────────────────────────

export default function ProjectDetailScreen() {
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();
  const router = useRouter();
  const { id } = useLocalSearchParams<{ id: string }>();
  const [viewModeIndex, setViewModeIndex] = useState(0);
  const [project, setProject] = useState<Project | null>(null);
  const [columns, setColumns] = useState<Column[]>([]);
  const [issues, setIssues] = useState<Issue[]>([]);
  const [executionPlans, setExecutionPlans] = useState<ExecutionPlan[]>([]);
  const [workspaceByIssueId, setWorkspaceByIssueId] = useState<Record<string, Workspace>>({});
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [showMenu, setShowMenu] = useState(false);
  const [showDeleteDialog, setShowDeleteDialog] = useState(false);
  const [selectedIssue, setSelectedIssue] = useState<Issue | null>(null);
  const createIssueSheetRef = useRef<BottomSheetModal>(null);

  // Navigate to a workspace detail page when a card workspace badge is tapped.
  const handleWorkspacePress = useCallback(
    (workspaceId: string) => {
      router.push(`/workspace/${workspaceId}`);
    },
    [router],
  );

  const viewMode = VIEW_MODE_VALUES[viewModeIndex];

  // Fetch all data in parallel, cache in local state.
  // /workspaces is fetched separately and joined client-side, mirroring the
  // web SPA's KanbanBoard pattern (single workspace list across all projects
  // is cheap; per-issue lookup would N+1).
  const fetchData = useCallback(async () => {
    try {
      const [proj, cols, iss, plans, wsList] = await Promise.all([
        api.get<Project>(`/projects/${id}`).catch(() => null),
        api.get<Column[]>(`/projects/${id}/columns`),
        api.get<Issue[]>(`/projects/${id}/issues`),
        api
          .get<ExecutionPlan[]>(`/projects/${id}/execution-plans`)
          .catch(() => []),
        api.get<Workspace[]>(`/workspaces`).catch(() => [] as Workspace[]),
      ]);
      setProject(proj);
      setColumns((cols ?? []).sort((a, b) => a.position - b.position));
      setIssues(iss ?? []);
      setExecutionPlans(plans ?? []);

      // Build issueId -> workspace map. Backend returns issue_id as string;
      // key by string for safe lookup.
      const map: Record<string, Workspace> = {};
      for (const ws of wsList ?? []) {
        if (ws.issue_id != null) {
          map[String(ws.issue_id)] = ws;
        }
      }
      setWorkspaceByIssueId(map);
    } catch (err) {
      console.warn('Project detail fetch error:', err);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [id]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    fetchData();
  }, [fetchData]);

  // Refetch data when kanban_update SSE event arrives for this project
  const fetchDataRef = useRef(fetchData);
  fetchDataRef.current = fetchData;

  useSSE((event) => {
    if (event.type === 'kanban_update') {
      const data = event.data as { project_id?: number };
      if (!data.project_id || data.project_id === Number(id)) {
        fetchDataRef.current();
      }
    }
  });

  // Group issues by column
  const issuesByColumn = useMemo(() => {
    const map = new Map<number, Issue[]>();
    for (const col of columns) {
      map.set(col.id, []);
    }
    for (const issue of issues) {
      const arr = map.get(issue.column_id);
      if (arr) arr.push(issue);
    }
    for (const arr of map.values()) {
      arr.sort((a, b) => a.position - b.position);
    }
    return map;
  }, [columns, issues]);

  const handleDeleteProject = useCallback(async () => {
    try {
      await api.delete(`/projects/${id}`);
      Alert.alert('已删除', '项目已成功删除');
    } catch {
      Alert.alert('删除失败', '无法删除项目，请稍后重试');
    }
  }, [id]);

  // ─── Loading state — keep header visible so user can navigate back ─
  if (loading) {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
        <Stack.Screen options={{ headerShown: false }} />
        <View style={[styles.header, { borderBottomColor: colors.border.default }]}>
          <Pressable onPress={() => router.back()} hitSlop={8} style={styles.headerIcon}>
            <Ionicons name="chevron-back" size={24} color={colors.text.primary} />
          </Pressable>
          <Text style={[styles.headerTitle, { color: colors.text.primary }]} numberOfLines={1}>
            {project?.name ?? '项目详情'}
          </Text>
          <View style={styles.headerIcon} />
        </View>
        <View style={styles.center}>
          <ActivityIndicator size="large" color={colors.brand.accent} />
        </View>
      </View>
    );
  }

  // ─── Main content ─────────────────────────────────────────────
  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
      <Stack.Screen options={{ headerShown: false }} />

      {/* Header: back + project title + overflow menu */}
      <View style={[styles.header, { borderBottomColor: colors.border.default }]}>
        <Pressable onPress={() => router.back()} hitSlop={8} style={styles.headerIcon}>
          <Ionicons name="chevron-back" size={24} color={colors.text.primary} />
        </Pressable>
        <Text
          style={[styles.headerTitle, { color: colors.text.primary }]}
          numberOfLines={1}
        >
          {project?.name ?? '项目详情'}
        </Text>
        <Pressable
          onPress={() => setShowMenu((v) => !v)}
          hitSlop={8}
          style={styles.headerIcon}
          accessibilityRole="button"
          accessibilityLabel="更多操作"
        >
          <Ionicons name="ellipsis-horizontal" size={22} color={colors.text.secondary} />
        </Pressable>
      </View>

      {/* View mode segmented control */}
      <View style={styles.modeBar}>
        <SegmentedControl
          options={VIEW_MODE_OPTIONS}
          selected={viewModeIndex}
          onSelect={setViewModeIndex}
        />
      </View>

      {/* Dropdown menu */}
      {showMenu && (
        <View style={styles.dropdownOverlay}>
          <Pressable style={styles.dropdownBackdrop} onPress={() => setShowMenu(false)} />
          <View style={[styles.dropdown, { backgroundColor: colors.bg.surface, borderColor: colors.border.default }]}>
            <TouchableOpacity
              style={styles.dropdownItem}
              onPress={() => {
                setShowMenu(false);
                setShowDeleteDialog(true);
              }}
            >
              <Ionicons name="trash-outline" size={18} color={colors.status.error} />
              <Text style={[styles.dropdownText, { color: colors.status.error }]}>
                删除项目
              </Text>
            </TouchableOpacity>
          </View>
        </View>
      )}

      {/* View content */}
      {viewMode === 'kanban' && (
        <View style={styles.viewContainer}>
          <KanbanView
            columns={columns}
            issuesByColumn={issuesByColumn}
            workspaceByIssueId={workspaceByIssueId}
            onIssuePress={setSelectedIssue}
            onWorkspacePress={handleWorkspacePress}
          />
        </View>
      )}
      {viewMode === 'list' && (
        <View style={styles.viewContainer}>
          <ListView
            columns={columns}
            issuesByColumn={issuesByColumn}
            workspaceByIssueId={workspaceByIssueId}
            onIssuePress={setSelectedIssue}
            onWorkspacePress={handleWorkspacePress}
          />
        </View>
      )}
      {viewMode === 'stats' && (
        <View style={styles.viewContainer}>
          <StatsView
            issues={issues}
            columns={columns}
            executionPlans={executionPlans}
          />
        </View>
      )}

      {/* FAB for new issue (kanban + list views only) */}
      {viewMode !== 'stats' && (
        <TouchableOpacity
          style={[styles.fab, { backgroundColor: colors.brand.accent }]}
          onPress={() => createIssueSheetRef.current?.present()}
        >
          <Ionicons name="add" size={28} color="#fff" />
        </TouchableOpacity>
      )}

      {/* Delete confirm dialog */}
      <ConfirmDialog
        visible={showDeleteDialog}
        title="删除项目"
        message="确定要删除此项目吗？此操作不可撤销，所有关联的 Issue、看板列和执行计划都将被永久删除。"
        confirmLabel="删除"
        onConfirm={handleDeleteProject}
        onCancel={() => setShowDeleteDialog(false)}
        destructive
      />

      {/* Issue detail sheet */}
      {selectedIssue !== null && (
        <IssueDetailSheet
          issueId={selectedIssue.id}
          initialIssue={selectedIssue}
          projectId={Number(id)}
          columns={columns}
          visible={true}
          onClose={() => setSelectedIssue(null)}
        />
      )}

      {/* Create issue sheet */}
      <CreateIssueSheet
        ref={createIssueSheetRef}
        columns={columns}
        onCreated={fetchData}
      />
    </View>
  );
}

// ─── Styles ──────────────────────────────────────────────────────

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  center: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },

  // ─── Header ────────────────────────────────────────────────────
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  headerIcon: {
    width: 36,
    height: 36,
    alignItems: 'center',
    justifyContent: 'center',
  },
  headerTitle: {
    ...typography.sectionHead,
    flex: 1,
  },

  // ─── Mode bar ──────────────────────────────────────────────────
  modeBar: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm,
  },

  // ─── Dropdown menu ───────────────────────────────────────────
  dropdownOverlay: {
    position: 'absolute',
    // header is roughly 36 (icon) + 2*spacing.sm padding + hairline ≈ 53; clear it.
    top: 56,
    right: spacing.md,
    zIndex: 100,
  },
  dropdownBackdrop: {
    position: 'absolute',
    top: -20,
    left: -40,
    right: -20,
    bottom: -100,
  },
  dropdown: {
    borderRadius: radius.md,
    borderWidth: 1,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.15,
    shadowRadius: 12,
    elevation: 8,
    minWidth: 160,
    overflow: 'hidden',
  },
  dropdownItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
    gap: spacing.md,
  },
  dropdownText: {
    ...typography.body,
  },

  // ─── View container ──────────────────────────────────────────
  viewContainer: {
    flex: 1,
  },

  // ─── Kanban view ─────────────────────────────────────────────
  kanbanContainer: {
    flex: 1,
  },
  columnPickerWrap: {
    paddingHorizontal: spacing.lg,
    marginBottom: spacing.md,
  },
  kanbanListContent: {
    paddingHorizontal: spacing.lg,
    paddingBottom: spacing['3xl'],
  },
  columnEmpty: {
    paddingVertical: spacing['3xl'],
    alignItems: 'center',
  },
  columnEmptyText: {
    ...typography.body,
  },
  emptyView: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingTop: 80,
  },
  emptyText: {
    ...typography.body,
  },

  // ─── List view ───────────────────────────────────────────────
  listViewContent: {
    paddingHorizontal: spacing.lg,
    paddingBottom: spacing['3xl'],
  },
  sectionHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: spacing.sm,
  },
  sectionHeaderLeft: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
  statusSquare: {
    width: 8,
    height: 8,
    borderRadius: 2,
  },
  sectionHeaderText: {
    ...typography.label,
    fontWeight: '600',
  },
  sectionHeaderRight: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
  sectionHeaderCount: {
    ...typography.caption,
  },

  // ─── Stats view ──────────────────────────────────────────────
  statsContent: {
    paddingHorizontal: spacing.lg,
    paddingBottom: spacing['3xl'],
    gap: spacing.lg,
  },
  statsSectionTitle: {
    ...typography.sectionHead,
    marginTop: spacing.sm,
  },
  statsGrid: {
    flexDirection: 'row',
    gap: spacing.md,
  },
  statCard: {
    flex: 1,
    alignItems: 'center',
    gap: spacing.xs,
  },
  statDot: {
    width: 12,
    height: 12,
    borderRadius: 6,
  },
  statLabel: {
    ...typography.caption,
  },
  statValue: {
    ...typography.sectionHead,
  },
  lifecycleCard: {},
  lifecycleRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingVertical: spacing.sm,
  },
  lifecycleLabel: {
    ...typography.body,
  },
  lifecycleValue: {
    ...typography.bodyMedium,
  },
  noDataText: {
    ...typography.body,
    textAlign: 'center',
    paddingVertical: spacing.md,
  },

  // ─── Execution plan cards ────────────────────────────────────
  planCard: {
    marginBottom: spacing.sm,
  },
  planName: {
    ...typography.bodyMedium,
  },
  planMeta: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.md,
    marginTop: spacing.sm,
  },
  planStatusBadge: {
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: radius.xs,
  },
  planStatusText: {
    fontSize: 12,
    fontWeight: '500',
  },
  planGroupCount: {
    ...typography.caption,
  },

  // ─── FAB ─────────────────────────────────────────────────────
  fab: {
    position: 'absolute',
    bottom: spacing.xl,
    right: spacing.xl,
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
});
