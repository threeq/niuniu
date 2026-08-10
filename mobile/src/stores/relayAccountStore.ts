import { useEffect, useState } from 'react';
import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';
import * as SecureStore from 'expo-secure-store';
import {
  RelayClient,
  requestLoginCode as requestLoginCodeApi,
  verifyLoginCode,
  type RelayTokenStore,
} from '../api/relay/client';
import { usePairedDesktopsStore } from './pairedDesktopsStore';
import { useTransportStore } from './transportStore';

// ─── Secure-store backed JSON storage for Zustand persist ───────────────────

// SecureStore on Android requires keys to match ^[\w.-]+$ — no colons.
const RELAY_ACCOUNT_KEY = 'niuniu.relay-account';

const secureStorage = {
  getItem: async (name: string): Promise<string | null> => {
    return SecureStore.getItemAsync(name);
  },
  setItem: async (name: string, value: string): Promise<void> => {
    await SecureStore.setItemAsync(name, value);
  },
  removeItem: async (name: string): Promise<void> => {
    await SecureStore.deleteItemAsync(name);
  },
};

// ─── Types ───────────────────────────────────────────────────────────────────

interface RelayAccountState {
  email: string | null;
  accessToken: string | null;
  refreshToken: string | null;
  relayBaseUrl: string | null;

  // Actions — email-code passwordless flow.
  // `requestLoginCode` triggers the relay to send a 6-digit code to `email`
  // and returns the resend cooldown so the UI can disable the "resend"
  // button. `loginWithCode` calls verify and writes
  // {accessToken, refreshToken, email, relayBaseUrl} to the store on
  // success — the email is the relay's canonical normalized form (it may
  // differ from input by trim/lowercase), so other UI reading
  // `state.email` always sees the value the server keyed the account on.
  loginWithCode: (relayBaseUrl: string, email: string, code: string) => Promise<void>;
  requestLoginCode: (relayBaseUrl: string, email: string) => Promise<{ resendAfter: number }>;
  setRelayBaseUrl: (url: string) => void;
  logout: () => Promise<void>;
  setTokens: (accessToken: string, refreshToken: string) => void;
  clearTokens: () => void;

  // Computed
  isAuthenticated: () => boolean;
  getClient: () => RelayClient | null;
}

// ─── Token store adapter ─────────────────────────────────────────────────

function createRelayTokenStore(): RelayTokenStore {
  return {
    getAccessToken: () => useRelayAccountStore.getState().accessToken,
    getRefreshToken: () => useRelayAccountStore.getState().refreshToken,
    setTokens: (pair) => {
      useRelayAccountStore.getState().setTokens(pair.access_token, pair.refresh_token);
    },
    clearTokens: () => {
      useRelayAccountStore.getState().clearTokens();
    },
  };
}

// ─── Store ───────────────────────────────────────────────────────────────────

export const useRelayAccountStore = create<RelayAccountState>()(
  persist(
    (set, get) => ({
      email: null,
      accessToken: null,
      refreshToken: null,
      relayBaseUrl: null,

      loginWithCode: async (relayBaseUrl, email, code) => {
        const resp = await verifyLoginCode(relayBaseUrl, email, code);
        // Explicitly write resp.email — the relay normalizes the address
        // (trim + lowercase) so we always store the server-canonical
        // form, which is what other UI gates on `state.email`.
        set({
          email: resp.email,
          accessToken: resp.access_token,
          refreshToken: resp.refresh_token,
          relayBaseUrl,
        });
      },

      requestLoginCode: async (relayBaseUrl, email) => {
        const resp = await requestLoginCodeApi(relayBaseUrl, email);
        return { resendAfter: resp.resend_after };
      },

      setRelayBaseUrl: (url) => set({ relayBaseUrl: url }),

      logout: async () => {
        const { relayBaseUrl, accessToken } = get();
        if (relayBaseUrl && accessToken) {
          const client = new RelayClient(relayBaseUrl, createRelayTokenStore());
          // client.logout() has `finally { this.clearTokens() }` which
          // routes through the injected adapter and nulls accessToken /
          // refreshToken in this store before returning.
          await client.logout().catch(() => {/* ignore network errors on logout */});
        }
        // Defensive: explicitly clear all three fields including email.
        // Token nulling above is a side-effect of client.logout()'s finally
        // block routed through the adapter; keeping the explicit clear here
        // means a future change to the client (e.g. moving the clear out
        // of the finally) doesn't silently leave account tokens populated.
        set({ email: null, accessToken: null, refreshToken: null });
        // Clear the persisted transport choice AND tear down live Noise
        // ciphers + LAN tunnel handles. Backgrounding alone would also do
        // the teardown (registerForegroundHandler in foreground.ts), but
        // a logout-then-login flow that never backgrounds would otherwise
        // keep the prior account's CipherPair / LanTunnelHandle objects
        // alive in memory, keyed by the prior account's desktopIds — so
        // we tear them down eagerly here.
        const transport = useTransportStore.getState();
        transport.resetActiveTransport();
        transport.clearAllCiphers();
        // Drop the paired-desktops list. The entries are scoped to the
        // account that paired them (relay_device_token is account-bound),
        // so leaving them around would let a different account on the
        // same device tap a row they have no key material for and land
        // in tabs with a dead tunnel. On next login the relay re-fetches
        // the authoritative list (relay-desktops.tsx fetchFromRelay).
        // Tradeoff: cached LAN endpoints (lastKnownLanEndpoints) are
        // also dropped; LAN transport will re-discover via mDNS on next
        // connect. Acceptable for correctness.
        usePairedDesktopsStore.getState().clearAll();
      },

      setTokens: (accessToken, refreshToken) => {
        set({ accessToken, refreshToken });
      },

      clearTokens: () => {
        set({ accessToken: null, refreshToken: null });
      },

      isAuthenticated: () => {
        return !!get().accessToken;
      },

      getClient: () => {
        const { relayBaseUrl } = get();
        if (!relayBaseUrl) return null;
        return new RelayClient(relayBaseUrl, createRelayTokenStore());
      },
    }),
    {
      name: RELAY_ACCOUNT_KEY,
      storage: createJSONStorage(() => secureStorage),
      // Persist email, tokens, and relay base URL — zustand persist is the
      // sole token storage (RelayClient delegates to this store via
      // RelayTokenStore adapter).
      partialize: (state) => ({
        email: state.email,
        accessToken: state.accessToken,
        refreshToken: state.refreshToken,
        relayBaseUrl: state.relayBaseUrl,
      }),
    },
  ),
);

// ─── Hydration hook ─────────────────────────────────────────────────────

/**
 * Returns true once the SecureStore-backed persist middleware has finished
 * rehydrating. Components that gate on `accessToken` for auth decisions must
 * wait for this, otherwise a cold-start deep-link will briefly see a null
 * token and bounce signed-in users to the login screen.
 */
export function useRelayAccountHydrated(): boolean {
  const [hydrated, setHydrated] = useState<boolean>(() =>
    useRelayAccountStore.persist.hasHydrated(),
  );
  useEffect(() => {
    if (hydrated) return;
    const unsub = useRelayAccountStore.persist.onFinishHydration(() =>
      setHydrated(true),
    );
    return unsub;
  }, [hydrated]);
  return hydrated;
}
