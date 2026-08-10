/**
 * Tests for the pairkey transport-mode wiring in apiClient.smartFetch.
 *
 * Three independent concerns rolled into this suite:
 *
 *   1. Selection — when RELAY_TRANSPORT_MODE is "pairkey" AND the desktop has
 *      a 32-byte pair_key in store AND a non-empty mobileId, smartFetch goes
 *      through pairKeyFetch (not plainTunnelFetch, not the legacy noise/
 *      tunnelFetch path).
 *   2. Local-state fallback — same mode, but the desktop's pairKey is missing
 *      or malformed: smartFetch silently demotes to RELAY_FALLBACK_MODE
 *      ("plain") so the user's business flow keeps working without a re-pair
 *      gate. Same when mobileId is missing (with a console.warn so we can
 *      track upgrade-path bugs).
 *   3. Runtime fallback — pairkey was successfully selected, the request
 *      went out, but the relay rejects with 409 +
 *      X-Niuniu-Error: pair_key_unprovisioned / pair_link_missing (because
 *      the link was revoked or the relay was rolled back). smartFetch must
 *      catch and retry once via plainTunnelFetch — without this a relay
 *      rollback bricks every paired mobile until the user re-pairs.
 */

const mockPairKeyFetch = jest.fn();
const mockPlainTunnelFetch = jest.fn();
const mockTunnelFetch = jest.fn();
const mockPerformHandshake = jest.fn();

jest.mock('../relay/pairKeyFetch', () => {
  const actual = jest.requireActual('../relay/pairKeyFetch');
  return {
    ...actual,
    pairKeyFetch: (...args: unknown[]) => mockPairKeyFetch(...args),
  };
});
jest.mock('../relay/plainFetch', () => ({
  plainTunnelFetch: (...args: unknown[]) => mockPlainTunnelFetch(...args),
}));
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
  LAN_CAPABLE: false,
  openLanTunnel: jest.fn(),
}));
jest.mock('../lan/discover', () => ({
  findLanEndpoint: jest.fn(async () => null),
}));
jest.mock('../lan/rpc', () => ({
  lanEncryptedRPC: jest.fn(),
}));

import { encode as b64Encode } from '@stablelib/base64';
import {
  smartFetch,
  __resetEncryptedQueueForTesting,
  __setTransportModeForTesting,
} from '../apiClient';
import { TunnelRelayError } from '../relay/tunnelFetch';
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

const DESKTOP_ID = 'desk-pk';
const MOBILE_ID = 'mob-pk';
const RELAY_URL = 'https://relay.example.com';
const PAIR_KEY_B64 = b64Encode(new Uint8Array(32).fill(7));

beforeEach(() => {
  mockPairKeyFetch.mockReset();
  mockPlainTunnelFetch.mockReset();
  mockTunnelFetch.mockReset();
  mockPerformHandshake.mockReset();
  __resetEncryptedQueueForTesting();
  __setTransportModeForTesting('pairkey');

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
        mobileId: MOBILE_ID,
        pairKey: PAIR_KEY_B64,
        pairedAt: '2026-04-24T00:00:00.000Z',
      },
    ],
  } as never);
});

afterEach(() => {
  __setTransportModeForTesting(null);
});

const ok = (json: unknown) => ({
  status: 200,
  headers: {},
  body: JSON.stringify(json),
  ok: true,
});

describe('smartFetch — pairkey selection', () => {
  it('routes through pairKeyFetch when mode=pairkey and the desktop has a pair_key + mobileId', async () => {
    mockPairKeyFetch.mockResolvedValue(ok({ ok: true }));
    const out = await smartFetch<{ ok: boolean }>('/api/ping', {
      desktopId: DESKTOP_ID,
    });
    expect(out).toEqual({ ok: true });
    expect(mockPairKeyFetch).toHaveBeenCalledTimes(1);
    expect(mockPlainTunnelFetch).not.toHaveBeenCalled();
    expect(mockTunnelFetch).not.toHaveBeenCalled();
    // Verify the supplied pairKey is the decoded 32-byte buffer.
    const [, , mobileIdArg, pairKeyArg] = mockPairKeyFetch.mock.calls[0];
    expect(mobileIdArg).toBe(MOBILE_ID);
    expect(pairKeyArg).toBeInstanceOf(Uint8Array);
    expect(pairKeyArg.length).toBe(32);
  });
});

