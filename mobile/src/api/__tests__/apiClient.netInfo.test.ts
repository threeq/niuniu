/**
 * Verifies that the NetInfo subscription wired in apiClient invalidates the
 * LAN endpoint cache the moment the device changes networks.
 *
 * Without this, a 'hit' verdict captured on home Wi-Fi would survive the
 * switch to cellular and the next request would try to open a tunnel to an
 * unreachable 192.168.x.x; conversely a 'miss' captured on cellular would
 * block the LAN fast path even after the user rejoined the desktop's
 * network. The handler clears both the verdict cache and the inflight probe
 * map, and bumps `probeGeneration` so an in-flight probe from the prior
 * network discards its (now-stale) verdict on completion.
 *
 * End-to-end shape:
 *   1. First call → cache miss → background probe kicked → request goes
 *      relay (no probe wait).
 *   2. Wait for the probe to settle → cache verdict stored.
 *   3. Subsequent calls within TTL → no re-probe (cache hit/miss serves).
 *   4. NetInfo network-change event → cache + inflight cleared, generation
 *      bumped.
 *   5. Next call → cache empty again → fresh background probe kicked.
 */

const mockAddEventListener = jest.fn();
const mockUnsubscribe = jest.fn();

jest.mock('@react-native-community/netinfo', () => ({
  __esModule: true,
  default: {
    addEventListener: (...args: unknown[]) => {
      mockAddEventListener(...args);
      return mockUnsubscribe;
    },
  },
}));

const mockPlainTunnelFetch = jest.fn();
const mockFindLanEndpoint = jest.fn();

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
  openLanTunnel: jest.fn(),
}));
jest.mock('../lan/discover', () => ({
  findLanEndpoint: (...args: unknown[]) => mockFindLanEndpoint(...args),
}));
jest.mock('../lan/rpc', () => ({ lanEncryptedRPC: jest.fn() }));

import {
  smartFetch,
  __resetEncryptedQueueForTesting,
  __resetLanCacheForTesting,
  __resetNetInfoForTesting,
  __subscribeToNetworkChangesForTesting,
  __setTransportModeForTesting,
  __waitForInflightLanProbesForTesting,
} from '../apiClient';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

const DESKTOP_ID = 'desktop-netinfo';

beforeEach(() => {
  mockAddEventListener.mockClear();
  mockUnsubscribe.mockClear();
  mockPlainTunnelFetch.mockReset();
  mockFindLanEndpoint.mockReset();
  __resetEncryptedQueueForTesting();
  __resetLanCacheForTesting();
  __resetNetInfoForTesting();
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
  mockFindLanEndpoint.mockResolvedValue(null);
});

afterEach(async () => {
  __setTransportModeForTesting(null);
  await __waitForInflightLanProbesForTesting();
  __resetLanCacheForTesting();
  __resetNetInfoForTesting();
});

describe('apiClient — NetInfo-driven LAN cache invalidation', () => {
  it('subscribes exactly once on (re)bootstrap and is idempotent', () => {
    __subscribeToNetworkChangesForTesting();
    __subscribeToNetworkChangesForTesting(); // second call must not re-subscribe
    expect(mockAddEventListener).toHaveBeenCalledTimes(1);
  });

  it('returns the unsubscribe handle so the listener tears down on reset', () => {
    __subscribeToNetworkChangesForTesting();
    __resetNetInfoForTesting();
    expect(mockUnsubscribe).toHaveBeenCalledTimes(1);
  });

  it('wipes the LAN cache so the next API call kicks a fresh probe', async () => {
    __subscribeToNetworkChangesForTesting();
    expect(mockAddEventListener).toHaveBeenCalledTimes(1);
    const onChange = mockAddEventListener.mock.calls[0][0] as () => void;

    // (1) First call: cache empty → background probe kicked → relay used.
    await smartFetch('/api/a', { desktopId: DESKTOP_ID });
    await __waitForInflightLanProbesForTesting(); // verdict lands as 'miss'

    // (2) Second call within the miss-TTL window: no re-probe.
    await smartFetch('/api/b', { desktopId: DESKTOP_ID });
    expect(mockFindLanEndpoint).toHaveBeenCalledTimes(1);

    // (3) Network change → cache + inflight cleared, generation bumped.
    onChange();

    // (4) Third call: cache empty again → fresh probe kicked.
    await smartFetch('/api/c', { desktopId: DESKTOP_ID });
    expect(mockFindLanEndpoint).toHaveBeenCalledTimes(2);
  });
});
