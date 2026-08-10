/**
 * pair-manual.tsx — fallback screen for when camera is not available.
 *
 * The user pastes or types a niuniu-pair://v1?... URL into a textarea.
 * On confirm, the URL is parsed with parsePairingURL and the flow continues
 * to pair-waiting exactly as if the QR had been scanned.
 */

import { router } from 'expo-router';
import { useState } from 'react';
import {
  Alert,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { useTranslation } from 'react-i18next';
import { getOrCreateIdentity } from '../../src/crypto/identity';
import { encode as b64Encode } from '@stablelib/base64';
import { parsePairingURL } from '../../src/utils/pairQR';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';
import { useRelayAccountStore } from '../../src/stores/relayAccountStore';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

export default function PairManualScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const insets = useSafeAreaInsets();
  const accessToken = useRelayAccountStore((s) => s.accessToken);
  const relayBaseUrl = useRelayAccountStore((s) => s.relayBaseUrl);

  const [url, setUrl] = useState('');
  const [processing, setProcessing] = useState(false);

  const handleConfirm = async () => {
    const trimmed = url.trim();
    const qr = parsePairingURL(trimmed);
    if (!qr) {
      Alert.alert(t('pairing.manual.invalidUrl'));
      return;
    }

    // Check expiry
    const nowSec = Math.floor(Date.now() / 1000);
    if (qr.expiresAt < nowSec) {
      Alert.alert(t('pairing.scan.qrExpiredTitle'), t('pairing.scan.qrExpiredMessage'), [
        { text: t('pairing.scan.qrExpiredOk') },
      ]);
      return;
    }

    if (!accessToken || !relayBaseUrl) {
      Alert.alert(t('pairing.scan.notLoggedInTitle'), t('pairing.scan.notLoggedInMessage'), [
        {
          text: t('pairing.scan.notLoggedInAction'),
          onPress: () => router.replace('/(auth)/auth-email'),
        },
      ]);
      return;
    }

    setProcessing(true);
    try {
      const identity = await getOrCreateIdentity();
      const mobileXpubB64 = b64Encode(identity.xPub);
      const mobileSignPubB64 = b64Encode(identity.edPub);
      const mobileName = `Mobile (${Platform.OS})`;

      router.push({
        pathname: '/(auth)/pair-waiting',
        params: {
          relayHost: qr.relayHost,
          desktopId: qr.desktopId,
          sessionId: qr.sessionId,
          desktopXpubB64: qr.desktopXpubB64,
          desktopSignPubB64: qr.desktopSignPubB64,
          nonceB64: qr.nonceB64,
          expiresAt: String(qr.expiresAt),
          mobileXpubB64,
          mobileSignPubB64,
          mobileName,
        },
      });
    } catch (err: any) {
      Alert.alert(t('pairing.scan.prepareErrorTitle'), err.message);
      setProcessing(false);
    }
  };

  return (
    <KeyboardAvoidingView
      style={[styles.flex, { backgroundColor: colors.bg.base }]}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      <ScrollView
        contentContainerStyle={[
          styles.container,
          { paddingTop: insets.top + spacing.xl, paddingBottom: insets.bottom + spacing['2xl'] },
        ]}
        keyboardShouldPersistTaps="handled"
      >
        <Text style={[styles.title, { color: colors.text.primary }]}>
          {t('pairing.manual.title')}
        </Text>

        <Text style={[styles.label, { color: colors.text.secondary }]}>
          {t('pairing.manual.pasteLabel')}
        </Text>

        <TextInput
          style={[
            styles.textarea,
            {
              backgroundColor: colors.bg.surface,
              borderColor: colors.border.default,
              color: colors.text.primary,
            },
          ]}
          value={url}
          onChangeText={setUrl}
          multiline
          numberOfLines={5}
          autoCapitalize="none"
          autoCorrect={false}
          spellCheck={false}
          placeholder="niuniu-pair://v1?r=..."
          placeholderTextColor={colors.text.tertiary}
          textAlignVertical="top"
        />

        <Pressable
          style={[
            styles.confirmBtn,
            { backgroundColor: colors.brand.accent },
            (processing || !url.trim()) && { opacity: 0.6 },
          ]}
          onPress={handleConfirm}
          disabled={processing || !url.trim()}
        >
          <Text style={styles.confirmBtnText}>
            {processing ? t('pairing.scan.processingBtn') : t('pairing.manual.confirm')}
          </Text>
        </Pressable>

        <Pressable style={styles.backLink} onPress={() => router.back()}>
          <Text style={[styles.backLinkText, { color: colors.text.tertiary }]}>
            {t('pairing.scan.back')}
          </Text>
        </Pressable>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  container: {
    paddingHorizontal: spacing.xl,
    flexGrow: 1,
  },
  title: {
    ...typography.pageTitle,
    marginBottom: spacing.xl,
  },
  label: {
    ...typography.label,
    marginBottom: spacing.sm,
  },
  textarea: {
    borderWidth: 1,
    borderRadius: radius.sm,
    padding: spacing.md,
    minHeight: 120,
    ...typography.body,
    marginBottom: spacing.xl,
  },
  confirmBtn: {
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
    marginBottom: spacing.lg,
  },
  confirmBtnText: {
    ...typography.bodyMedium,
    color: '#FFFFFF',
  },
  backLink: {
    alignItems: 'center',
    paddingVertical: spacing.sm,
  },
  backLinkText: {
    ...typography.body,
  },
});
