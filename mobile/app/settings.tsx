import React from 'react';
import { View, Text, Pressable, ScrollView, StyleSheet } from 'react-native';
import { useRouter } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useThemeColors, useThemeStore } from '../src/theme/useTheme';
import { typography, spacing, radius } from '../src/theme/tokens';
import { useAuthStore } from '../src/stores/authStore';
import { useRelayAccountStore } from '../src/stores/relayAccountStore';
import { useServerStore } from '../src/stores/serverStore';
import {
  usePairedDesktopsStore,
  usePairedDesktopsHydrated,
} from '../src/stores/pairedDesktopsStore';
import { SectionHeader } from '../src/components/SectionHeader';
import { SegmentedControl } from '../src/components/SegmentedControl';
import Constants from 'expo-constants';

export default function SettingsScreen() {
  const colors = useThemeColors();
  const router = useRouter();
  const insets = useSafeAreaInsets();
  const clearTokens = useAuthStore((s) => s.clearTokens);
  const activeServer = useServerStore((s) => s.getActiveServer());
  const { preference, setPreference } = useThemeStore();

  const desktopCount = usePairedDesktopsStore((s) => s.desktops.length);
  const desktopsHydrated = usePairedDesktopsHydrated();
  const desktopCountLabel = !desktopsHydrated
    ? '—'
    : desktopCount === 0
      ? 'None'
      : String(desktopCount);

  const themeOptions = ['System', 'Light', 'Dark'];
  const themeIndex = preference === 'system' ? 0 : preference === 'light' ? 1 : 2;

  const handleSignOut = async () => {
    // Clear relay session FIRST so the dispatcher (server.tsx) doesn't see a
    // lingering accessToken on next launch and skip /(auth)/auth-email. This
    // also clears pairedDesktopsStore via relayAccountStore.logout().
    await useRelayAccountStore.getState().logout();
    await clearTokens();
    router.replace('/(auth)/auth-email');
  };

  const rowStyle = [styles.row, { backgroundColor: colors.bg.base, borderColor: colors.border.default }];

  return (
    <View style={[styles.screen, { backgroundColor: colors.bg.surface }]}>
      <ScrollView contentContainerStyle={[styles.content, { paddingTop: insets.top + spacing.lg }]}>
        {/* Account */}
        <View style={styles.section}>
          <SectionHeader title="ACCOUNT" />
          <View style={[styles.group, { borderColor: colors.border.default }]}>
            <View style={rowStyle}>
              <Text style={[styles.label, { color: colors.text.secondary }]}>Username</Text>
              <Text style={[styles.value, { color: colors.text.primary }]}>user</Text>
            </View>
            <Pressable style={rowStyle} onPress={handleSignOut}>
              <Text style={[styles.label, { color: colors.status.error }]}>Sign Out</Text>
            </Pressable>
          </View>
        </View>

        {/* Server */}
        <View style={styles.section}>
          <SectionHeader title="SERVER" />
          <View style={[styles.group, { borderColor: colors.border.default }]}>
            <View style={rowStyle}>
              <Text style={[styles.label, { color: colors.text.secondary }]}>URL</Text>
              <Text style={[styles.value, { color: colors.text.primary }]} numberOfLines={1}>
                {activeServer ? `${activeServer.scheme}://${activeServer.host}:${activeServer.port}` : 'Not connected'}
              </Text>
            </View>
            <View style={rowStyle}>
              <Text style={[styles.label, { color: colors.text.secondary }]}>Status</Text>
              <View style={[styles.statusDot, { backgroundColor: colors.status.success }]} />
            </View>
          </View>
        </View>

        {/* Desktops */}
        <View style={styles.section}>
          <SectionHeader title="DESKTOPS" />
          <View style={[styles.group, { borderColor: colors.border.default }]}>
            <Pressable
              style={rowStyle}
              onPress={() => router.push('/settings/paired-desktops')}
              accessibilityRole="button"
              accessibilityLabel="Paired Desktops"
            >
              <Text style={[styles.label, { color: colors.text.secondary }]}>
                Paired Desktops
              </Text>
              <View style={styles.metaRow}>
                <Text style={[styles.value, { color: colors.text.tertiary }]}>
                  {desktopCountLabel}
                </Text>
                <Text style={[styles.chevron, { color: colors.text.tertiary }]}>›</Text>
              </View>
            </Pressable>
          </View>
        </View>

        {/* Appearance */}
        <View style={styles.section}>
          <SectionHeader title="APPEARANCE" />
          <View style={[styles.group, { borderColor: colors.border.default }]}>
            <View style={[rowStyle, { flexDirection: 'column', alignItems: 'stretch', gap: spacing.sm }]}>
              <Text style={[styles.label, { color: colors.text.secondary }]}>Theme</Text>
              <SegmentedControl
                options={themeOptions}
                selected={themeIndex}
                onSelect={(i) => setPreference((['system', 'light', 'dark'] as const)[i])}
              />
            </View>
          </View>
        </View>

        {/* About */}
        <View style={styles.section}>
          <SectionHeader title="ABOUT" />
          <View style={[styles.group, { borderColor: colors.border.default }]}>
            <View style={rowStyle}>
              <Text style={[styles.label, { color: colors.text.secondary }]}>Version</Text>
              <Text style={[styles.value, { color: colors.text.tertiary }]}>
                {Constants.expoConfig?.version ?? '1.0.0'}
              </Text>
            </View>
            {/* Full git describe written by scripts/sync-version.mjs at build
                time. Hidden when the binary was built without that step
                (extra.niuniuBuildId stays undefined) so a stock dev build
                doesn't show a confusing empty row. */}
            {Constants.expoConfig?.extra?.niuniuBuildId ? (
              <View style={rowStyle}>
                <Text style={[styles.label, { color: colors.text.secondary }]}>Build</Text>
                <Text
                  style={[styles.value, { color: colors.text.tertiary }]}
                  numberOfLines={1}
                >
                  {String(Constants.expoConfig.extra.niuniuBuildId)}
                </Text>
              </View>
            ) : null}
          </View>
        </View>
      </ScrollView>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  content: { paddingBottom: spacing['2xl'] },
  section: { paddingHorizontal: spacing.lg, marginBottom: spacing.xl },
  group: { borderWidth: 1, borderRadius: radius.md, overflow: 'hidden' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: 14,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  label: { ...typography.label },
  value: { ...typography.body },
  statusDot: { width: 8, height: 8, borderRadius: 4 },
  metaRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
  chevron: {
    fontSize: 20,
    lineHeight: 22,
  },
});
