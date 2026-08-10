/**
 * settings/paired-desktops.tsx — manage paired desktops from in-app settings.
 *
 * Differences from `(auth)/relay-desktops.tsx`:
 *   - No custom header. Stack-provided header (back arrow + title) from
 *     _layout.tsx is the only header. No insets.top padding (Stack header
 *     already accounts for the safe area).
 *   - Tap switches active desktop in place; does not navigate.
 *   - No Logout button, no Manual Connect link, no email subtitle.
 *   - When the user arrives in `'direct'` transport mode, tapping a row
 *     also flips kind to `'relay'` so apiClient.smartFetch routes through
 *     the encrypted path (see apiClient.ts:646).
 */

import { router } from 'expo-router';
import {
  ActivityIndicator,
  Alert,
  FlatList,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useTranslation } from 'react-i18next';
import { PairedDesktopRow } from '../../src/components/PairedDesktopRow';
import { useSyncPairedDesktops } from '../../src/hooks/useSyncPairedDesktops';
import { usePairedDesktopsStore } from '../../src/stores/pairedDesktopsStore';
import { useTransportStore } from '../../src/stores/transportStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

export default function PairedDesktopsSettingsScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();

  const desktops = usePairedDesktopsStore((s) => s.desktops);
  const removeDesktop = usePairedDesktopsStore((s) => s.removeDesktop);

  const activeDesktopId = useTransportStore((s) => s.activeDesktopId);
  const activeTransportKind = useTransportStore((s) => s.activeTransportKind);
  const setActiveDesktopId = useTransportStore((s) => s.setActiveDesktopId);
  const setTransportKind = useTransportStore((s) => s.setTransportKind);
  const removeCipher = useTransportStore((s) => s.removeCipher);

  const { refresh, initialLoading, isRefreshing } = useSyncPairedDesktops();

  const handleSelect = (id: string) => {
    if (activeDesktopId && activeDesktopId !== id) {
      // Drop the cached cipher for the previous desktop; it is bound to
      // that desktop's key material and useless for the new one.
      removeCipher(activeDesktopId);
    }
    setActiveDesktopId(id);
    if (activeTransportKind !== 'relay') {
      // smartFetch only routes through the encrypted relay path when
      // kind === 'relay' (apiClient.ts:646). If the user reached settings
      // while still in 'direct' (post-resetActiveTransport, etc.), flip it
      // so subsequent API calls actually use the newly-selected desktop.
      setTransportKind('relay');
    }
    // No navigation: the user is in settings managing the list.
  };

  const handleLongPress = (id: string, name: string) => {
    Alert.alert(
      t('desktops.unpairConfirmTitle', { name }),
      t('desktops.unpairConfirmMessage'),
      [
        { text: t('pairing.scan.cancelBtn'), style: 'cancel' },
        {
          text: t('desktops.unpairConfirmBtn'),
          style: 'destructive',
          onPress: () => removeDesktop(id),
        },
      ],
    );
  };

  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
      <FlatList
        data={desktops}
        keyExtractor={(item) => item.desktopId}
        onRefresh={refresh}
        refreshing={isRefreshing}
        contentContainerStyle={styles.listContent}
        ListEmptyComponent={
          initialLoading ? (
            <View style={styles.emptyState}>
              <ActivityIndicator size="small" color={colors.brand.accent} />
            </View>
          ) : (
            <View style={styles.emptyState}>
              <Text style={styles.emptyIcon}>💻</Text>
              <Text style={[styles.emptyTitle, { color: colors.text.primary }]}>
                {t('desktops.emptyTitle')}
              </Text>
              <Text style={[styles.emptyDesc, { color: colors.text.secondary }]}>
                {t('desktops.emptyDesc')}
              </Text>
            </View>
          )
        }
        renderItem={({ item }) => (
          <PairedDesktopRow
            desktop={item}
            isActive={item.desktopId === activeDesktopId}
            onPress={() => handleSelect(item.desktopId)}
            onLongPress={() => handleLongPress(item.desktopId, item.desktopName)}
          />
        )}
      />
      <View style={[styles.footer, { borderTopColor: colors.border.default }]}>
        <Pressable
          style={[styles.pairBtn, { backgroundColor: colors.brand.accent }]}
          onPress={() => router.push('/(auth)/pair-scan')}
        >
          <Text style={styles.pairBtnText}>{t('desktops.pairNewBtn')}</Text>
        </Pressable>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  listContent: {
    padding: spacing.xl,
    gap: spacing.md,
    flexGrow: 1,
  },
  emptyState: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: spacing['3xl'],
    paddingHorizontal: spacing['2xl'],
  },
  emptyIcon: {
    fontSize: 48,
    marginBottom: spacing.xl,
  },
  emptyTitle: {
    ...typography.sectionHead,
    textAlign: 'center',
    marginBottom: spacing.md,
  },
  emptyDesc: {
    ...typography.body,
    textAlign: 'center',
    lineHeight: 22,
  },
  footer: {
    padding: spacing.xl,
    paddingBottom: 40,
    borderTopWidth: 1,
  },
  pairBtn: {
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
  },
  pairBtnText: {
    ...typography.bodyMedium,
    color: '#FFFFFF',
  },
});
