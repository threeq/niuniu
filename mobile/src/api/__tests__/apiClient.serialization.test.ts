/**
 * Verifies that smartFetch serializes encrypted RPCs per desktopId, so a
 * burst of concurrent api.get() calls (e.g. tab mount + workspace SSE
 * reconnect + retry race) cannot push two encrypted frames at the same
 * Noise CipherPair on either end.
 *
 * Background — the AEAD MAC failure that motivated this fix:
 *   1. The mobile JS side encrypts each RPC sequentially (single-threaded),
 *      but `fetch()` parallelises the HTTP POSTs.
 *   2. The relay opens one yamux stream per RPC and forwards them to the
 *      desktop concurrently.
 *   3. The desktop spawns one goroutine per stream; all goroutines share a
 *      single `pairingcrypto.CipherPair` whose internal nonce counter is
 *      not thread-safe and whose AEAD requires monotonic in-order receipt.
 *   4. As soon as one out-of-order decrypt fails, `flynn/noise.CipherState`
 *      flips to permanently invalid — every subsequent RPC on the same
 *      cipher fails with "chacha20poly1305: message authentication failed".
 *
 * The smoke harness reproduces (1)-(4) by firing 4 parallel encrypted RPCs
 * against the production relay; without serialization, ~3 of 4 fail with
 * the AEAD error. With smartFetch serializing per-desktop, all 4 succeed.
 */

import type { TunnelResponse } from '../relay/tunnelFetch';
import type { CipherPair } from '../../crypto/noise';

// ─── Module mocks (must be before importing apiClient) ──────────────────────

const mockTunnelFetch = jest.fn();
const mockPerformHandshake = jest.fn();

jest.mock('../relay/tunnelFetch', () => {
  const actual = jest.requireActual('../relay/tunnelFetch');
  return {
    ...actual,
    tunnelFetch: (...args: unknown[]) => mockTunnelFetch(...args),
  };
});

jest.mock('../relay/handshake', () => ({
  performHandshake: (...args: unknown[]) => mockPerformHandshake(...args),
}));

jest.mock('../../crypto/identity', () => ({
  getOrCreateIdentity: jest.fn(async () => ({
    edPriv: new Uint8Array(32),
    edPub: new Uint8Array(32),
    xPriv: new Uint8Array(32),
    xPub: new Uint8Array(32),
  })),
}));

jest.mock('../../crypto/dpop', () => ({
  buildDPoPHeader: jest.fn(() => 'mock-dpop'),
}));

jest.mock('../lan/tunnel', () => ({
  LAN_CAPABLE: false,
  openLanTunnel: jest.fn(),
}));
jest.mock('../lan/discover', () => ({
  findLanEndpoint: jest.fn(async () => null),
}));
jest.mock('../lan/rpc', () => ({
  lanEncryptedRPC: jest.fn(),
}));

import {
  smartFetch,
  __resetEncryptedQueueForTesting,
  __setPlaintextModeForTesting,
} from '../apiClient';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

// ─── Fixtures ────────────────────────────────────────────────────────────────

function dummyCipher(): CipherPair {
  return {
    encrypt: (pt: Uint8Array) => pt,
    decrypt: (ct: Uint8Array) => ct,
    rekey: () => {},
    setRekeyInterval: () => {},
    rekeyInterval: () => 0,
  } as unknown as CipherPair;
}

const DESKTOP_ID = 'desktop-serial';

function jsonResp(body: unknown): TunnelResponse {
  return {
    status: 200,
    headers: {},
    body: JSON.stringify(body),
    ok: true,
  };
}

// ─── Setup / teardown ────────────────────────────────────────────────────────

