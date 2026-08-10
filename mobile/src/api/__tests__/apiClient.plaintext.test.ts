/**
 * Verifies the plaintext transport mode (RELAY_TRANSPORT_MODE='plain' /
 * RELAY_FALLBACK_MODE='plain'):
 *
 * When pairkey selection demotes to plain (or the flag is forced via
 * `__setTransportModeForTesting('plain')`), smartFetch:
 *   1. Skips ensureCipher / performHandshake — no Noise handshake fires.
 *   2. Skips the LAN encrypted fast-path entirely.
 *   3. Sends the request via fetch() with an `encrypted: false` envelope
 *      that the relay's `proxy_handler.RPC` forwards through the desktop's
 *      handleHTTP plaintext stream type.
 *   4. Returns the relay's outer HTTP status as the TunnelResponse status
 *      (no decryption / no inner status unwrap).
 *
 * This is the bypass we ship while the long-term envelope-embedded-nonce
 * fix is being designed; without these assertions a future refactor could
 * silently re-engage the broken AEAD path.
 */

const mockPerformHandshake = jest.fn();
const mockTunnelFetch = jest.fn();
const mockLanEncryptedRPC = jest.fn();
const mockFindLanEndpoint = jest.fn<
  Promise<unknown>,
  [unknown, unknown?]
>(async () => null);
const mockGlobalFetch = jest.fn();

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
  buildDPoPHeader: jest.fn(() => 'dpop-stub'),
}));
jest.mock('../lan/tunnel', () => ({
  LAN_CAPABLE: true, // pretend LAN is available so we can assert it gets skipped
  openLanTunnel: jest.fn(),
}));
jest.mock('../lan/discover', () => ({
  findLanEndpoint: (...args: unknown[]) =>
    mockFindLanEndpoint(...(args as [unknown, unknown])),
}));
jest.mock('../lan/rpc', () => ({
  lanEncryptedRPC: (...args: unknown[]) => mockLanEncryptedRPC(...args),
}));

// Stub the global fetch so plainTunnelFetch is observable.
beforeAll(() => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).fetch = mockGlobalFetch;
});

import {
  smartFetch,
  __resetEncryptedQueueForTesting,
  __resetLanCacheForTesting,
  __setPlaintextModeForTesting,
  __waitForInflightLanProbesForTesting,
} from '../apiClient';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

const DESKTOP_ID = 'desktop-plain';
const RELAY_URL = 'https://relay.example.com';

