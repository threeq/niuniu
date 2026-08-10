import React, { useState, useRef, useCallback, forwardRef, useImperativeHandle } from 'react';
import { View, Text, TextInput, TouchableOpacity, StyleSheet, ScrollView, Alert } from 'react-native';
import BottomSheetView, { BottomSheetBackdrop } from '@gorhom/bottom-sheet';
import type { BottomSheetBackdropProps } from '@gorhom/bottom-sheet';
import Ionicons from '@expo/vector-icons/Ionicons';
import { useProjectFilterStore } from '../stores/projectFilterStore';
import { useServerStore } from '../stores/serverStore';
import { useAuthStore } from '../stores/authStore';
import { api } from '../api/client';
import type { Project } from '../api/types';
import { useThemeColors } from '../theme/useTheme';
import { typography, spacing, radius } from '../theme/tokens';

export interface ProjectDrawerRef {
  open: () => void;
  close: () => void;
}

export const ProjectDrawer = forwardRef<ProjectDrawerRef>(function ProjectDrawer(_props, ref) {
  const colors = useThemeColors();
  const bottomSheetRef = useRef<BottomSheetView>(null);

  useImperativeHandle(ref, () => ({
    open: () => bottomSheetRef.current?.snapToIndex(0),
    close: () => bottomSheetRef.current?.close(),
  }));
  const { selectedProjectId, setProject } = useProjectFilterStore();
  const [projects, setProjects] = useState<Project[]>([]);
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(false);

  const loadProjects = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<Project[]>('/projects');
      setProjects(data);
    } catch { /* ignore */ }
    setLoading(false);
  }, []);

  React.useEffect(() => { loadProjects(); }, []);

  const filtered = search
    ? projects.filter((p) => p.name.toLowerCase().includes(search.toLowerCase()))
    : projects;

  const handleSelect = (id: number | null) => {
    setProject(id);
    bottomSheetRef.current?.close();
  };

  const handleLogout = () => {
    Alert.alert('退出登录', '确定要退出登录吗？', [
      { text: '取消', style: 'cancel' },
      { text: '退出', style: 'destructive', onPress: async () => {
        const server = useServerStore.getState().getActiveServer();
        if (server) {
          try { await api.post('/auth/logout', { refresh_token: '' }); } catch {}
        }
        useAuthStore.getState().clearTokens();
      }},
    ]);
  };

  const renderBackdrop = useCallback(
    (props: BottomSheetBackdropProps) => (
      <BottomSheetBackdrop {...props} disappearsOnIndex={-1} appearsOnIndex={0} opacity={0.5} />
    ),
    [],
  );

  return (
    <BottomSheetView
      ref={bottomSheetRef}
      index={-1}
      snapPoints={['70%']}
      enablePanDownToClose
      backdropComponent={renderBackdrop}
      backgroundStyle={{ backgroundColor: colors.bg.surface, borderRadius: 20 }}
      handleIndicatorStyle={{ backgroundColor: colors.text.tertiary, width: 36 }}
    >
      <View style={styles.header}>
        <Text style={[styles.headerTitle, { color: colors.text.primary }]}>项目筛选</Text>
      </View>

      <View style={styles.searchContainer}>
        <TextInput
          style={[styles.searchInput, { backgroundColor: colors.bg.muted, color: colors.text.primary }]}
          placeholder="搜索项目..."
          placeholderTextColor={colors.text.tertiary}
          value={search}
          onChangeText={setSearch}
          autoCapitalize="none"
        />
      </View>

      <ScrollView style={styles.list}>
        <TouchableOpacity
          style={[styles.item, { backgroundColor: colors.bg.muted }, !selectedProjectId && { backgroundColor: colors.brand.accentLight, borderWidth: 1, borderColor: colors.brand.accent }]}
          onPress={() => handleSelect(null)}
        >
          <Text style={[styles.itemText, { color: colors.text.primary }]}>全部项目</Text>
        </TouchableOpacity>
        {loading && <Text style={[styles.loadingText, { color: colors.text.tertiary }]}>加载中...</Text>}
        {filtered.map((p) => (
          <TouchableOpacity
            key={p.id}
            style={[styles.item, { backgroundColor: colors.bg.muted }, selectedProjectId === p.id && { backgroundColor: colors.brand.accentLight, borderWidth: 1, borderColor: colors.brand.accent }]}
            onPress={() => handleSelect(p.id)}
          >
            <Text style={[styles.itemText, { color: colors.text.primary }]}>{p.name}</Text>
            <Text style={[styles.itemSub, { color: colors.text.secondary }]}>
              {p.issue_stats?.map((s) => `${s.column_name}: ${s.count}`).join(', ')}
            </Text>
          </TouchableOpacity>
        ))}
      </ScrollView>

      <View style={styles.footer}>
        <View style={[styles.divider, { backgroundColor: colors.border.default }]} />
        <TouchableOpacity style={styles.footerItem} onPress={handleLogout}>
          <Ionicons name="log-out-outline" size={20} color={colors.status.error} />
          <Text style={[styles.footerText, { color: colors.status.error }]}>退出登录</Text>
        </TouchableOpacity>
      </View>
    </BottomSheetView>
  );
});

const styles = StyleSheet.create({
  header: { paddingHorizontal: spacing.xl, paddingTop: spacing.lg, paddingBottom: spacing.md },
  headerTitle: { ...typography.sectionHead },
  searchContainer: { paddingHorizontal: spacing.xl, marginBottom: spacing.md },
  searchInput: {
    borderRadius: radius.sm,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
    fontSize: 15,
  },
  list: { flex: 1, paddingHorizontal: spacing.xl },
  item: {
    paddingVertical: spacing.md,
    paddingHorizontal: spacing.lg,
    borderRadius: radius.sm,
    marginBottom: spacing.xs,
  },
  itemText: { ...typography.body },
  itemSub: { ...typography.caption, marginTop: spacing.xs },
  loadingText: { ...typography.body, padding: spacing.lg },
  footer: { paddingHorizontal: spacing.xl, paddingBottom: spacing.xl },
  divider: { height: 1, marginVertical: spacing.md },
  footerItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: spacing.md,
    paddingHorizontal: spacing.lg,
    gap: spacing.md,
  },
  footerText: { ...typography.body },
});
