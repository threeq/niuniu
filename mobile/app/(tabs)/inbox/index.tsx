import React, { useCallback, useState } from 'react';
import { View, ScrollView, Text, Pressable, RefreshControl, StyleSheet } from 'react-native';
import { useRouter } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useThemeColors } from '../../../src/theme/useTheme';
import { typography, spacing } from '../../../src/theme/tokens';
import { useNotificationStore } from '../../../src/stores/notificationStore';
import { AgentCard } from '../../../src/components/AgentCard';
import { EmptyState } from '../../../src/components/EmptyState';
import { PageHeader } from '../../../src/components/PageHeader';

export default function InboxScreen() {
  const colors = useThemeColors();
  const router = useRouter();
  const insets = useSafeAreaInsets();
  const { notifications, unreadCount, markRead, markAllRead, loadNotifications } = useNotificationStore();

  const [refreshing, setRefreshing] = useState(false);

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await loadNotifications();
    setRefreshing(false);
  }, [loadNotifications]);

  return (
    <View style={[styles.screen, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
      <PageHeader
        title="通知"
        subtitle={unreadCount > 0 ? `${unreadCount} 条未读` : undefined}
        rightActions={
          unreadCount > 0 ? (
            <Pressable onPress={markAllRead} hitSlop={8}>
              <Text style={[typography.label, { color: colors.brand.accent }]}>全部标记已读</Text>
            </Pressable>
          ) : undefined
        }
      />
      <ScrollView
        contentContainerStyle={styles.scrollContent}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
      >
        {notifications.length === 0 ? (
          <EmptyState title="No agent activity" description="Agent notifications will appear here" />
        ) : (
          <View style={styles.list}>
            {notifications.map((n) => (
              <AgentCard
                key={n.workspaceId}
                name={n.workspaceName}
                status={n.status}
                summary={n.summary}
                costUsd={n.costUsd}
                turns={n.turns}
                updatedAt={n.updatedAt}
                unread={!n.read}
                onPress={() => {
                  markRead(n.workspaceId);
                  router.push(`/workspace/${n.workspaceId}`);
                }}
              />
            ))}
          </View>
        )}
      </ScrollView>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  scrollContent: { paddingBottom: spacing['2xl'] },
  list: {
    paddingHorizontal: spacing.lg + 4,
    gap: spacing.sm,
  },
});
