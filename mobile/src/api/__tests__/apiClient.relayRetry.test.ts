/**
 * Verifies that relayFetch transparently re-handshakes and retries once when
 * the desktop returns 412 + X-Niuniu-Error: no_session (i.e. the desktop's
 * in-memory cipher cache was lost — e.g. after a process restart).
 *
 * Regression coverage for the pairing flow where, absent this retry, the user
 * saw "tunnelFetch: relay error 412" and even manual retries from the UI
 * continued to hit the same stale mobile-side cipher.
 */

import { TunnelRelayError, type TunnelResponse } from '../relay/tunnelFetch';
import type { CipherPair } from '../../crypto/noise';

// ─── Module mocks ────────────────────────────────────────────────────────────

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

// LAN fast-path is not exercised here — force LAN_CAPABLE off by mocking the
// module before relayFetch is imported.
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

// ─── Imports that depend on the mocks above ──────────────────────────────────

import {
  relayFetch,
  __setPlaintextModeForTesting,
} from '../apiClient';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

// ─── Fixtures ────────────────────────────────────────────────────────────────

function dummyCipher(tag: string): CipherPair {
  return {
    encrypt: (pt: Uint8Array) => pt,
    decrypt: (ct: Uint8Array) => ct,
    rekey: () => {},
    setRekeyInterval: () => {},
    rekeyInterval: () => 0,
    // tag makes the two ciphers distinguishable in assertions
    _tag: tag,
  } as unknown as CipherPair;
}

const DESKTOP_ID = 'desktop-abc';

// ─── Setup / teardown ────────────────────────────────────────────────────────

beforeEach(() => {
  mockTunnelFetch.mockReset();
  mockPerformHandshake.mockReset();
  // These specs target the legacy noise path. Production now defaults to
  // RELAY_TRANSPORT_MODE='pairkey'; opt explicitly into the noise path.
  // (`__setPlaintextModeForTesting(false)` is the back-compat shim that
  // maps to `__setTransportModeForTesting('noise')` — kept here so the
  // diff stays small.)
  __setPlaintextModeForTesting(false);

  // Reset stores to a known state.
  useTransportStore.getState().clearAllCiphers();
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

describe('relayFetch — 412 no_session auto re-handshake', () => {
  it('evicts the stale cipher, re-handshakes, and retries once on 412 no_session', async () => {
    const staleCipher = dummyCipher('stale');
    const freshCipher = dummyCipher('fresh');
    mockPerformHandshake
      .mockResolvedValueOnce(staleCipher) // first attempt
      .mockResolvedValueOnce(freshCipher); // retry after eviction

    const successResp: TunnelResponse = {
      status: 200,
      headers: {},
      body: '{"ok":true}',
      ok: true,
    };
    mockTunnelFetch
      .mockRejectedValueOnce(new TunnelRelayError(412, 'no_session', 'boom'))
      .mockResolvedValueOnce(successResp);

    const resp = await relayFetch(DESKTOP_ID, '/api/ping', { method: 'GET' });

    expect(resp).toEqual(successResp);
    expect(mockPerformHandshake).toHaveBeenCalledTimes(2);
    expect(mockTunnelFetch).toHaveBeenCalledTimes(2);

    // The second tunnelFetch must have received the FRESH cipher, not the stale one.
    const secondCall = mockTunnelFetch.mock.calls[1];
    expect(secondCall[3]).toBe(freshCipher);
  });

  it('does not retry on non-412 failures', async () => {
    mockPerformHandshake.mockResolvedValueOnce(dummyCipher('c1'));
    mockTunnelFetch.mockRejectedValueOnce(
      new TunnelRelayError(503, 'DesktopOffline', 'offline'),
    );

    await expect(
      relayFetch(DESKTOP_ID, '/api/ping', { method: 'GET' }),
    ).rejects.toMatchObject({ status: 503 });

    expect(mockTunnelFetch).toHaveBeenCalledTimes(1);
    expect(mockPerformHandshake).toHaveBeenCalledTimes(1);
  });

  it('does not retry when 412 arrives without X-Niuniu-Error: no_session', async () => {
    mockPerformHandshake.mockResolvedValueOnce(dummyCipher('c1'));
    mockTunnelFetch.mockRejectedValueOnce(
      new TunnelRelayError(412, null, 'precondition failed'),
    );

    await expect(
      relayFetch(DESKTOP_ID, '/api/ping', { method: 'GET' }),
    ).rejects.toMatchObject({ status: 412, errorCode: null });

    expect(mockTunnelFetch).toHaveBeenCalledTimes(1);
    expect(mockPerformHandshake).toHaveBeenCalledTimes(1);
  });
});

describe('relayFetch — concurrent handshake deduplication', () => {
  it('parallel calls on a cold cache trigger exactly ONE handshake', async () => {
    // Resolve the handshake lazily so we can fire N parallel relayFetch calls
    // before the first handshake completes — otherwise the second call would
    // find the cipher already in the store and legitimately skip the handshake.
    let resolveHandshake: (cipher: CipherPair) => void = () => {};
    const handshakePromise = new Promise<CipherPair>((resolve) => {
      resolveHandshake = resolve;
    });
    mockPerformHandshake.mockReturnValueOnce(handshakePromise);

    const sharedCipher = dummyCipher('shared');
    const successResp: TunnelResponse = {
      status: 200,
      headers: {},
      body: '{"ok":true}',
      ok: true,
    };
    mockTunnelFetch.mockResolvedValue(successResp);

    // Kick off N parallel requests against a cold cipher cache.
    const parallel = [
      relayFetch(DESKTOP_ID, '/api/a'),
      relayFetch(DESKTOP_ID, '/api/b'),
      relayFetch(DESKTOP_ID, '/api/c'),
      relayFetch(DESKTOP_ID, '/api/d'),
    ];

    // Let the microtask queue drain so each call reaches the handshake-check.
    await new Promise((r) => setImmediate(r));

    // Release the single in-flight handshake.
    resolveHandshake(sharedCipher);
    const results = await Promise.all(parallel);

    expect(results).toHaveLength(4);
    expect(mockPerformHandshake).toHaveBeenCalledTimes(1);
    expect(mockTunnelFetch).toHaveBeenCalledTimes(4);

    // All four tunnelFetch invocations must have received the SAME cipher
    // (the shared one from the single handshake).
    for (const call of mockTunnelFetch.mock.calls) {
      expect(call[3]).toBe(sharedCipher);
    }
  });
});
