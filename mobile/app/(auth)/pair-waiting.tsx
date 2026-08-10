/**
 * pair-waiting.tsx — waits for the desktop to confirm the pairing session,
 * then presents a 3-choice SAS picker to guard against MITM.
 *
 * Flow implemented here:
 *  1. On mount, call pairing.claim() with a 5-minute timeout.
 *  2. While awaiting, show spinner.
 *  3. On claim confirmed: verify echoed desktop pub matches QR (anti-swap),
 *     persist to pairedDesktopsStore, compute SAS with deriveSAS, generate
 *     3-choice picker.
 *  4. Present the three choices; user taps the one matching the desktop.
 *  5. Correct → navigate to relay-desktops.
 *     Wrong   → show error and navigate back.
 */

import { router, useLocalSearchParams } from 'expo-router';
import { useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Platform } from 'react-native';
import { useTranslation } from 'react-i18next';
import { decode as b64Decode } from '@stablelib/base64';
import { claim, type ClaimResponse } from '../../src/api/relay/pairing';
import { deriveSAS, threeChoicePicker } from '../../src/crypto/sas';
import { getOrCreateIdentity } from '../../src/crypto/identity';
import { useRelayAccountStore } from '../../src/stores/relayAccountStore';
import { usePairedDesktopsStore } from '../../src/stores/pairedDesktopsStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

type Stage = 'waiting' | 'sas' | 'sas-pick' | 'success' | 'error';

