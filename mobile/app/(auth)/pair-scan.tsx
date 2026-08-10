/**
 * pair-scan.tsx — QR code scanner for the niuniu-pair:// pairing URL.
 *
 * Uses expo-camera's built-in barcode scanning (Expo SDK 53+; expo-barcode-scanner
 * has been deprecated in favour of expo-camera's BarCodeScanner).
 *
 * Flow:
 *  1. Request camera permission.
 *  2. Scan QR code containing a niuniu-pair://v1?... URL.
 *  3. Parse + validate the URL.
 *  4. Show confirmation prompt with relay origin.
 *  5. On confirm: ensure mobile identity, then navigate to /pair-waiting with
 *     all necessary params so the claim long-poll can run there.
 */

import { CameraView, useCameraPermissions } from 'expo-camera';
import { router } from 'expo-router';
import { useRef, useState } from 'react';
import {
  Alert,
  Modal,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Platform } from 'react-native';
import { useTranslation } from 'react-i18next';
import { getOrCreateIdentity } from '../../src/crypto/identity';
import { encode as b64Encode } from '@stablelib/base64';
import { parsePairingURL } from '../../src/utils/pairQR';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';
import { useRelayAccountStore } from '../../src/stores/relayAccountStore';
import { useTrustedRelaysStore } from '../../src/stores/trustedRelaysStore';