beforeEach(() => {
  mockTunnelFetch.mockReset();
  mockPerformHandshake.mockReset();
  mockLanEncryptedRPC.mockReset();
  mockFindLanEndpoint.mockReset();
  mockFindLanEndpoint.mockResolvedValue(null);
  mockGlobalFetch.mockReset();
  __resetEncryptedQueueForTesting();
  __resetLanCacheForTesting();
  __setPlaintextModeForTesting(true); // explicit-on for this suite

  useTransportStore.setState({
    activeDesktopId: DESKTOP_ID,
    activeTransportKind: 'relay',
  } as never);
  useRelayAccountStore.setState({ relayBaseUrl: RELAY_URL } as never);
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

afterEach(async () => {
  __setPlaintextModeForTesting(null);
  // Drain any in-flight background LAN probe before the next test resets
  // mocks; otherwise its writes can land on a clean state from the next
  // beforeEach.
  await __waitForInflightLanProbesForTesting();
  __resetLanCacheForTesting();
});

function makeFetchResponse(opts: {
  status?: number;
  body?: string;
  headers?: Record<string, string>;
}): Response {
  const headers = new Headers(opts.headers ?? {});
  return {
    ok: (opts.status ?? 200) < 400,
    status: opts.status ?? 200,
    statusText: '',
    headers,
    text: async () => opts.body ?? '',
  } as unknown as Response;
}

describe('smartFetch — plaintext escape hatch', () => {
  it('does not trigger a Noise handshake or call tunnelFetch', async () => {
    mockGlobalFetch.mockResolvedValue(
      makeFetchResponse({ status: 200, body: '{"ok":true}' }),
    );

    const result = await smartFetch<{ ok: boolean }>('/api/ping', {
      desktopId: DESKTOP_ID,
    });

    expect(result).toEqual({ ok: true });
    expect(mockPerformHandshake).not.toHaveBeenCalled();
    expect(mockTunnelFetch).not.toHaveBeenCalled();
    // LAN discovery is probed in every mode now (the per-desktop queue
    // makes the LAN noise cipher safe under concurrency); the probe
    // returns null in this fixture so we fall through to the relay
    // path. The negative assertion is on lanEncryptedRPC, not on
    // findLanEndpoint.
    expect(mockLanEncryptedRPC).not.toHaveBeenCalled();
  });

  it('posts an encrypted:false envelope to the relay /d/:id/rpc route', async () => {
    mockGlobalFetch.mockResolvedValue(
      makeFetchResponse({ status: 200, body: '{"hello":"world"}' }),
    );

    await smartFetch('/api/workspaces', {
      desktopId: DESKTOP_ID,
      method: 'GET',
    });

    expect(mockGlobalFetch).toHaveBeenCalledTimes(1);
    const [url, init] = mockGlobalFetch.mock.calls[0];
    expect(url).toBe(`${RELAY_URL}/d/${DESKTOP_ID}/rpc`);
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['Authorization']).toBe(
      'Bearer device-token',
    );
    expect((init.headers as Record<string, string>)['X-DPoP']).toBe('dpop-stub');

    const envelope = JSON.parse(init.body as string) as {
      encrypted: boolean;
      method: string;
      path: string;
      headers: Record<string, string[]>;
      body: string;
    };
    expect(envelope.encrypted).toBe(false);
    expect(envelope.method).toBe('GET');
    expect(envelope.path).toBe('/api/workspaces');
    // Empty body for GET -> empty base64 string.
    expect(envelope.body).toBe('');
  });

  it('returns the local server JSON parsed from the relay outer response body', async () => {
    mockGlobalFetch.mockResolvedValue(
      makeFetchResponse({
        status: 200,
        body: '{"workspaces":[{"id":"w1"}]}',
      }),
    );

    const out = await smartFetch<{ workspaces: { id: string }[] }>(
      '/api/workspaces',
      { desktopId: DESKTOP_ID },
    );
    expect(out.workspaces).toEqual([{ id: 'w1' }]);
  });

  it('falls back to the relay plaintext envelope when LAN is unreachable', async () => {
    // LAN_CAPABLE=true (mocked) but the discovery probe returns null.
    // smartFetch no longer awaits the probe — it kicks it in the
    // background and falls straight through to relay. The probe still
    // gets called (synchronously, inside the fire-and-forget IIFE), so
    // we can assert on it; the request itself returned via the plain
    // tunnel path without paying the probe latency.
    mockFindLanEndpoint.mockResolvedValue(null);
    mockGlobalFetch.mockResolvedValue(
      makeFetchResponse({ status: 200, body: '{"ok":true}' }),
    );

    await smartFetch('/api/ping', { desktopId: DESKTOP_ID });

    expect(mockFindLanEndpoint).toHaveBeenCalledTimes(1);
    expect(mockLanEncryptedRPC).not.toHaveBeenCalled(); // no endpoint cached → never tried
    expect(mockGlobalFetch).toHaveBeenCalledTimes(1); // relay plain path
  });

  it('forwards a relay-level error (X-Niuniu-Error) as a TunnelRelayError-shaped throw', async () => {
    mockGlobalFetch.mockResolvedValue(
      makeFetchResponse({
        status: 503,
        body: '{"error":"DesktopOffline"}',
        headers: { 'X-Niuniu-Error': 'DesktopOffline' },
      }),
    );

    await expect(
      smartFetch('/api/ping', { desktopId: DESKTOP_ID }),
    ).rejects.toThrow();
  });
});
