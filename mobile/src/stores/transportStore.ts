/**
 * transportStore — active transport state.
 *
 * Holds the live CipherPair instances and LAN tunnel handles keyed by
 * desktopId, plus a flag indicating which transport kind is active.
 *
 * Persistence: only `activeDesktopId` and `activeTransportKind` are written
 * to SecureStore (see `partialize` below). Ciphers, LAN handles, and
 * handshake timestamps are session-ephemeral — CipherPair holds live Noise
 * state that cannot resume across process restarts, and LanTunnelHandle
 * wraps a live yamux session. Persisting the user's transport *choice* lets
 * returning users skip the desktop-selection screen on cold start; the
 * ciphers themselves are re-established on the next API call.
 *
 * Import-cycle invariant: this module MUST NOT runtime-import either
 * `pairedDesktopsStore` or `relayAccountStore`. Those stores import
 * `useTransportStore` as a value (for cross-store cleanup on unpair /
 * logout), so a reciprocal runtime import here would create a cycle that
 * Metro resolves by returning `undefined` on the late side. Use
 * `import type` only if a type from those modules is ever needed.
 */
import { useEffect, useState } from 'react';
import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';
import * as SecureStore from 'expo-secure-store';
import type { CipherPair } from '../crypto/noise';
import type { LanTunnelHandle } from '../api/lan/tunnel';

// SecureStore on Android requires keys to match ^[\w.-]+$ — no colons.
const TRANSPORT_KEY = 'niuniu.transport';

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

export type TransportKind = 'direct' | 'relay';

interface TransportState {
  /** Active Noise KK cipher pairs, one per desktop session */
  activeCiphers: Map<string, CipherPair>;
  /** Active LAN tunnel handles (yamux + cipher), one per desktop session */
  lanHandles: Map<string, LanTunnelHandle>;
  /** Which transport backend is currently in use */
  activeTransportKind: TransportKind;
  /** The desktop currently selected by the user as the active connection target */
  activeDesktopId: string | null;
  /**
   * Timestamp (ms) of the last successful Noise KK handshake per desktopId.
   * Set whenever setCipher is called.
   */
  lastHandshakeAt: Record<string, number>;

  // Actions
  setCipher: (desktopId: string, cipher: CipherPair) => void;
  getCipher: (desktopId: string) => CipherPair | undefined;
  removeCipher: (desktopId: string) => void;
  clearCiphers: () => void;
  /** Clears all active ciphers and closes all LAN handles. */
  clearAllCiphers: () => void;
  setTransportKind: (kind: TransportKind) => void;
  setActiveDesktopId: (id: string | null) => void;
  getActiveDesktopId: () => string | null;
  /** Store a LAN tunnel handle for the given desktop. */
  setLanHandle: (desktopId: string, handle: LanTunnelHandle) => void;
  /** Retrieve a cached LAN tunnel handle, or undefined if none. */
  getLanHandle: (desktopId: string) => LanTunnelHandle | undefined;
  /** Close and remove the LAN handle for a specific desktop. */
  removeLanHandle: (desktopId: string) => void;
  /**
   * Reset the user's persisted transport choice to the initial state
   * (activeDesktopId=null, kind='direct'). Use on logout or when clearing
   * multi-account state. Does not affect live ciphers/handles — call
   * `clearAllCiphers` for that.
   */
  resetActiveTransport: () => void;
}

export const useTransportStore = create<TransportState>()(
  persist(
    (set, get) => ({
      activeCiphers: new Map(),
      lanHandles: new Map(),
      activeTransportKind: 'direct',
      activeDesktopId: null,
      lastHandshakeAt: {},

      setCipher: (desktopId, cipher) => {
        set((state) => {
          const next = new Map(state.activeCiphers);
          next.set(desktopId, cipher);
          return {
            activeCiphers: next,
            lastHandshakeAt: { ...state.lastHandshakeAt, [desktopId]: Date.now() },
          };
        });
      },

      getCipher: (desktopId) => {
        return get().activeCiphers.get(desktopId);
      },

      removeCipher: (desktopId) => {
        set((state) => {
          const next = new Map(state.activeCiphers);
          next.delete(desktopId);
          return { activeCiphers: next };
        });
      },

      clearCiphers: () => {
        set({ activeCiphers: new Map() });
      },

      clearAllCiphers: () => {
        // Close all LAN handles before clearing
        const { lanHandles } = get();
        for (const handle of lanHandles.values()) {
          try {
            handle.close();
          } catch {
            // ignore errors during teardown
          }
        }
        set({ activeCiphers: new Map(), lanHandles: new Map() });
      },

      setTransportKind: (kind) => {
        set({ activeTransportKind: kind });
      },

      setActiveDesktopId: (id) => {
        set({ activeDesktopId: id });
      },

      getActiveDesktopId: () => {
        return get().activeDesktopId;
      },

      setLanHandle: (desktopId, handle) => {
        set((state) => {
          const next = new Map(state.lanHandles);
          next.set(desktopId, handle);
          return { lanHandles: next };
        });
      },

      getLanHandle: (desktopId) => {
        return get().lanHandles.get(desktopId);
      },

      removeLanHandle: (desktopId) => {
        set((state) => {
          const existing = state.lanHandles.get(desktopId);
          if (existing) {
            try {
              existing.close();
            } catch {
              // ignore
            }
          }
          const next = new Map(state.lanHandles);
          next.delete(desktopId);
          return { lanHandles: next };
        });
      },

      resetActiveTransport: () => {
        set({ activeDesktopId: null, activeTransportKind: 'direct' });
      },
    }),
    {
      name: TRANSPORT_KEY,
      storage: createJSONStorage(() => secureStorage),
      // Persist ONLY the user's transport choice. Everything else
      // (ciphers, LAN handles, handshake timestamps) is session-ephemeral
      // and will be re-established after hydration.
      partialize: (state) => ({
        activeDesktopId: state.activeDesktopId,
        activeTransportKind: state.activeTransportKind,
      }),
    },
  ),
);

// ─── Hydration hook ─────────────────────────────────────────────────────

/**
 * Returns true once the SecureStore-backed persist middleware has finished
 * rehydrating. Routing code that gates on `activeDesktopId` must wait for
 * this; otherwise a cold-start read returns the in-memory default (null)
 * before the persisted choice arrives, and a valid returning user gets
 * bounced to the desktop-selection screen.
 */
export function useTransportStoreHydrated(): boolean {
  const [hydrated, setHydrated] = useState<boolean>(() =>
    useTransportStore.persist.hasHydrated(),
  );
  useEffect(() => {
    if (hydrated) return;
    const unsub = useTransportStore.persist.onFinishHydration(() =>
      setHydrated(true),
    );
    return unsub;
  }, [hydrated]);
  return hydrated;
}
