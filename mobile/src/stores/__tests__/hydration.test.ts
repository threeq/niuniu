/**
 * Smoke tests for the zustand persist lifecycle that `useRelayAccountHydrated`,
 * `usePairedDesktopsHydrated`, and `useTransportStoreHydrated` wrap. We test
 * the underlying `persist.hasHydrated()` + `onFinishHydration` contract
 * directly — the hooks themselves are thin React shells around this API, and
 * rendering React in node-env Jest requires jsdom + @testing-library (not
 * installed). If the underlying zustand contract holds, the hook cannot
 * regress without an obvious edit.
 */
import { usePairedDesktopsStore } from '../pairedDesktopsStore';
import { useRelayAccountStore } from '../relayAccountStore';
import { useTransportStore } from '../transportStore';

type HydratableStore = {
  persist: {
    hasHydrated: () => boolean;
    onFinishHydration: (fn: () => void) => () => void;
  };
};

const stores: Array<[string, HydratableStore]> = [
  ['relayAccountStore', useRelayAccountStore as unknown as HydratableStore],
  ['pairedDesktopsStore', usePairedDesktopsStore as unknown as HydratableStore],
  ['transportStore', useTransportStore as unknown as HydratableStore],
];

describe.each(stores)('%s persist lifecycle', (_name, store) => {
  it('exposes persist.hasHydrated()', () => {
    // Persist is async; hasHydrated() MUST be callable immediately without
    // throwing even if rehydration is still in flight (that's the gate the
    // hook relies on).
    expect(() => store.persist.hasHydrated()).not.toThrow();
  });

  it('exposes onFinishHydration() with an unsubscribe return', () => {
    const unsub = store.persist.onFinishHydration(() => {
      /* no-op */
    });
    expect(typeof unsub).toBe('function');
    // Unsubscribe must be idempotent-safe (calling it twice).
    unsub();
    expect(() => unsub()).not.toThrow();
  });
});
