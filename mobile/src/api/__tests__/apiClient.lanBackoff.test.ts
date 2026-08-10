/**
 * Verifies the off-request-path LAN endpoint cache in apiClient:
 *
 *   * `findLanEndpoint` is no longer awaited inside `smartFetch`. The very
 *     first request to a fresh desktop returns through relay immediately
 *     and a probe is kicked in the background; the next request after the
 *     probe settles picks up the LAN fast-path (or stays on relay if the
 *     probe verdict was 'miss').
 *   * Cache entries have separate hit/miss TTLs (30 s / 60 s); within those
 *     windows no fresh probe is started. The inflight map dedups concurrent
 *     probe kicks so a burst of requests on a cold cache only fires one.
 *   * On a LAN tunnel error against a cached 'hit', the entry is downgraded
 *     to 'miss' and the LAN handle evicted, so the immediate next request
 *     takes the relay path without re-opening the broken tunnel.
 *
 * Background — what this fix replaced:
 *   The previous design awaited `findLanEndpoint(desktop, 3000)` inline on
 *   every API call. When `lastKnownLanEndpoints` existed but were
 *   unreachable, every call paid the full 3 s probe timeout before falling
 *   back. The negative cache helped within a 60 s window but the first call
 *   after every expiry still ate 3 s. The new design takes the probe off
 *   the request path entirely.
 */

const mockPlainTunnelFetch = jest.fn();
const mockFindLanEndpoint = jest.fn();
const mockOpenLanTunnel = jest.fn();
const mockLanEncryptedRPC = jest.fn();

jest.mock('../relay/plainFetch', () => ({
  plainTunnelFetch: (...args: unknown[]) => mockPlainTunnelFetch(...args),
}));
jest.mock('../relay/pairKeyFetch', () => {
  const actual = jest.requireActual('../relay/pairKeyFetch');
  return { ...actual, pairKeyFetch: jest.fn() };
});
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
  LAN_CAPABLE: true,
  openLanTunnel: (...args: unknown[]) => mockOpenLanTunnel(...args),
}));
jest.mock('../lan/discover', () => ({
  findLanEndpoint: (...args: unknown[]) => mockFindLanEndpoint(...args),
}));
jest.mock('../lan/rpc', () => ({
  lanEncryptedRPC: (...args: unknown[]) => mockLanEncryptedRPC(...args),
}));

import {
  smartFetch,
  __resetEncryptedQueueForTesting,
  __resetLanCacheForTesting,
  __setTransportModeForTesting,
  __waitForInflightLanProbesForTesting,
} from '../apiClient';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

const DESKTOP_ID = 'desktop-lan-cache';

beforeEach(() => {
  mockPlainTunnelFetch.mockReset();
  mockFindLanEndpoint.mockReset();
  mockOpenLanTunnel.mockReset();
  mockLanEncryptedRPC.mockReset();
  __resetEncryptedQueueForTesting();
  __resetLanCacheForTesting();
  __setTransportModeForTesting('plain');
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
        lastKnownLanEndpoints: [
          { host: '192.168.1.42', port: 9988, lastSeen: Date.now() },
        ],
      },
    ],
  } as never);

  mockPlainTunnelFetch.mockResolvedValue({
    status: 200,
    headers: {},
    body: JSON.stringify({ ok: true }),
    ok: true,
  });
});

afterEach(async () => {
  __setTransportModeForTesting(null);
  await __waitForInflightLanProbesForTesting();
  __resetLanCacheForTesting();
});

describe('smartFetch — LAN endpoint cache (off-request-path probe)', () => {
  it('kicks a background probe on cache miss and serves the request via relay', async () => {
    mockFindLanEndpoint.mockResolvedValue(null);

    await smartFetch('/api/a', { desktopId: DESKTOP_ID });

    // Probe was triggered (synchronously, even though it resolves later).
    expect(mockFindLanEndpoint).toHaveBeenCalledTimes(1);
    // Request returned via relay — no LAN RPC attempted on a cold cache.
    expect(mockLanEncryptedRPC).not.toHaveBeenCalled();
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
  });

  it('dedups concurrent probe kicks; a burst of requests fires exactly one probe', async () => {
    mockFindLanEndpoint.mockImplementation(
      // Slow probe so we can race three smartFetches against it.
      () => new Promise((r) => setTimeout(() => r(null), 30)),
    );

    await Promise.all([
      smartFetch('/api/a', { desktopId: DESKTOP_ID }),
      smartFetch('/api/b', { desktopId: DESKTOP_ID }),
      smartFetch('/api/c', { desktopId: DESKTOP_ID }),
    ]);

    // Only the first call kicks; the next two see the inflight entry.
    expect(mockFindLanEndpoint).toHaveBeenCalledTimes(1);
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(3);
  });

  it('caches a "miss" verdict and refrains from probing again within the TTL window', async () => {
    mockFindLanEndpoint.mockResolvedValue(null);

    await smartFetch('/api/a', { desktopId: DESKTOP_ID });
    await __waitForInflightLanProbesForTesting(); // verdict lands as 'miss'

    await smartFetch('/api/b', { desktopId: DESKTOP_ID });
    await smartFetch('/api/c', { desktopId: DESKTOP_ID });

    // 'miss' cached → no further probes within the window.
    expect(mockFindLanEndpoint).toHaveBeenCalledTimes(1);
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(3);
  });

  it('routes the next request through LAN once the probe records a "hit"', async () => {
    mockFindLanEndpoint.mockResolvedValue({ host: '192.168.1.42', port: 9988 });
    mockOpenLanTunnel.mockResolvedValue({
      yamux: { openStream: jest.fn(), close: jest.fn() },
      cipher: { encrypt: (b: Uint8Array) => b, decrypt: (b: Uint8Array) => b },
    });
    mockLanEncryptedRPC.mockResolvedValue({
      status: 200,
      headers: {},
      body: new TextEncoder().encode('{"ok":true}'),
    });

    // Cold cache — relay path.
    await smartFetch('/api/cold', { desktopId: DESKTOP_ID });
    await __waitForInflightLanProbesForTesting(); // 'hit' lands

    // Warm cache — LAN path now.
    const result = await smartFetch<{ ok: boolean }>('/api/warm', {
      desktopId: DESKTOP_ID,
    });
    expect(result).toEqual({ ok: true });
    expect(mockLanEncryptedRPC).toHaveBeenCalledTimes(1);
    // First request went relay, second went LAN — relay total stays at 1.
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
  });

  it('downgrades a stale "hit" to "miss" when the LAN RPC throws', async () => {
    mockFindLanEndpoint.mockResolvedValue({ host: '192.168.1.42', port: 9988 });
    mockOpenLanTunnel.mockResolvedValue({
      yamux: { openStream: jest.fn(), close: jest.fn() },
      cipher: { encrypt: (b: Uint8Array) => b, decrypt: (b: Uint8Array) => b },
    });
    mockLanEncryptedRPC.mockRejectedValueOnce(new Error('tunnel went away'));

    // Prime the cache as 'hit'.
    await smartFetch('/api/cold', { desktopId: DESKTOP_ID });
    await __waitForInflightLanProbesForTesting();
    mockFindLanEndpoint.mockClear();

    // Next request: LAN RPC throws → fall back to relay → cache downgraded.
    await smartFetch('/api/broken', { desktopId: DESKTOP_ID });

    // A subsequent request should NOT re-probe (we're now in 'miss' window)
    // and should go relay.
    await smartFetch('/api/after', { desktopId: DESKTOP_ID });
    expect(mockFindLanEndpoint).not.toHaveBeenCalled();
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(3); // cold + broken-fallback + after
  });
});