export default function PairScanScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const accessToken = useRelayAccountStore((s) => s.accessToken);
  const relayBaseUrl = useRelayAccountStore((s) => s.relayBaseUrl);

  const [permission, requestPermission] = useCameraPermissions();
  const [scanned, setScanned] = useState(false);
  const [confirmVisible, setConfirmVisible] = useState(false);
  const [untrustedWarningVisible, setUntrustedWarningVisible] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const pendingQR = useRef<ReturnType<typeof parsePairingURL>>(null);

  if (!permission) {
    // Permissions still loading
    return <View style={[styles.container, { backgroundColor: colors.bg.base }]} />;
  }

  if (!permission.granted) {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
        <Text style={[styles.permissionText, { color: colors.text.primary }]}>
          {t('pairing.scan.cameraPermission')}
        </Text>
        <Pressable
          style={[styles.button, { backgroundColor: colors.brand.accent }]}
          onPress={requestPermission}
        >
          <Text style={styles.buttonText}>{t('pairing.scan.grantCamera')}</Text>
        </Pressable>
        <Pressable
          style={[styles.button, styles.buttonOutline, { borderColor: colors.border.default }]}
          onPress={() => router.push('/(auth)/pair-manual')}
        >
          <Text style={[styles.buttonText, { color: colors.text.primary }]}>
            {t('pairing.scan.manualEntryButton')}
          </Text>
        </Pressable>
        <Pressable style={styles.backLink} onPress={() => router.back()}>
          <Text style={[styles.backLinkText, { color: colors.text.tertiary }]}>{t('pairing.scan.back')}</Text>
        </Pressable>
      </View>
    );
  }

  const handleBarCodeScanned = ({ data }: { data: string }) => {
    if (scanned) return;
    const qr = parsePairingURL(data);
    if (!qr) {
      // Not a pairing QR — ignore and keep scanning
      return;
    }

    // Check expiry
    const nowSec = Math.floor(Date.now() / 1000);
    if (qr.expiresAt < nowSec) {
      Alert.alert(t('pairing.scan.qrExpiredTitle'), t('pairing.scan.qrExpiredMessage'), [
        { text: t('pairing.scan.qrExpiredOk'), onPress: () => setScanned(false) },
      ]);
      setScanned(true);
      return;
    }

    setScanned(true);
    pendingQR.current = qr;

    // Check if the relay host is in the trusted allowlist
    const isTrusted = useTrustedRelaysStore.getState().isTrusted(qr.relayHost);
    if (!isTrusted) {
      setUntrustedWarningVisible(true);
    } else {
      setConfirmVisible(true);
    }
  };

  const handleConfirm = async () => {
    const qr = pendingQR.current;
    if (!qr) return;

    if (!accessToken || !relayBaseUrl) {
      Alert.alert(t('pairing.scan.notLoggedInTitle'), t('pairing.scan.notLoggedInMessage'), [
        {
          text: t('pairing.scan.notLoggedInAction'),
          onPress: () => router.replace('/(auth)/auth-email'),
        },
      ]);
      setConfirmVisible(false);
      return;
    }

    setConfirming(true);
    try {
      const identity = await getOrCreateIdentity();
      const mobileXpubB64 = b64Encode(identity.xPub);
      const mobileSignPubB64 = b64Encode(identity.edPub);
      const mobileName = `Mobile (${Platform.OS})`;

      setConfirmVisible(false);

      // Navigate to pair-waiting, passing all params needed for claim + SAS
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
      setConfirming(false);
    }
  };

  const handleCancel = () => {
    setConfirmVisible(false);
    setUntrustedWarningVisible(false);
    pendingQR.current = null;
    setScanned(false);
  };

  const handleTrustAndContinue = () => {
    const qr = pendingQR.current;
    if (!qr) return;
    useTrustedRelaysStore.getState().addOrigin(qr.relayHost);
    setUntrustedWarningVisible(false);
    setConfirmVisible(true);
  };

  return (
    <View style={styles.container}>
      <CameraView
        style={StyleSheet.absoluteFillObject}
        facing="back"
        barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
        onBarcodeScanned={scanned ? undefined : handleBarCodeScanned}
      />

      {/* Overlay */}
      <View style={styles.overlay}>
        <View style={styles.topBar}>
          <Pressable onPress={() => router.back()} style={styles.closeBtn}>
            <Text style={styles.closeBtnText}>{t('pairing.scan.cancel')}</Text>
          </Pressable>
        </View>

        <View style={styles.scanArea}>
          {/* Corner markers */}
          <View style={[styles.corner, styles.cornerTL]} />
          <View style={[styles.corner, styles.cornerTR]} />
          <View style={[styles.corner, styles.cornerBL]} />
          <View style={[styles.corner, styles.cornerBR]} />
        </View>

        <Text style={styles.hint}>{t('pairing.scan.hint')}</Text>
      </View>

      {/* Untrusted relay warning modal */}
      <Modal
        visible={untrustedWarningVisible}
        transparent
        animationType="slide"
        onRequestClose={handleCancel}
      >
        <View style={styles.modalBackdrop}>
          <View style={[styles.modalCard, { backgroundColor: colors.bg.surface }]}>
            <Text style={[styles.modalTitle, { color: colors.status.warning }]}>
              {t('trustedRelays.title')}
            </Text>
            {pendingQR.current && (
              <Text style={[styles.modalDesc, { color: colors.text.secondary }]}>
                {t('trustedRelays.untrustedWarning', { host: pendingQR.current.relayHost })}
              </Text>
            )}
            <View style={styles.modalActions}>
              <Pressable
                style={[styles.modalBtn, styles.modalBtnCancel, { borderColor: colors.border.default }]}
                onPress={handleCancel}
              >
                <Text style={[styles.modalBtnText, { color: colors.text.secondary }]}>{t('pairing.scan.cancelBtn')}</Text>
              </Pressable>
              <Pressable
                style={[styles.modalBtn, styles.modalBtnConfirm, { backgroundColor: colors.status.warning }]}
                onPress={handleTrustAndContinue}
              >
                <Text style={[styles.modalBtnText, { color: '#FFFFFF' }]}>
                  {t('trustedRelays.trustAndContinue')}
                </Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>

      {/* Confirm modal */}
      <Modal
        visible={confirmVisible}
        transparent
        animationType="slide"
        onRequestClose={handleCancel}
      >
        <View style={styles.modalBackdrop}>
          <View style={[styles.modalCard, { backgroundColor: colors.bg.surface }]}>
            <Text style={[styles.modalTitle, { color: colors.text.primary }]}>
              {t('pairing.scan.confirmTitle')}
            </Text>
            {pendingQR.current && (
              <>
                <Text style={[styles.modalRelayLabel, { color: colors.text.secondary }]}>
                  {t('pairing.scan.relayServiceLabel')}
                </Text>
                <Text style={[styles.modalRelayValue, { color: colors.brand.accent }]}>
                  {pendingQR.current.relayHost}
                </Text>
                <Text style={[styles.modalDesc, { color: colors.text.secondary }]}>
                  {t('pairing.scan.confirmOrigin', { desktopId: pendingQR.current.desktopId })}
                </Text>
              </>
            )}

            <View style={styles.modalActions}>
              <Pressable
                style={[styles.modalBtn, styles.modalBtnCancel, { borderColor: colors.border.default }]}
                onPress={handleCancel}
              >
                <Text style={[styles.modalBtnText, { color: colors.text.secondary }]}>{t('pairing.scan.cancelBtn')}</Text>
              </Pressable>
              <Pressable
                style={[
                  styles.modalBtn,
                  styles.modalBtnConfirm,
                  { backgroundColor: colors.brand.accent },
                  confirming && { opacity: 0.6 },
                ]}
                onPress={handleConfirm}
                disabled={confirming}
              >
                <Text style={[styles.modalBtnText, { color: '#FFFFFF' }]}>
                  {confirming ? t('pairing.scan.processingBtn') : t('pairing.scan.confirmBtn')}
                </Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>
    </View>
  );
}

