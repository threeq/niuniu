/**
 * connection-info.tsx — shows the current connection status and allows
 * the user to trigger a re-handshake.
 *
 * Shows:
 *  - Active desktop name
 *  - Transport kind: Direct LAN / Via <relay host> / Disconnected
 *  - Last handshake timestamp
 *  - End-to-end encryption note
 *  - "Re-handshake now" button (clears cipher; next RPC re-handshakes lazily)
 */

import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { useTranslation } from 'react-i18next';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTransportStore } from '../../src/stores/transportStore';
import { usePairedDesktopsStore } from '../../src/stores/pairedDesktopsStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

function formatHandshakeTime(ts: number | undefined): string {
  if (!ts) return '—';
  const d = new Date(ts);
  return d.toLocaleString();
}

export default function ConnectionInfoScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();

  const activeDesktopId = useTransportStore((s) => s.activeDesktopId);
  const activeTransportKind = useTransportStore((s) => s.activeTransportKind);
  const lastHandshakeAt = useTransportStore((s) => s.lastHandshakeAt);
  const removeCipher = useTransportStore((s) => s.removeCipher);
  const getCipher = useTransportStore((s) => s.getCipher);
  const desktops = usePairedDesktopsStore((s) => s.desktops);

  const activeDesktop = desktops.find((d) => d.desktopId === activeDesktopId);
  const hasCipher = activeDesktopId ? !!getCipher(activeDesktopId) : false;

  const transportLabel = () => {
    if (!activeDesktopId || !hasCipher) return t('connectionInfo.transport.disconnected');
    if (activeTransportKind === 'direct') return t('connectionInfo.transport.lan');
    // Extract relay host from the desktop's relay info
    return t('connectionInfo.transport.relay');
  };

  const handleRehandshake = () => {
    if (activeDesktopId) {
      removeCipher(activeDesktopId);
    }
  };

  const lastHandshake = activeDesktopId ? lastHandshakeAt[activeDesktopId] : undefined;

  return (
    <ScrollView
      style={[styles.screen, { backgroundColor: colors.bg.base }]}
      contentContainerStyle={[
        styles.content,
        { paddingTop: insets.top + spacing.xl, paddingBottom: insets.bottom + spacing['2xl'] },
      ]}
    >
      <Text style={[styles.pageTitle, { color: colors.text.primary }]}>
        {t('connectionInfo.title')}
      </Text>

      {/* Active desktop */}
      <View style={[styles.card, { backgroundColor: colors.bg.surface, borderColor: colors.border.default }]}>
        <Text style={[styles.fieldLabel, { color: colors.text.secondary }]}>
          {t('connectionInfo.activeDesktop')}
        </Text>
        <Text style={[styles.fieldValue, { color: colors.text.primary }]}>
          {activeDesktop?.desktopName ?? '—'}
        </Text>
      </View>

      {/* Transport kind */}
      <View style={[styles.card, { backgroundColor: colors.bg.surface, borderColor: colors.border.default }]}>
        <Text style={[styles.fieldLabel, { color: colors.text.secondary }]}>
          {t('connectionInfo.transport')}
        </Text>
        <Text
          style={[
            styles.fieldValue,
            {
              color: hasCipher ? colors.status.success : colors.text.tertiary,
            },
          ]}
        >
          {transportLabel()}
        </Text>
      </View>

      {/* Last handshake */}
      <View style={[styles.card, { backgroundColor: colors.bg.surface, borderColor: colors.border.default }]}>
        <Text style={[styles.fieldLabel, { color: colors.text.secondary }]}>
          {t('connectionInfo.lastHandshake')}
        </Text>
        <Text style={[styles.fieldValue, { color: colors.text.primary }]}>
          {formatHandshakeTime(lastHandshake)}
        </Text>
      </View>

      {/* Encryption note */}
      <View
        style={[
          styles.encryptionCard,
          { backgroundColor: colors.status.infoBg, borderColor: colors.brand.accentLight },
        ]}
      >
        <Text style={[styles.lockIcon, { color: colors.brand.accent }]}>{'🔒'}</Text>
        <Text style={[styles.encryptionText, { color: colors.brand.accent }]}>
          {t('connectionInfo.encryptedLine')}
        </Text>
      </View>

      {/* Re-handshake button */}
      <Pressable
        style={[
          styles.rehandshakeBtn,
          { borderColor: colors.border.default },
          !activeDesktopId && { opacity: 0.4 },
        ]}
        onPress={handleRehandshake}
        disabled={!activeDesktopId}
      >
        <Text style={[styles.rehandshakeBtnText, { color: colors.text.primary }]}>
          {t('connectionInfo.rehandshake')}
        </Text>
      </Pressable>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  content: {
    paddingHorizontal: spacing.xl,
    gap: spacing.md,
  },
  pageTitle: {
    ...typography.pageTitle,
    marginBottom: spacing.lg,
  },
  card: {
    borderRadius: radius.sm,
    borderWidth: 1,
    padding: spacing.lg,
  },
  fieldLabel: {
    ...typography.label,
    marginBottom: spacing.xs,
  },
  fieldValue: {
    ...typography.bodyMedium,
  },
  encryptionCard: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: spacing.sm,
    borderRadius: radius.sm,
    borderWidth: 1,
    padding: spacing.lg,
  },
  lockIcon: {
    fontSize: 16,
    lineHeight: 22,
  },
  encryptionText: {
    ...typography.body,
    flex: 1,
  },
  rehandshakeBtn: {
    borderRadius: radius.sm,
    borderWidth: 1,
    padding: spacing.lg,
    alignItems: 'center',
    marginTop: spacing.lg,
  },
  rehandshakeBtnText: {
    ...typography.bodyMedium,
  },
});