beforeEach(() => {
  mockTunnelFetch.mockReset();
  mockPerformHandshake.mockReset();
  __resetEncryptedQueueForTesting();
  // The queue regression tests target the legacy noise path. Production
  // now defaults to RELAY_TRANSPORT_MODE='pairkey'; opt back into noise
  // explicitly so we exercise the cipher-state code that the queue protects.
  __setPlaintextModeForTesting(false);
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
        pairedAt: '2026-04-24T00:00:00.000Z',
      },
    ],
  } as never);
});

afterEach(() => {
  __setPlaintextModeForTesting(null);
});

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('smartFetch — per-desktop encrypted RPC serialization', () => {
  it('runs 4 concurrent calls one-at-a-time on the same cipher', async () => {
    mockPerformHandshake.mockResolvedValue(dummyCipher());

    // Track tunnelFetch entry/exit so we can assert no overlap.
    let inFlight = 0;
    let maxInFlight = 0;
    const callOrder: string[] = [];

    mockTunnelFetch.mockImplementation(async (_baseUrl, _desktopId, req) => {
      const path = (req as { path: string }).path;
      callOrder.push('start:' + path);
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      // Yield to the scheduler so a non-serialized impl would let another
      // call enter tunnelFetch concurrently.
      await new Promise((r) => setTimeout(r, 5));
      inFlight--;
      callOrder.push('end:' + path);
      return jsonResp({ path });
    });

    const calls = [
      smartFetch('/api/a', { desktopId: DESKTOP_ID }),
      smartFetch('/api/b', { desktopId: DESKTOP_ID }),
      smartFetch('/api/c', { desktopId: DESKTOP_ID }),
      smartFetch('/api/d', { desktopId: DESKTOP_ID }),
    ];

    await Promise.all(calls);

    expect(mockTunnelFetch).toHaveBeenCalledTimes(4);
    expect(maxInFlight).toBe(1);
    // Strict serial order: every start is immediately followed by its end
    // before the next start.
    expect(callOrder).toEqual([
      'start:/api/a', 'end:/api/a',
      'start:/api/b', 'end:/api/b',
      'start:/api/c', 'end:/api/c',
      'start:/api/d', 'end:/api/d',
    ]);
  });

  it('a failure in one call does not strand the queue for the next one', async () => {
    mockPerformHandshake.mockResolvedValue(dummyCipher());

    let attempt = 0;
    mockTunnelFetch.mockImplementation(async () => {
      attempt++;
      if (attempt === 1) throw new Error('boom');
      return jsonResp({ ok: true });
    });

    const first = smartFetch('/api/fail', { desktopId: DESKTOP_ID });
    const second = smartFetch('/api/ok', { desktopId: DESKTOP_ID });

    await expect(first).rejects.toThrow();
    await expect(second).resolves.toEqual({ ok: true });
    expect(mockTunnelFetch).toHaveBeenCalledTimes(2);
  });

  it('different desktopIds do NOT block each other', async () => {
    const OTHER_ID = 'desktop-other';
    usePairedDesktopsStore.setState({
      desktops: [
        ...usePairedDesktopsStore.getState().desktops,
        {
          desktopId: OTHER_ID,
          desktopName: 'Other',
          xpub: 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA=',
          signPub: 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA=',
          relayDeviceToken: 'token-2',
          pairedAt: '2026-04-24T00:00:00.000Z',
        },
      ],
    } as never);
    mockPerformHandshake.mockResolvedValue(dummyCipher());

    let inFlight = 0;
    let maxInFlight = 0;
    mockTunnelFetch.mockImplementation(async (_baseUrl, desktopId) => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await new Promise((r) => setTimeout(r, 10));
      inFlight--;
      return jsonResp({ desktopId });
    });

    await Promise.all([
      smartFetch('/api/a', { desktopId: DESKTOP_ID }),
      smartFetch('/api/b', { desktopId: OTHER_ID }),
    ]);

    // Two different desktops can run concurrently; the cipher race only
    // exists per-desktop.
    expect(maxInFlight).toBe(2);
    expect(mockTunnelFetch).toHaveBeenCalledTimes(2);
  });
});
