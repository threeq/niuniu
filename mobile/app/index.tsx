import { Redirect } from 'expo-router';
import { ActivityIndicator, View } from 'react-native';
import { useAuthStore } from '../src/stores/authStore';
import {
  usePairedDesktopsHydrated,
  usePairedDesktopsStore,
} from '../src/stores/pairedDesktopsStore';
import {
  useRelayAccountHydrated,
  useRelayAccountStore,
} from '../src/stores/relayAccountStore';
import { useServerStore } from '../src/stores/serverStore';
import {
  useTransportStore,
  useTransportStoreHydrated,
} from '../src/stores/transportStore';
import { decideInitialRoute } from '../src/utils/initialRoute';

export default function Index() {
  const serverLoading = useAuthStore((s) => s.isLoading);
  const isServerAuthed = useAuthStore((s) => s.isAuthenticated);
  const hasServer = useServerStore((s) => s.servers.length > 0);
  const activeServer = useServerStore((s) => s.getActiveServer());

  const relayAccessToken = useRelayAccountStore((s) => s.accessToken);
  const relayHydrated = useRelayAccountHydrated();

  // Paired desktops + transport choice drive the "skip the desktop picker"
  // branch of the design doc: a returning user with a still-valid persisted
  // choice lands directly in tabs.
  const desktops = usePairedDesktopsStore((s) => s.desktops);
  const desktopsHydrated = usePairedDesktopsHydrated();
  const activeDesktopId = useTransportStore((s) => s.activeDesktopId);
  const transportHydrated = useTransportStoreHydrated();

  const route = decideInitialRoute({
    allHydrated:
      !serverLoading && relayHydrated && desktopsHydrated && transportHydrated,
    relayAuthed: relayHydrated && !!relayAccessToken,
    activeDesktopId,
    // Pass desktop ids via a lazy iterator — decideInitialRoute only
    // iterates when relayAuthed, so we avoid allocating a Set/array
    // on the common not-authed path.
    pairedDesktopIds: desktops.map((d) => d.desktopId),
    hasServer,
    serverAuthDisabled: activeServer?.authEnabled === false,
    isServerAuthed,
  });

  if (route.kind === 'loading') {
    return (
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
        <ActivityIndicator size="large" color="#4F46E5" />
      </View>
    );
  }

  return <Redirect href={route.href} />;
}
