import { FlashList } from '@shopify/flash-list';
import { router } from 'expo-router';
import React, { useMemo, useRef, useState } from 'react';
import {
  Pressable,
  RefreshControl,
  StyleSheet,
  View,
} from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import type { BottomSheetModal } from '@gorhom/bottom-sheet';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { api } from '../../../src/api/client';
import type { Project } from '../../../src/api/types';
import {
  CreateProjectSheet,
  EmptyState,
  ProjectCard,
  SkeletonCard,
} from '../../../src/components';
import { FloatingNotificationButton } from '../../../src/components/FloatingNotificationButton';
import { PageHeader } from '../../../src/components/PageHeader';
import { SearchBar } from '../../../src/components/SearchBar';
import { useApi } from '../../../src/hooks/useApi';
import { useProjectFilterStore } from '../../../src/stores/projectFilterStore';
import { useThemeColors } from '../../../src/theme/useTheme';
import { spacing, radius } from '../../../src/theme/tokens';

// ─── Main Screen ─────────────────────────────────────────────────

export default function ProjectsScreen() {
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();
  const { selectedProjectId } = useProjectFilterStore();
  const [search, setSearch] = useState('');
  const [showSearch, setShowSearch] = useState(false);
  const createSheetRef = useRef<BottomSheetModal>(null);

  const { data: projects, loading, error, refetch } = useApi<Project[]>(
    () => api.get<Project[]>('/projects'),
    [],
  );

  const openCreateSheet = () => createSheetRef.current?.present();

  const filteredProjects = useMemo(() => {
    let list = projects ?? [];
    // Client-side project filter from store
    if (selectedProjectId !== null) {
      list = list.filter((p) => p.id === selectedProjectId);
    }
    // Client-side search
    if (search.trim()) {
      const q = search.trim().toLowerCase();
      list = list.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          (p.description ?? '').toLowerCase().includes(q),
      );
    }
    return list;
  }, [projects, selectedProjectId, search]);

  const isInitialLoading = loading && !projects;

  const header = (
    <PageHeader
      title="项目"
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
  if (error && !projects) {
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
      <FlashList
        data={filteredProjects}
        keyboardDismissMode="on-drag"
        keyExtractor={(item) => String(item.id)}
        renderItem={({ item }) => (
          <ProjectCard
            project={item}
            onPress={() => router.push(`/project/${item.id}`)}
          />
        )}
        ItemSeparatorComponent={() => <View style={styles.separator} />}
        contentContainerStyle={styles.listContent}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl
            refreshing={loading}
            onRefresh={refetch}
            tintColor={colors.brand.accent}
          />
        }
        ListEmptyComponent={
          <EmptyState
            title="暂无项目"
            description="创建项目来管理你的任务和看板"
            actionLabel="创建第一个项目"
            onAction={openCreateSheet}
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
        placeholder="搜索项目..."
      />

      {body}

      <Pressable
        onPress={openCreateSheet}
        accessibilityRole="button"
        accessibilityLabel="创建项目"
        style={[styles.fab, { backgroundColor: colors.brand.accent }]}
      >
        <Ionicons name="add" size={28} color="#fff" />
      </Pressable>

      <CreateProjectSheet ref={createSheetRef} onCreated={refetch} />

      <FloatingNotificationButton />
    </View>
  );
}

// ─── Styles ──────────────────────────────────────────────────────

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  separator: {
    height: spacing.md,
  },
  skeletonList: {
    paddingHorizontal: spacing.lg,
    gap: spacing.md,
  },
  listContent: {
    paddingHorizontal: spacing.lg,
    paddingBottom: spacing['3xl'],
  },
  fab: {
    position: 'absolute',
    right: spacing.lg,
    bottom: spacing.lg,
    width: 56,
    height: 56,
    borderRadius: radius.full,
    alignItems: 'center',
    justifyContent: 'center',
    shadowColor: '#000',
    shadowOpacity: 0.18,
    shadowRadius: 8,
    shadowOffset: { width: 0, height: 3 },
    elevation: 6,
  },
});
