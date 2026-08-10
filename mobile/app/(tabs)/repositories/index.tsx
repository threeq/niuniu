import { router } from 'expo-router';
import React, { useMemo, useState } from 'react';
import {
  FlatList,
  Pressable,
  RefreshControl,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import Ionicons from '@expo/vector-icons/Ionicons';
import { api } from '../../../src/api/client';
import type { Repository } from '../../../src/api/types';
import { EmptyState, SkeletonCard } from '../../../src/components';
import { Card } from '../../../src/components/Card';
import { FloatingNotificationButton } from '../../../src/components/FloatingNotificationButton';
import { PageHeader } from '../../../src/components/PageHeader';
import { SearchBar } from '../../../src/components/SearchBar';
import { useApi } from '../../../src/hooks/useApi';
import { useThemeColors } from '../../../src/theme/useTheme';
import { typography, spacing, radius } from '../../../src/theme/tokens';
import { formatRelativeTime } from '../../../src/utils/formatTime';

// ─── Repository card ──────────────────────────────────────────────

interface RepoCardProps {
  repo: Repository;
  onPress: () => void;
}

function RepoCard({ repo, onPress }: RepoCardProps) {
  const colors = useThemeColors();

  return (
    <Pressable onPress={onPress}>
      <Card>
        {/* Name row */}
        <View style={styles.cardHeader}>
          <Ionicons name="git-branch-outline" size={16} color={colors.brand.accent} />
          <Text style={[styles.repoName, { color: colors.text.primary }]} numberOfLines={1}>
            {repo.name}
          </Text>
          <Text style={[styles.timeText, { color: colors.text.tertiary }]}>
            {formatRelativeTime(repo.updated_at)}
          </Text>
        </View>

        {/* Path */}
        <Text style={[styles.repoPath, { color: colors.text.secondary }]} numberOfLines={1}>
          {repo.path}
        </Text>

        {/* Footer: default branch */}
        {repo.default_branch ? (
          <View style={styles.branchRow}>
            <View style={[styles.branchTag, { backgroundColor: colors.bg.muted }]}>
              <Ionicons name="git-commit-outline" size={11} color={colors.text.tertiary} />
              <Text style={[styles.branchText, { color: colors.text.tertiary }]}>
                {repo.default_branch}
              </Text>
            </View>
          </View>
        ) : null}
      </Card>
    </Pressable>
  );
}

// ─── Main Screen ─────────────────────────────────────────────────

export default function RepositoriesScreen() {
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();
  const [search, setSearch] = useState('');
  const [showSearch, setShowSearch] = useState(false);

  const { data: repositories, loading, error, refetch } = useApi<Repository[]>(
    () => api.get<Repository[]>('/repositories'),
    [],
  );

  const filtered = useMemo(() => {
    const list = repositories ?? [];
    if (!search.trim()) return list;
    const q = search.trim().toLowerCase();
    return list.filter(
      (r) => r.name.toLowerCase().includes(q) || r.path.toLowerCase().includes(q),
    );
  }, [repositories, search]);

  const isInitialLoading = loading && !repositories;

  const header = (
    <PageHeader
      title="仓库"
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
  if (error && !repositories) {
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
      <FlatList
        data={filtered}
        keyboardDismissMode="on-drag"
        keyExtractor={(item) => String(item.id)}
        renderItem={({ item }) => (
          <RepoCard
            repo={item}
            onPress={() => router.push(`/repository/${item.id}`)}
          />
        )}
        contentContainerStyle={styles.listContent}
        showsVerticalScrollIndicator={false}
        ItemSeparatorComponent={() => <View style={{ height: spacing.sm }} />}
        refreshControl={
          <RefreshControl
            refreshing={loading && !!repositories}
            onRefresh={refetch}
            tintColor={colors.brand.accent}
          />
        }
        ListEmptyComponent={
          <EmptyState
            title="暂无仓库"
            description={search ? '没有符合条件的仓库' : '添加仓库来管理你的代码'}
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
        placeholder="搜索仓库..."
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
  skeletonList: {
    paddingHorizontal: spacing.lg,
    gap: spacing.md,
  },
  listContent: {
    paddingHorizontal: spacing.lg,
    paddingBottom: spacing['3xl'],
  },
  // Card internals
  cardHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    marginBottom: spacing.xs,
  },
  repoName: {
    ...typography.bodyMedium,
    flex: 1,
  },
  timeText: {
    ...typography.caption,
    flexShrink: 0,
  },
  repoPath: {
    ...typography.label,
    marginBottom: spacing.sm,
    paddingLeft: 24,
  },
  branchRow: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  branchTag: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    paddingHorizontal: 8,
    paddingVertical: 3,
    borderRadius: radius.xs,
  },
  branchText: {
    fontSize: 11,
    fontWeight: '500',
  },
});
