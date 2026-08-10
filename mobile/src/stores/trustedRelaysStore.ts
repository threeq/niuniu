/**
 * trustedRelaysStore — persisted allowlist of relay origins the user trusts.
 *
 * Defaults to ["relay.niuniu.dev"]. When a QR code references a relay host
 * not in this list, the pairing flow shows a warning sheet before proceeding.
 */

import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';
import * as SecureStore from 'expo-secure-store';

// SecureStore on Android requires keys to match ^[\w.-]+$ — no colons.
const TRUSTED_RELAYS_KEY = 'niuniu.trusted-relays';

const DEFAULT_ORIGINS = ['niuniu-relay.niu6ai.com'];

// ─── Secure-store backed JSON storage ────────────────────────────────────────

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

interface TrustedRelaysState {
  /** List of trusted relay origin hostnames, e.g. ["relay.niuniu.dev"] */
  origins: string[];
  addOrigin: (host: string) => void;
  removeOrigin: (host: string) => void;
  isTrusted: (host: string) => boolean;
}

// ─── Store ───────────────────────────────────────────────────────────────────

export const useTrustedRelaysStore = create<TrustedRelaysState>()(
  persist(
    (set, get) => ({
      origins: [...DEFAULT_ORIGINS],

      addOrigin: (host) => {
        const trimmed = host.trim().toLowerCase();
        if (!trimmed) return;
        set((state) => {
          if (state.origins.includes(trimmed)) return state;
          return { origins: [...state.origins, trimmed] };
        });
      },

      removeOrigin: (host) => {
        set((state) => ({
          origins: state.origins.filter((o) => o !== host),
        }));
      },

      isTrusted: (host) => {
        const trimmed = host.trim().toLowerCase();
        return get().origins.includes(trimmed);
      },
    }),
    {
      name: TRUSTED_RELAYS_KEY,
      storage: createJSONStorage(() => secureStorage),
    },
  ),
);
