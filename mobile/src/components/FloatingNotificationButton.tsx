import React from 'react';
import { Pressable, View, Text, StyleSheet } from 'react-native';
import { useRouter } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import { useThemeColors } from '../theme/useTheme';
import { spacing, radius } from '../theme/tokens';
import { useNotificationStore } from '../stores/notificationStore';

/**
 * 悬浮通知按钮（FAB）。仅在 `useNotificationStore.unreadCount > 0` 时渲染，
 * 点击进入 `/(tabs)/inbox`。
 *
 * 设计前提：底部 tab 栏使用默认非绝对定位（screen content 自然处于 tab 栏之上），
 * 因此用 `bottom: spacing.md` 即可让 FAB 紧贴 tab 栏上方。挂载位置应为各 tab
 * 索引页主 return 的最外层 View 内（不应挂在 tab navigator 之外的页面）。
 */
export function FloatingNotificationButton() {
  const colors = useThemeColors();
  const router = useRouter();
  const unreadCount = useNotificationStore((s) => s.unreadCount);

  if (unreadCount <= 0) return null;

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`${unreadCount} 条未读通知`}
      accessibilityHint="打开通知列表"
      onPress={() => router.push('/(tabs)/inbox')}
      style={[
        styles.button,
        {
          backgroundColor: colors.brand.accent,
          right: spacing.lg,
          bottom: spacing.md,
        },
      ]}
    >
      <Ionicons name="notifications" size={24} color="#FFFFFF" />
      <View
        style={[
          styles.badge,
          { backgroundColor: colors.status.error, borderColor: colors.bg.base },
        ]}
      >
        <Text style={styles.badgeText}>{unreadCount > 99 ? '99+' : unreadCount}</Text>
      </View>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  button: {
    position: 'absolute',
    width: 56,
    height: 56,
    borderRadius: radius.full,
    alignItems: 'center',
    justifyContent: 'center',
    shadowColor: '#000',
    shadowOpacity: 0.15,
    shadowRadius: 6,
    shadowOffset: { width: 0, height: 2 },
    elevation: 4,
  },
  badge: {
    position: 'absolute',
    top: 4,
    right: 4,
    minWidth: 22,
    height: 18,
    paddingHorizontal: 4,
    borderRadius: radius.full,
    alignItems: 'center',
    justifyContent: 'center',
    borderWidth: 2,
  },
  badgeText: {
    fontSize: 10,
    fontWeight: '800',
    color: '#FFFFFF',
  },
});
