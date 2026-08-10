import { useCallback, useEffect, useState } from 'react';
import { usePairedDesktopsStore } from '../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../stores/relayAccountStore';

/**
 * Syncs the local pairedDesktopsStore with the relay's
 * /api/my/paired-desktops listing on mount, and exposes a `refresh`
 * imperative for pull-to-refresh.
 *
 * Extracted from `(auth)/relay-desktops.tsx:79-123` so the auth-flow
 * picker and the in-app settings page render from one source.
 *
 * Locally-known desktops absent from the relay listing are intentionally
 * preserved (not removed) because they may still be reachable over LAN
 * when the relay itself is unavailable.
 *
 * CRITICAL: the relay's listing response intentionally omits the
 * `relay_device_token` (it's issued write-once at pair time and stored
 * only on the mobile). If a desktop already exists locally with a real
 * token, we MUST NOT clobber it to ''. We therefore:
 *   - upsert a new row (with empty token) only when desktopId is unknown,
 *   - refresh metadata in place (preserving the stored token) otherwise.
 */
export function useSyncPairedDesktops(): {
  refresh: () => Promise<void>;
  initialLoading: boolean;
  isRefreshing: boolean;
} {
  const addDesktop = usePairedDesktopsStore((s) => s.addDesktop);
  const updateDesktop = usePairedDesktopsStore((s) => s.updateDesktop);
  const relayBaseUrl = useRelayAccountStore((s) => s.relayBaseUrl);

  const [initialLoading, setInitialLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);

  const refresh = useCallback(async () => {
    if (!relayBaseUrl) return;
    setIsRefreshing(true);
    try {
      // Use the store's getClient() — a bare `new RelayClient(relayBaseUrl)`
      // would fall back to SecureStore keys that are never written to
      // (login/register persist via zustand). The result would be an
      // unauthenticated client.
      const client = useRelayAccountStore.getState().getClient();
      if (!client) return;
      const remote = await client.listPairedDesktops();
      const existing = usePairedDesktopsStore.getState().desktops;
      const knownIds = new Set(existing.map((d) => d.desktopId));
      for (const d of remote) {
        if (knownIds.has(d.desktopId)) {
          // Refresh only name / keys / pairedAt. Never overwrite the token,
          // since the remote listing doesn't include it (would strand us on
          // LAN-only transport just by viewing the list).
          updateDesktop(d.desktopId, {
            desktopName: d.desktopName,
            xpub: d.xpub,
            signPub: d.signPub,
            pairedAt: d.pairedAt,
          });
        } else {
          addDesktop({
            desktopId: d.desktopId,
            desktopName: d.desktopName,
            xpub: d.xpub,
            signPub: d.signPub,
            // listPairedDesktops doesn't return the token (write-once at
            // pair time). Blank here means only LAN transport will work
            // for this entry until the user re-pairs from this device.
            relayDeviceToken: '',
            pairedAt: d.pairedAt,
          });
        }
      }
    } catch {
      // Non-fatal: fall back to whatever's in the local store.
    } finally {
      setIsRefreshing(false);
    }
  }, [relayBaseUrl, addDesktop, updateDesktop]);

  useEffect(() => {
    refresh().finally(() => setInitialLoading(false));
  }, [refresh]);

  return { refresh, initialLoading, isRefreshing };
}
