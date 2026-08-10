// Boot-time router/dispatcher. No UI of its own beyond a centered spinner
// while it decides where to send the user. Decision tree:
//
//   no relay token         → /(auth)/auth-email
//   token + 0 local paired → /(auth)/pair-scan
//   token + 1 local paired → set active + /(tabs)/dashboard
//   token + ≥2 local paired→ /(auth)/relay-desktops
//
// Why local store and no network call: the natural per-device list is
// /api/my/paired-desktops, but that endpoint is DPoP-authed (relay
// router.go:247-248) — DPoP requires a mobile_device row, which a
// freshly-email-code-logged-in mobile doesn't yet have until pair-scan
// runs. Reading from the local SecureStore-persisted pairedDesktopsStore
// avoids the chicken-and-egg, also doubles as offline support.

import { router } from 'expo-router';
import { useEffect } from 'react';
import { ActivityIndicator, StyleSheet, View } from 'react-native';
import {
  usePairedDesktopsHydrated,
  usePairedDesktopsStore,
} from '../../src/stores/pairedDesktopsStore';
import {
  useRelayAccountHydrated,
  useRelayAccountStore,
} from '../../src/stores/relayAccountStore';
import { useTransportStore } from '../../src/stores/transportStore';
import { useThemeColors } from '../../src/theme/useTheme';

export default function ServerScreen() {
  const colors = useThemeColors();
  const relayHydrated = useRelayAccountHydrated();
  const desktopsHydrated = usePairedDesktopsHydrated();
  const accessToken = useRelayAccountStore((s) => s.accessToken);
  const desktops = usePairedDesktopsStore((s) => s.desktops);
  const setActiveDesktopId = useTransportStore((s) => s.setActiveDesktopId);
  const setTransportKind = useTransportStore((s) => s.setTransportKind);

  useEffect(() => {
    // Wait for BOTH SecureStore-backed stores to rehydrate before deciding.
    // Otherwise a returning multi-desktop user briefly looks like "0 desktops"
    // (defaults) and gets bounced to /(auth)/pair-scan instead of the desktop
    // list. Same risk for accessToken — looks unauthenticated for a frame.
    if (!relayHydrated || !desktopsHydrated) return;

    if (!accessToken) {
      router.replace('/(auth)/auth-email');
      return;
    }

    if (desktops.length === 0) {
      router.replace('/(auth)/pair-scan');
      return;
    }
    if (desktops.length === 1) {
      const only = desktops[0];
      setTransportKind('relay');
      setActiveDesktopId(only.desktopId);
      router.replace('/(tabs)/dashboard');
      return;
    }
    router.replace('/(auth)/relay-desktops');
  }, [relayHydrated, desktopsHydrated, accessToken, desktops, setActiveDesktopId, setTransportKind]);

  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base }]}>
      <ActivityIndicator size="large" color={colors.brand.accent} />
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
});
