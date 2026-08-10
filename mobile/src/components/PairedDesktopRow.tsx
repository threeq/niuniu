import React from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { useTranslation } from 'react-i18next';
import type { PairedDesktop } from '../stores/pairedDesktopsStore';
import { useThemeColors } from '../theme/useTheme';
import { radius, shadows, spacing, typography } from '../theme/tokens';
import { formatRelativeTime } from '../utils/formatTime';

export interface PairedDesktopRowProps {
  desktop: PairedDesktop;
  isActive?: boolean;
  onPress: () => void;
  onLongPress?: () => void;
}

/**
 * Single row in a list of paired desktops. Shared between
 * `(auth)/relay-desktops.tsx` (the post-login picker) and
 * `settings/paired-desktops.tsx` (the in-app management page).
 *
 * Markup mirrors the original inline implementation at
 * relay-desktops.tsx:185-222; the only addition is the optional Active
 * badge driven by `isActive`.
 */
export function PairedDesktopRow({
  desktop,
  isActive,
  onPress,
  onLongPress,
}: PairedDesktopRowProps) {
  const { t } = useTranslation();
  const colors = useThemeColors();

  return (
    <Pressable
      testID="paired-desktop-row"
      accessibilityRole="button"
      accessibilityLabel={desktop.desktopName}
      style={({ pressed }) => [
        styles.card,
        {
          backgroundColor: pressed ? colors.bg.muted : colors.bg.surface,
          borderColor: colors.border.default,
        },
        shadows.sm,
      ]}
      onPress={onPress}
      onLongPress={onLongPress}
    >
      <View style={styles.cardMain}>
        <View style={[styles.icon, { backgroundColor: colors.brand.accentLight }]}>
          <Text style={[styles.iconText, { color: colors.brand.accent }]}>N</Text>
        </View>
        <View style={styles.info}>
          <View style={styles.nameRow}>
            <Text style={[styles.name, { color: colors.text.primary }]} numberOfLines={1}>
              {desktop.desktopName}
            </Text>
            {isActive ? (
              <View
                testID="paired-desktop-row-active-badge"
                style={[styles.badge, { backgroundColor: colors.brand.accentLight }]}
              >
                <Text style={[styles.badgeText, { color: colors.brand.accent }]}>
                  {t('desktops.activeBadge')}
                </Text>
              </View>
            ) : null}
          </View>
          <Text style={[styles.id, { color: colors.text.tertiary }]}>
            {desktop.desktopId.slice(0, 12)}...
          </Text>
        </View>
        <View style={styles.meta}>
          {desktop.pairedAt ? (
            <Text style={[styles.pairedAt, { color: colors.text.tertiary }]}>
              {formatRelativeTime(desktop.pairedAt)}
            </Text>
          ) : null}
          <Text style={[styles.chevron, { color: colors.text.tertiary }]}>›</Text>
        </View>
      </View>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  card: {
    borderRadius: radius.md,
    borderWidth: 1,
    overflow: 'hidden',
  },
  cardMain: {
    flexDirection: 'row',
    alignItems: 'center',
    padding: spacing.lg,
    gap: spacing.md,
  },
  icon: {
    width: 40,
    height: 40,
    borderRadius: radius.sm,
    alignItems: 'center',
    justifyContent: 'center',
  },
  iconText: {
    fontSize: 18,
    fontWeight: '700',
  },
  info: {
    flex: 1,
  },
  nameRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
  name: {
    ...typography.bodyMedium,
    flexShrink: 1,
  },
  badge: {
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
    borderRadius: radius.sm,
  },
  badgeText: {
    ...typography.caption,
    fontWeight: '600',
  },
  id: {
    ...typography.caption,
    marginTop: spacing.xs,
    fontFamily: 'monospace',
  },
  meta: {
    alignItems: 'flex-end',
    gap: spacing.xs,
  },
  pairedAt: {
    ...typography.caption,
  },
  chevron: {
    fontSize: 20,
    lineHeight: 22,
  },
});