describe('smartFetch — local-state fallback', () => {
  it('falls back to plainTunnelFetch when pair_key is missing (older pair)', async () => {
    usePairedDesktopsStore.setState({
      desktops: [
        {
          desktopId: DESKTOP_ID,
          desktopName: 'Test desktop',
          xpub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
          signPub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
          relayDeviceToken: 'device-token',
          mobileId: MOBILE_ID,
          // pairKey omitted on purpose
          pairedAt: '2026-04-24T00:00:00.000Z',
        },
      ],
    } as never);
    mockPlainTunnelFetch.mockResolvedValue(ok({ via: 'plain' }));
    const out = await smartFetch<{ via: string }>('/api/ping', {
      desktopId: DESKTOP_ID,
    });
    expect(out).toEqual({ via: 'plain' });
    expect(mockPairKeyFetch).not.toHaveBeenCalled();
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
  });

  it('falls back when pair_key is the wrong length (corrupted store)', async () => {
    usePairedDesktopsStore.setState({
      desktops: [
        {
          desktopId: DESKTOP_ID,
          desktopName: 'Test desktop',
          xpub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
          signPub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
          relayDeviceToken: 'device-token',
          mobileId: MOBILE_ID,
          pairKey: b64Encode(new Uint8Array(20).fill(0)),
          pairedAt: '2026-04-24T00:00:00.000Z',
        },
      ],
    } as never);
    mockPlainTunnelFetch.mockResolvedValue(ok({ via: 'plain' }));
    await smartFetch('/api/ping', { desktopId: DESKTOP_ID });
    expect(mockPairKeyFetch).not.toHaveBeenCalled();
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
  });

  it('warns and falls back when pair_key is present but mobileId is missing', async () => {
    usePairedDesktopsStore.setState({
      desktops: [
        {
          desktopId: DESKTOP_ID,
          desktopName: 'Test desktop',
          xpub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
          signPub: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
          relayDeviceToken: 'device-token',
          // mobileId intentionally absent — anomalous old-store state
          pairKey: PAIR_KEY_B64,
          pairedAt: '2026-04-24T00:00:00.000Z',
        },
      ],
    } as never);
    mockPlainTunnelFetch.mockResolvedValue(ok({ via: 'plain' }));
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    try {
      await smartFetch('/api/ping', { desktopId: DESKTOP_ID });
      expect(mockPairKeyFetch).not.toHaveBeenCalled();
      expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining('pairKey but no mobileId'),
      );
    } finally {
      warn.mockRestore();
    }
  });
});

describe('smartFetch — runtime 409 fallback', () => {
  it('demotes to plainTunnelFetch on 409 pair_key_unprovisioned', async () => {
    mockPairKeyFetch.mockRejectedValueOnce(
      new TunnelRelayError(409, 'pair_key_unprovisioned', 'rolled back'),
    );
    mockPlainTunnelFetch.mockResolvedValue(ok({ via: 'plain-after-rollback' }));
    const out = await smartFetch<{ via: string }>('/api/ping', {
      desktopId: DESKTOP_ID,
    });
    expect(out).toEqual({ via: 'plain-after-rollback' });
    expect(mockPairKeyFetch).toHaveBeenCalledTimes(1);
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
  });

  it('demotes on 409 pair_link_missing', async () => {
    mockPairKeyFetch.mockRejectedValueOnce(
      new TunnelRelayError(409, 'pair_link_missing', 'revoked'),
    );
    mockPlainTunnelFetch.mockResolvedValue(ok({ via: 'plain-after-revoke' }));
    const out = await smartFetch<{ via: string }>('/api/ping', {
      desktopId: DESKTOP_ID,
    });
    expect(out).toEqual({ via: 'plain-after-revoke' });
    expect(mockPlainTunnelFetch).toHaveBeenCalledTimes(1);
  });

  it('does NOT demote on a 412 (Noise no_session) — only the 409 codes are wired', async () => {
    mockPairKeyFetch.mockRejectedValueOnce(
      new TunnelRelayError(412, 'no_session', 'noise stale'),
    );
    await expect(
      smartFetch('/api/ping', { desktopId: DESKTOP_ID }),
    ).rejects.toBeInstanceOf(TunnelRelayError);
    expect(mockPlainTunnelFetch).not.toHaveBeenCalled();
  });

  it('does NOT demote on a desktop-side AEAD failure (decrypt_failed) — that means the key is wrong, retrying with plain would send the broken request through unencrypted', async () => {
    mockPairKeyFetch.mockRejectedValueOnce(
      new TunnelRelayError(400, 'decrypt_failed', 'desktop AEAD'),
    );
    await expect(
      smartFetch('/api/ping', { desktopId: DESKTOP_ID }),
    ).rejects.toBeInstanceOf(TunnelRelayError);
    expect(mockPlainTunnelFetch).not.toHaveBeenCalled();
  });
});