export default function PairWaitingScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const params = useLocalSearchParams<{
    relayHost: string;
    desktopId: string;
    sessionId: string;
    desktopXpubB64: string;
    desktopSignPubB64: string;
    nonceB64: string;
    expiresAt: string;
    mobileXpubB64: string;
    mobileSignPubB64: string;
    mobileName: string;
  }>();

  const accessToken = useRelayAccountStore((s) => s.accessToken);

  const [stage, setStage] = useState<Stage>('waiting');
  const [errorMsg, setErrorMsg] = useState('');
  const [choices, setChoices] = useState<string[]>([]);
  const [correctIndex, setCorrectIndex] = useState(-1);
  const claimDone = useRef(false);

  useEffect(() => {
    if (claimDone.current) return;
    claimDone.current = true;

    const run = async () => {
      if (!accessToken) {
        setErrorMsg(t('pairing.waiting.notLoggedIn'));
        setStage('error');
        return;
      }

      try {
        const identity = await getOrCreateIdentity();

        const claimResp: ClaimResponse = await claim(
          params.relayHost,
          accessToken,
          {
            sessionId: params.sessionId,
            mobileXpub: params.mobileXpubB64,
            mobileSignPub: params.mobileSignPubB64,
            mobileName: params.mobileName ?? `Mobile (${Platform.OS})`,
            platform: Platform.OS,
            pairingNonceB64: params.nonceB64,
          },
        );

        if (claimResp.status !== 'confirmed') {
          throw new Error(t('pairing.waiting.pairingStatus', { status: claimResp.status }));
        }

        // Anti-swap check: verify the echoed desktop pub matches what was in the QR code.
        if (claimResp.desktop_xpub_b64 !== params.desktopXpubB64) {
          setErrorMsg(t('pairing.waiting.pubKeyMismatch'));
          setStage('error');
          return;
        }

        // Persist to store. pair_key_b64 may be empty on relays that
        // predate the 008 migration; that's fine — apiClient falls back
        // to noise / plaintext when pairKey is missing.
        usePairedDesktopsStore.getState().addDesktop({
          desktopId: claimResp.desktop_id!,
          desktopName: claimResp.desktop_name ?? claimResp.desktop_id!,
          xpub: claimResp.desktop_xpub_b64!,
          signPub: claimResp.desktop_sign_pub_b64!,
          relayDeviceToken: claimResp.relay_device_token!,
          mobileId: claimResp.mobile_id,
          pairKey: claimResp.pair_key_b64 || undefined,
          pairedAt: new Date().toISOString(),
        });

        // If the server detected a reinstall conflict, navigate to the
        // replace/keep resolution screen before finishing.
        if (claimResp.conflict && claimResp.prior_mobile_id) {
          router.replace({
            pathname: '/(auth)/pair-conflict',
            params: {
              relayHost: params.relayHost,
              sessionId: params.sessionId,
              priorMobileId: claimResp.prior_mobile_id,
              priorMobileName: claimResp.prior_mobile_name ?? '',
            },
          });
          return;
        }

        // Compute SAS.
        const nonce = b64Decode(params.nonceB64);
        const desktopXpub = b64Decode(claimResp.desktop_xpub_b64!);
        const mobileXpub = identity.xPub;
        const desktopSignPub = b64Decode(claimResp.desktop_sign_pub_b64!);
        const mobileSignPub = identity.edPub;

        const sas = deriveSAS(nonce, desktopXpub, mobileXpub, desktopSignPub, mobileSignPub);
        const { choices: c, correctIndex: ci } = threeChoicePicker(sas);
        setChoices(c);
        setCorrectIndex(ci);
        setStage('sas');
      } catch (err: any) {
        setErrorMsg(err.message || t('pairing.waiting.pairingFailed'));
        setStage('error');
      }
    };

    run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleChoiceTap = (idx: number) => {
    if (idx === correctIndex) {
      // Correct SAS — desktop is verified; pairing already persisted above.
      setStage('success');
    } else {
      Alert.alert(
        t('pairing.waiting.sasMismatchTitle'),
        t('pairing.waiting.sasMismatchMessage'),
        [
          {
            text: t('pairing.waiting.sasMismatchBack'),
            onPress: () => router.replace('/(auth)/relay-desktops'),
          },
        ],
      );
    }
  };

  if (stage === 'waiting') {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
        <ActivityIndicator size="large" color={colors.brand.accent} style={styles.spinner} />
        <Text style={[styles.waitingTitle, { color: colors.text.primary }]}>
          {t('pairing.waiting.waitingTitle')}
        </Text>
        <Text style={[styles.waitingHint, { color: colors.text.secondary }]}>
          {t('pairing.waiting.waitingHint')}
        </Text>
        <Pressable style={styles.cancelLink} onPress={() => router.back()}>
          <Text style={[styles.cancelLinkText, { color: colors.text.tertiary }]}>{t('pairing.waiting.cancelLink')}</Text>
        </Pressable>
      </View>
    );
  }

  if (stage === 'sas') {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
        <Text style={[styles.sasTitle, { color: colors.text.primary }]}>
          {t('pairing.waiting.sasTitle')}
        </Text>
        <Text style={[styles.sasHint, { color: colors.text.secondary }]}>
          {t('pairing.waiting.sasHint')}
        </Text>

        <View style={styles.choicesContainer}>
          {choices.map((choice, idx) => (
            <Pressable
              key={choice}
              style={({ pressed }) => [
                styles.choiceBtn,
                {
                  backgroundColor: pressed ? colors.brand.accentLight : colors.bg.surface,
                  borderColor: colors.border.default,
                },
              ]}
              onPress={() => handleChoiceTap(idx)}
            >
              <Text style={[styles.choiceCode, { color: colors.text.primary }]}>
                {choice}
              </Text>
            </Pressable>
          ))}
        </View>

        <Text style={[styles.sasWarning, { color: colors.text.tertiary }]}>
          {t('pairing.waiting.sasWarning')}
        </Text>

        <Pressable style={styles.cancelLink} onPress={() => router.back()}>
          <Text style={[styles.cancelLinkText, { color: colors.text.tertiary }]}>{t('pairing.waiting.cancelPairing')}</Text>
        </Pressable>
      </View>
    );
  }

  if (stage === 'success') {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
        <View style={[styles.iconCircle, { backgroundColor: colors.status.successBg }]}>
          <Text style={styles.iconEmoji}>✓</Text>
        </View>
        <Text style={[styles.successTitle, { color: colors.text.primary }]}>
          {t('pairing.waiting.successTitle')}
        </Text>
        <Text style={[styles.successDesc, { color: colors.text.secondary }]}>
          {t('pairing.waiting.successDesc')}
        </Text>
        <Pressable
          style={[styles.button, { backgroundColor: colors.brand.accent, marginTop: spacing['2xl'] }]}
          onPress={() => router.replace('/(auth)/relay-desktops')}
        >
          <Text style={styles.buttonText}>{t('pairing.waiting.viewPairedBtn')}</Text>
        </Pressable>
      </View>
    );
  }

  // error stage
  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
      <View style={[styles.iconCircle, { backgroundColor: colors.status.errorBg }]}>
        <Text style={styles.iconEmoji}>✗</Text>
      </View>
      <Text style={[styles.errorTitle, { color: colors.text.primary }]}>
        {t('pairing.waiting.errorTitle')}
      </Text>
      <Text style={[styles.errorDesc, { color: colors.status.error }]}>
        {errorMsg}
      </Text>
      <Pressable
        style={[styles.button, { backgroundColor: colors.brand.accent, marginTop: spacing['2xl'] }]}
        onPress={() => router.replace('/(auth)/relay-desktops')}
      >
        <Text style={styles.buttonText}>{t('pairing.waiting.backBtn')}</Text>
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
  spinner: {
    marginBottom: spacing.xl,
  },
  waitingTitle: {
    ...typography.sectionHead,
    marginBottom: spacing.md,
    textAlign: 'center',
  },
  waitingHint: {
    ...typography.body,
    textAlign: 'center',
    paddingHorizontal: spacing.lg,
  },
  cancelLink: {
    marginTop: spacing['2xl'],
    paddingVertical: spacing.sm,
  },
  cancelLinkText: {
    ...typography.body,
  },
  // SAS
  sasTitle: {
    ...typography.pageTitle,
    textAlign: 'center',
    marginBottom: spacing.lg,
  },
  sasHint: {
    ...typography.body,
    textAlign: 'center',
    marginBottom: spacing['2xl'],
    paddingHorizontal: spacing.sm,
  },
  choicesContainer: {
    width: '100%',
    gap: spacing.md,
    marginBottom: spacing.xl,
  },
  choiceBtn: {
    borderRadius: radius.md,
    borderWidth: 1,
    padding: spacing.xl,
    alignItems: 'center',
  },
  choiceCode: {
    fontSize: 32,
    fontWeight: '700',
    letterSpacing: 8,
  },
  sasWarning: {
    ...typography.caption,
    textAlign: 'center',
    paddingHorizontal: spacing.lg,
  },
  // Success / error
  iconCircle: {
    width: 72,
    height: 72,
    borderRadius: 36,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: spacing.xl,
  },
  iconEmoji: {
    fontSize: 32,
    fontWeight: '700',
    color: '#16A34A',
  },
  successTitle: {
    ...typography.pageTitle,
    marginBottom: spacing.md,
    textAlign: 'center',
  },
  successDesc: {
    ...typography.body,
    textAlign: 'center',
    marginBottom: spacing.xl,
  },
  errorTitle: {
    ...typography.pageTitle,
    marginBottom: spacing.md,
    textAlign: 'center',
  },
  errorDesc: {
    ...typography.body,
    textAlign: 'center',
    marginBottom: spacing.xl,
  },
  button: {
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
    width: '100%',
  },
  buttonText: {
    ...typography.bodyMedium,
    color: '#FFFFFF',
  },
});
