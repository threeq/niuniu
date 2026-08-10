import { router } from 'expo-router';
import React, { useMemo, useState } from 'react';
import {
  Pressable,
  RefreshControl,
  StyleSheet,
  View,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import Ionicons from '@expo/vector-icons/Ionicons';
import { api } from '../../../src/api/client';
import type { Workspace } from '../../../src/api/types';
import {
  EmptyState,
  FilterChips,
  GroupedWorkspaceList,
  SkeletonCard,
} from '../../../src/components';
import { FloatingNotificationButton } from '../../../src/components/FloatingNotificationButton';
import { PageHeader } from '../../../src/components/PageHeader';
import { SearchBar } from '../../../src/components/SearchBar';
import { useApi } from '../../../src/hooks/useApi';
import { useThemeColors } from '../../../src/theme/useTheme';
import { spacing } from '../../../src/theme/tokens';
import { mapWorkspaceStatus } from '../../../src/utils/workspaceCardHelpers';
import { groupWorkspaces } from '../../../src/utils/groupWorkspaces';

// ─── Status filter options ────────────────────────────────────────

// Filter values are workspaces.status (the canonical workflow state),
// matching what the badge displays. Chinese labels mirror the web's
// zh-CN i18n at server/web/src/i18n/locales/zh-CN/workspaces.json status.*.
const STATUS_FILTERS = [
  { label: '全部', value: 'all' },
  { label: '运行中', value: 'running' },
  { label: '待审核', value: 'needs_review' },
  { label: '需关注', value: 'attention' },
  { label: '已完成', value: 'completed' },
  { label: '已创建', value: 'created' },
];

// ─── Main Screen ─────────────────────────────────────────────────

export default function WorkspacesScreen() {
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();
  const [search, setSearch] = useState('');
  const [showSearch, setShowSearch] = useState(false);
  const [statusFilter, setStatusFilter] = useState('all');

  const { data: workspaces, loading, error, refetch } = useApi<Workspace[]>(
    () => api.get<Workspace[]>('/workspaces'),
    [],
  );

  const filtered = useMemo(() => {
    let list = workspaces ?? [];
    if (statusFilter !== 'all') {
      list = list.filter((ws) => mapWorkspaceStatus(ws) === statusFilter);
    }
    if (search.trim()) {
      const q = search.trim().toLowerCase();
      list = list.filter((ws) => ws.name.toLowerCase().includes(q));
    }
    return list;
  }, [workspaces, statusFilter, search]);

  const sections = useMemo(
    () => groupWorkspaces(filtered, { statsSource: workspaces ?? [] }),
    [filtered, workspaces],
  );
  const isSearchActive = search.trim() !== '';

  const isInitialLoading = loading && !workspaces;

  const header = (
    <PageHeader
      title="工作空间"
      leftIcon={{ name: 'settings-outline', onPress: () => router.push('/settings'), accessibilityLabel: '设置' }}
      rightActions={
        <Pressable
          onPress={() => {
            if (showSearch) setSearch('');
            setShowSearch((v) => !v);
          }}
          hitSlop={8}
          accessibilityRole="button"
          accessibilityLabel={showSearch ? '关闭搜索' : '搜索'}
        >
          <Ionicons
            name={showSearch ? 'close-outline' : 'search-outline'}
            size={22}
            color={colors.text.secondary}
          />
        </Pressable>
      }
    />
  );

  // ─── Body ────────────────────────────────────────────────────
  let body: React.ReactNode;
  if (error && !workspaces) {
    body = (
      <EmptyState
        title="加载失败"
        description={error}
        actionLabel="重试"
        onAction={refetch}
      />
    );
  } else if (isInitialLoading) {
    body = (
      <View style={styles.skeletonList}>
        <SkeletonCard />
        <SkeletonCard />
        <SkeletonCard />
      </View>
    );
  } else {
    body = (
      <GroupedWorkspaceList
        sections={sections}
        isSearchActive={isSearchActive}
        onWorkspacePress={(ws) => router.push(`/workspace/${ws.id}`)}
        refreshControl={
          <RefreshControl
            refreshing={loading && !!workspaces}
            onRefresh={refetch}
            tintColor={colors.brand.accent}
          />
        }
        ListHeaderComponent={
          <View style={styles.filterWrap}>
            <FilterChips
              options={STATUS_FILTERS}
              selected={statusFilter}
              onSelect={setStatusFilter}
            />
          </View>
        }
        ListEmptyComponent={
          <EmptyState
            title="暂无工作空间"
            description={
              search || statusFilter !== 'all'
                ? '没有符合条件的工作空间'
                : '创建工作空间来开始工作'
            }
          />
        }
      />
    );
  }

  // ─── Single return ───────────────────────────────────────────
  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
      {header}

      <SearchBar
        visible={showSearch}
        value={search}
        onChange={setSearch}
        placeholder="搜索工作空间..."
      />

      {body}
      <FloatingNotificationButton />
    </View>
  );
}

// ─── Styles ──────────────────────────────────────────────────────

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  filterWrap: {
    marginBottom: spacing.md,
  },
  skeletonList: {
    paddingHorizontal: spacing.lg,
    gap: spacing.md,
  },
});
