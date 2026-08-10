/**
 * pair-conflict.tsx — "Replace / Keep" prompt shown when a reinstalled phone
 * triggers a mobile device conflict during pairing.
 *
 * Navigated to from pair-waiting when the claim response includes
 * conflict: true.
 */

import { router, useLocalSearchParams } from 'expo-router';
import { useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useTranslation } from 'react-i18next';
import { resolveConflict } from '../../src/api/relay/pairing';
import { useRelayAccountStore } from '../../src/stores/relayAccountStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

export default function PairConflictScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();

  const params = useLocalSearchParams<{
    relayHost: string;
    sessionId: string;
    priorMobileId: string;
    priorMobileName: string;
  }>();

  const accessToken = useRelayAccountStore((s) => s.accessToken);
  const relayBaseUrl = useRelayAccountStore((s) => s.relayBaseUrl);

  const [loading, setLoading] = useState(false);

  const handleReplace = async () => {
    if (!accessToken || !relayBaseUrl) return;
    setLoading(true);
    try {
      await resolveConflict(
        `https://${params.relayHost}`,
        accessToken,
        params.sessionId,
        params.priorMobileId,
      );
      router.replace('/(auth)/relay-desktops');
    } catch (err: any) {
      Alert.alert(t('pairing.scan.prepareErrorTitle'), err.message);
      setLoading(false);
    }
  };

  const handleKeep = () => {
    // Proceed as a new device — no server call needed.
    router.replace('/(auth)/relay-desktops');
  };

  if (loading) {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
        <ActivityIndicator size="large" color={colors.brand.accent} />
      </View>
    );
  }

  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
      <View style={[styles.iconCircle, { backgroundColor: colors.status.warningBg }]}>
        <Text style={styles.iconText}>{'?'}</Text>
      </View>

      <Text style={[styles.title, { color: colors.text.primary }]}>
        {t('pairing.conflict.title')}
      </Text>

      <Text style={[styles.desc, { color: colors.text.secondary }]}>
        {t('pairing.conflict.description', { priorName: params.priorMobileName })}
      </Text>

      <Pressable
        style={[styles.btn, { backgroundColor: colors.brand.accent }]}
        onPress={handleReplace}
      >
        <Text style={styles.btnTextLight}>{t('pairing.conflict.replacePrevious')}</Text>
      </Pressable>

      <Pressable
        style={[styles.btn, styles.btnOutline, { borderColor: colors.border.default }]}
        onPress={handleKeep}
      >
        <Text style={[styles.btnTextDark, { color: colors.text.primary }]}>
          {t('pairing.conflict.keepAsNew')}
        </Text>
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    padding: spacing['2xl'],
  },
  iconCircle: {
    width: 72,
    height: 72,
    borderRadius: 36,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: spacing.xl,
  },
  iconText: {
    fontSize: 32,
    fontWeight: '700',
    color: '#CA8A04',
  },
  title: {
    ...typography.pageTitle,
    textAlign: 'center',
    marginBottom: spacing.md,
  },
  desc: {
    ...typography.body,
    textAlign: 'center',
    marginBottom: spacing['2xl'],
    paddingHorizontal: spacing.md,
  },
  btn: {
    width: '100%',
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
    marginBottom: spacing.md,
  },
  btnOutline: {
    backgroundColor: 'transparent',
    borderWidth: 1,
  },
  btnTextLight: {
    ...typography.bodyMedium,
    color: '#FFFFFF',
  },
  btnTextDark: {
    ...typography.bodyMedium,
  },
});
