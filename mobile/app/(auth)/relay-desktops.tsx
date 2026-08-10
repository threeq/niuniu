/**
 * relay-desktops.tsx — lists desktops paired to the current relay account.
 *
 * Placed in (auth)/ because it is entered right after auth-email
 * (login / register) and before the user lands in the main app tabs.
 *
 * Each desktop row shows:
 *  - Desktop name
 *  - Desktop ID (short)
 *  - "Paired at" relative timestamp
 *
 * Tapping a desktop sets it as the active transport and navigates into
 * the main app. The "+ Pair New Desktop" button launches the QR scanner
 * (pair-scan).
 *
 * Renders share two pieces with `app/settings/paired-desktops.tsx`:
 *   - `<PairedDesktopRow>` for the row markup
 *   - `useSyncPairedDesktops()` for the relay-fetch + local-merge.
 */

import { router } from 'expo-router';
import { useEffect } from 'react';
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
import {
  useRelayAccountHydrated,
  useRelayAccountStore,
} from '../../src/stores/relayAccountStore';
import { useTransportStore } from '../../src/stores/transportStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

export default function RelayDesktopsScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const desktops = usePairedDesktopsStore((s) => s.desktops);
  const removeDesktop = usePairedDesktopsStore((s) => s.removeDesktop);
  const accessToken = useRelayAccountStore((s) => s.accessToken);
  const email = useRelayAccountStore((s) => s.email);
  const logout = useRelayAccountStore((s) => s.logout);
  const storeHydrated = useRelayAccountHydrated();
  const setTransportKind = useTransportStore((s) => s.setTransportKind);
  const setActiveDesktopId = useTransportStore((s) => s.setActiveDesktopId);
  const activeDesktopId = useTransportStore((s) => s.activeDesktopId);

  const { refresh, initialLoading, isRefreshing } = useSyncPairedDesktops();

  useEffect(() => {
    // Wait for the persist middleware to rehydrate before deciding we're
    // unauthenticated — otherwise a cold-start deep-link into this screen
    // bounces signed-in users back to the login page for a frame.
    if (!storeHydrated) return;
    if (!accessToken) {
      router.replace('/(auth)/auth-email');
    }
  }, [storeHydrated, accessToken]);

  const handleSelectDesktop = (desktopId: string) => {
    setActiveDesktopId(desktopId);
    setTransportKind('relay');
    router.replace('/(tabs)/dashboard');
  };

  const handleLongPressDesktop = (desktopId: string, name: string) => {
    Alert.alert(
      t('desktops.unpairConfirmTitle', { name }),
      t('desktops.unpairConfirmMessage'),
      [
        { text: t('pairing.scan.cancelBtn'), style: 'cancel' },
        {
          text: t('desktops.unpairConfirmBtn'),
          style: 'destructive',
          onPress: () => removeDesktop(desktopId),
        },
      ],
    );
  };

  const handleLogout = async () => {
    await logout();
    router.replace('/(auth)/auth-email');
  };

  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
      {/* Header */}
      <View style={[styles.header, { borderBottomColor: colors.border.default }]}>
        <View style={styles.headerLeft}>
          <Text style={[styles.headerTitle, { color: colors.text.primary }]}>
            {t('desktops.headerTitle')}
          </Text>
          {email && (
            <Text style={[styles.headerSub, { color: colors.text.tertiary }]}>
              {email}
            </Text>
          )}
        </View>
        <Pressable onPress={handleLogout} style={styles.logoutBtn}>
          <Text style={[styles.logoutText, { color: colors.text.tertiary }]}>
            {t('desktops.logoutBtn')}
          </Text>
        </Pressable>
      </View>

      {/* Desktop list */}
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
            onPress={() => handleSelectDesktop(item.desktopId)}
            onLongPress={() => handleLongPressDesktop(item.desktopId, item.desktopName)}
          />
        )}
      />

      {/* Pair new button */}
      <View style={[styles.footer, { borderTopColor: colors.border.default }]}>
        <Pressable
          style={[styles.pairBtn, { backgroundColor: colors.brand.accent }]}
          onPress={() => router.push('/(auth)/pair-scan')}
        >
          <Text style={styles.pairBtnText}>{t('desktops.pairNewBtn')}</Text>
        </Pressable>
        <Pressable
          style={styles.manualLink}
          onPress={() => router.push('/(auth)/server')}
        >
          <Text style={[styles.manualLinkText, { color: colors.text.tertiary }]}>
            {t('desktops.manualLink')}
          </Text>
        </Pressable>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingTop: 60,
    paddingBottom: spacing.lg,
    paddingHorizontal: spacing.xl,
    borderBottomWidth: 1,
  },
  headerLeft: {
    flex: 1,
  },
  headerTitle: {
    ...typography.pageTitle,
  },
  headerSub: {
    ...typography.caption,
    marginTop: spacing.xs,
  },
  logoutBtn: {
    paddingVertical: spacing.sm,
    paddingLeft: spacing.xl,
  },
  logoutText: {
    ...typography.body,
  },
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
    gap: spacing.md,
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
  manualLink: {
    alignItems: 'center',
    paddingVertical: spacing.sm,
  },
  manualLinkText: {
    ...typography.body,
  },
});