const SCAN_BOX_SIZE = 240;
const CORNER_SIZE = 24;
const CORNER_THICKNESS = 3;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#000',
  },
  overlay: {
    ...StyleSheet.absoluteFillObject,
    alignItems: 'center',
  },
  topBar: {
    width: '100%',
    paddingTop: 56,
    paddingHorizontal: spacing.xl,
    alignItems: 'flex-start',
  },
  closeBtn: {
    paddingVertical: spacing.sm,
    paddingHorizontal: spacing.md,
  },
  closeBtnText: {
    color: '#FFFFFF',
    ...typography.bodyMedium,
  },
  scanArea: {
    marginTop: 80,
    width: SCAN_BOX_SIZE,
    height: SCAN_BOX_SIZE,
    position: 'relative',
  },
  corner: {
    position: 'absolute',
    width: CORNER_SIZE,
    height: CORNER_SIZE,
    borderColor: '#FFFFFF',
  },
  cornerTL: {
    top: 0,
    left: 0,
    borderTopWidth: CORNER_THICKNESS,
    borderLeftWidth: CORNER_THICKNESS,
    borderTopLeftRadius: 2,
  },
  cornerTR: {
    top: 0,
    right: 0,
    borderTopWidth: CORNER_THICKNESS,
    borderRightWidth: CORNER_THICKNESS,
    borderTopRightRadius: 2,
  },
  cornerBL: {
    bottom: 0,
    left: 0,
    borderBottomWidth: CORNER_THICKNESS,
    borderLeftWidth: CORNER_THICKNESS,
    borderBottomLeftRadius: 2,
  },
  cornerBR: {
    bottom: 0,
    right: 0,
    borderBottomWidth: CORNER_THICKNESS,
    borderRightWidth: CORNER_THICKNESS,
    borderBottomRightRadius: 2,
  },
  hint: {
    marginTop: spacing.xl,
    color: 'rgba(255,255,255,0.8)',
    ...typography.body,
    textAlign: 'center',
    paddingHorizontal: spacing['2xl'],
  },
  permissionText: {
    ...typography.body,
    textAlign: 'center',
    marginBottom: spacing.xl,
    paddingHorizontal: spacing['2xl'],
  },
  button: {
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
    marginHorizontal: spacing['2xl'],
    marginBottom: spacing.md,
  },
  buttonOutline: {
    backgroundColor: 'transparent',
    borderWidth: 1,
  },
  buttonText: {
    ...typography.bodyMedium,
    color: '#FFFFFF',
  },
  backLink: {
    alignItems: 'center',
    marginTop: spacing.xl,
  },
  backLinkText: {
    ...typography.body,
  },
  // Modal
  modalBackdrop: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.5)',
    justifyContent: 'flex-end',
  },
  modalCard: {
    borderTopLeftRadius: radius.lg,
    borderTopRightRadius: radius.lg,
    padding: spacing['2xl'],
    paddingBottom: 40,
  },
  modalTitle: {
    ...typography.sectionHead,
    marginBottom: spacing.xl,
  },
  modalRelayLabel: {
    ...typography.label,
    marginBottom: spacing.xs,
  },
  modalRelayValue: {
    ...typography.bodyMedium,
    marginBottom: spacing.lg,
  },
  modalDesc: {
    ...typography.body,
    marginBottom: spacing['2xl'],
  },
  modalActions: {
    flexDirection: 'row',
    gap: spacing.md,
  },
  modalBtn: {
    flex: 1,
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
  },
  modalBtnCancel: {
    borderWidth: 1,
  },
  modalBtnConfirm: {},
  modalBtnText: {
    ...typography.bodyMedium,
  },
});
