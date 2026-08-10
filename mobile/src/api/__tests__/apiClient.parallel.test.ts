/**
 * Verifies that pairkey and plain transports run **concurrently** per
 * desktop now that the per-desktop serial queue has been scoped down to
 * the two transports that actually need it (relay-noise and LAN, both
 * Noise KK).
 *
 * Before this fix the queue was wrapped around `smartFetch` at the top
 * level, so every encrypted RPC against a given desktop ran one-at-a-time
 * regardless of transport mode. With production defaulting to pairkey
 * (stateless random-nonce ChaCha20-Poly1305), that was pure overhead —
 * the cipher has no monotonic state to protect. This test pins the
 * "parallel by default" behaviour for pairkey and plain.
 *
 * The companion serialization regression check — that noise mode IS still
 * serialized — lives in `apiClient.serialization.test.ts`.
 */

const mockPairKeyFetch = jest.fn();
const mockPlainTunnelFetch = jest.fn();

jest.mock('../relay/pairKeyFetch', () => {
  const actual = jest.requireActual('../relay/pairKeyFetch');
  return { ...actual, pairKeyFetch: (...args: unknown[]) => mockPairKeyFetch(...args) };
});
jest.mock('../relay/plainFetch', () => ({
  plainTunnelFetch: (...args: unknown[]) => mockPlainTunnelFetch(...args),
}));
jest.mock('../relay/tunnelFetch', () => {
  const actual = jest.requireActual('../relay/tunnelFetch');
  return { ...actual, tunnelFetch: jest.fn() };
});
jest.mock('../relay/handshake', () => ({ performHandshake: jest.fn() }));
jest.mock('../../crypto/identity', () => ({
  getOrCreateIdentity: jest.fn(async () => ({
    edPriv: new Uint8Array(32),
    edPub: new Uint8Array(32),
    xPriv: new Uint8Array(32),
    xPub: new Uint8Array(32),
  })),
}));
jest.mock('../../crypto/dpop', () => ({
  buildDPoPHeader: jest.fn(() => 'dpop-stub'),
}));
jest.mock('../lan/tunnel', () => ({
  LAN_CAPABLE: false,
  openLanTunnel: jest.fn(),
}));
jest.mock('../lan/discover', () => ({
  findLanEndpoint: jest.fn(async () => null),
}));
jest.mock('../lan/rpc', () => ({ lanEncryptedRPC: jest.fn() }));

import { encode as b64Encode } from '@stablelib/base64';
import {
  smartFetch,
  __resetEncryptedQueueForTesting,
  __resetLanCacheForTesting,
  __setTransportModeForTesting,
} from '../apiClient';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

const DESKTOP_ID = 'desktop-parallel';

function jsonResp(body: unknown) {
  return {
    status: 200,
    headers: {},
    body: JSON.stringify(body),
    ok: true,
  };
}

beforeEach(() => {
  mockPairKeyFetch.mockReset();
  mockPlainTunnelFetch.mockReset();
  __resetEncryptedQueueForTesting();
  __resetLanCacheForTesting();
  useTransportStore.getState().clearAllCiphers();
  useTransportStore.setState({
    activeDesktopId: DESKTOP_ID,
    activeTransportKind: 'relay',
  } as never);
  useRelayAccountStore.setState({
    relayBaseUrl: 'https://relay.example.com',
  } as never);
  usePairedDesktopsStore.setState({
    desktops: [
      {
        desktopId: DESKTOP_ID,
        desktopName: 'Test desktop',
        xpub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
        signPub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
        relayDeviceToken: 'device-token',
        // mobileId + 32-byte pair key are required for the pairkey branch
        // to be selected; without them, smartFetch demotes to fallback.
        mobileId: 'mobile-1',
        pairKey: b64Encode(new Uint8Array(32).fill(7)),
        pairedAt: '2026-04-24T00:00:00.000Z',
      },
    ],
  } as never);
});

afterEach(() => {
  __setTransportModeForTesting(null);
});

describe('smartFetch — pairkey and plain run concurrently per desktop', () => {
  it('runs 4 pairkey RPCs in parallel (maxInFlight = 4)', async () => {
    __setTransportModeForTesting('pairkey');

    let inFlight = 0;
    let maxInFlight = 0;
    mockPairKeyFetch.mockImplementation(async () => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      // Yield long enough for the next call to enter pairKeyFetch
      // concurrently — under the old top-level queue this would be
      // impossible (maxInFlight would clamp to 1).
      await new Promise((r) => setTimeout(r, 10));
      inFlight--;
      return jsonResp({ ok: true });
    });

    await Promise.all([
      smartFetch('/api/a', { desktopId: DESKTOP_ID }),
      smartFetch('/api/b', { desktopId: DESKTOP_ID }),
      smartFetch('/api/c', { desktopId: DESKTOP_ID }),
      smartFetch('/api/d', { desktopId: DESKTOP_ID }),
    ]);

    expect(mockPairKeyFetch).toHaveBeenCalledTimes(4);
    expect(maxInFlight).toBe(4);
  });

  it('runs 4 plain RPCs in parallel (maxInFlight = 4)', async () => {
    __setTransportModeForTesting('plain');

    let inFlight = 0;
    let maxInFlight = 0;
    mockPlainTunnelFetch.mockImplementation(async () => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await new Promise((r) => setTimeout(r, 10));
      inFlight--;
      return jsonResp({ ok: true });
    });

    await Promise.all([
      smartFetch('/api/a', { desktopId: DESKTOP_ID }),
      smartFetch('/api/b', { desktopId: DESKTOP_ID }),
      smartFetch('/api/c', { desktopId: DESKTOP_ID }),
      smartFetch('/api/d', { desktopId: DESKTOP_ID }),
    ]);

    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(4);
    expect(maxInFlight).toBe(4);
  });
});
