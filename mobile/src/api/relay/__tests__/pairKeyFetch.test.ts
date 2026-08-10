/**
 * Tests for the pairkey transport (mobile/src/api/relay/pairKeyFetch.ts).
 *
 * The pairkey transport is the long-term replacement for Noise KK on the
 * mobile→relay→desktop encrypted RPC path; see the 2026-05-02 incident
 * note in apiClient.ts for the design rationale.
 *
 * Coverage here:
 *   1. AAD byte-level parity with the Go side. The smoke harness tests the
 *      end-to-end interop, but a fast unit fence catches drift the moment
 *      anyone tweaks the label, separator, or field order.
 *   2. decodePairKey edge cases — empty / wrong length / bad base64 must
 *      all return null so apiClient can fall back without crashing.
 *   3. pairKeyFetch round-trip against an in-process AEAD desktop, plus the
 *      three error surfaces (relay error, desktop error, response AAD
 *      mismatch).
 */

import { ChaCha20Poly1305 } from '@stablelib/chacha20poly1305';
import { encode as b64Encode } from '@stablelib/base64';
import {
  pairKeyFetch,
  decodePairKey,
  __pairKeyAADForTesting,
} from '../pairKeyFetch';
import { TunnelRelayError } from '../tunnelFetch';

jest.mock('../../../crypto/dpop', () => ({
  buildDPoPHeader: jest.fn(() => 'dpop-stub'),
}));
jest.mock('../../../utils/idempotency', () => ({
  generateIdempotencyKey: jest.fn(() => 'idem-stub'),
}));

const RELAY = 'https://relay.example.com';
const DESKTOP_ID = 'desk-pk';
const MOBILE_ID = 'mob-1';
const DEVICE_TOKEN = 'tok';
const ED_PRIV = new Uint8Array(32);

let mockFetch: jest.Mock;
beforeEach(() => {
  mockFetch = jest.fn();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).fetch = mockFetch;
});

function freshKey(): Uint8Array {
  return crypto.getRandomValues(new Uint8Array(32));
}

// ─── 1. AAD byte parity ──────────────────────────────────────────────────────

describe('pairKey AAD format', () => {
  it('request AAD matches the Go side byte-for-byte', () => {
    // Pinned to the same input as
    // server/internal/relayclient/pairkey_test.go:TestPairKeyAAD_Shape
    const ad = __pairKeyAADForTesting('mob-1', 'GET', '/api/ping', false);
    const expected = new TextEncoder().encode(
      'pairkey-rpc/v1\x1fmob-1\x1fGET\x1f/api/ping',
    );
    expect(Buffer.from(ad)).toEqual(Buffer.from(expected));
  });

  it('response AAD uses the response label', () => {
    const ad = __pairKeyAADForTesting('mob-1', 'GET', '/api/ping', true);
    const expected = new TextEncoder().encode(
      'pairkey-rpc-resp/v1\x1fmob-1\x1fGET\x1f/api/ping',
    );
    expect(Buffer.from(ad)).toEqual(Buffer.from(expected));
  });

  it('separator is the ASCII Unit Separator 0x1f', () => {
    const ad = __pairKeyAADForTesting('M', 'X', 'P', false);
    // Three separators between four fields: label / mobileId / method / path.
    const seps = Array.from(ad).filter((b) => b === 0x1f).length;
    expect(seps).toBe(3);
  });
});

// ─── 2. decodePairKey edge cases ─────────────────────────────────────────────

describe('decodePairKey', () => {
  it('returns null for undefined / empty / non-base64 input', () => {
    expect(decodePairKey(undefined)).toBeNull();
    expect(decodePairKey('')).toBeNull();
    expect(decodePairKey('!!!not-base64!!!')).toBeNull();
  });
  it('returns null when the decoded length is not 32 bytes', () => {
    expect(decodePairKey(b64Encode(new Uint8Array(31)))).toBeNull();
    expect(decodePairKey(b64Encode(new Uint8Array(33)))).toBeNull();
    expect(decodePairKey(b64Encode(new Uint8Array(0)))).toBeNull();
  });
  it('returns the bytes when length is exactly 32', () => {
    const k = freshKey();
    const got = decodePairKey(b64Encode(k));
    expect(got).not.toBeNull();
    expect(Array.from(got!)).toEqual(Array.from(k));
  });
});

// ─── 3. pairKeyFetch round-trip + error paths ────────────────────────────────

// Helper: simulates the relay+desktop side. Decrypts the request body with
// the request AAD and returns a Response that wraps a synthetic local-server
// reply encrypted with the response AAD.
function fakeDesktop(opts: {
  pairKey: Uint8Array;
  method: string;
  path: string;
  /** What the synthetic local server returns (default: '{"ok":true}'). */
  responseBody?: string;
  /** Override the outer response status (default: 200). */
  status?: number;
  /** Use a wrong response AAD label so the mobile rejects on AEAD-fail. */
  corruptResponseAAD?: boolean;
}): jest.Mock {
  return jest.fn(async (_url: string, init: RequestInit) => {
    const env = JSON.parse(init.body as string) as {
      cipher_kind: string;
      method: string;
      path: string;
      body: string; // base64(nonce || ciphertext)
    };
    expect(env.cipher_kind).toBe('pairkey');
    expect(env.method).toBe(opts.method);
    expect(env.path).toBe(opts.path);
    const wire = Buffer.from(env.body, 'base64');
    const nonce = wire.subarray(0, 12);
    const ct = wire.subarray(12);
    const aead = new ChaCha20Poly1305(opts.pairKey);
    const reqAd = __pairKeyAADForTesting(MOBILE_ID, opts.method, opts.path, false);
    const reqBody = aead.open(
      new Uint8Array(nonce),
      new Uint8Array(ct),
      reqAd,
    );
    if (!reqBody) throw new Error('test fakeDesktop: request decrypt failed');

    // Build encrypted response.
    const respLabel = !opts.corruptResponseAAD;
    const respAd = __pairKeyAADForTesting(
      MOBILE_ID,
      opts.method,
      opts.path,
      respLabel,
    );
    const respNonce = crypto.getRandomValues(new Uint8Array(12));
    const respPlain = new TextEncoder().encode(opts.responseBody ?? '{"ok":true}');
    const respCt = aead.seal(respNonce, respPlain, respAd);
    const wireResp = new Uint8Array(respNonce.length + respCt.length);
    wireResp.set(respNonce, 0);
    wireResp.set(respCt, respNonce.length);
    return makeResponse(opts.status ?? 200, wireResp);
  });
}

function makeResponse(
  status: number,
  body: Uint8Array | string,
  headers: Record<string, string> = {},
): Response {
  const h = new Headers(headers);
  return {
    ok: status < 400,
    status,
    statusText: '',
    headers: h,
    arrayBuffer: async () =>
      typeof body === 'string'
        ? new TextEncoder().encode(body).buffer
        : body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    text: async () => (typeof body === 'string' ? body : new TextDecoder().decode(body)),
  } as unknown as Response;
}

describe('pairKeyFetch', () => {
  it('round-trips a GET request through the AEAD envelope', async () => {
    const key = freshKey();
    mockFetch.mockImplementation(
      fakeDesktop({ pairKey: key, method: 'GET', path: '/api/ping' }),
    );
    const out = await pairKeyFetch(
      RELAY,
      DESKTOP_ID,
      MOBILE_ID,
      key,
      { method: 'GET', path: '/api/ping' },
      DEVICE_TOKEN,
      ED_PRIV,
    );
    expect(out.ok).toBe(true);
    expect(out.status).toBe(200);
    expect(out.body).toBe('{"ok":true}');
  });

  it('round-trips a POST with a body', async () => {
    const key = freshKey();
    let observedRequestBody: Uint8Array | null = null;
    mockFetch.mockImplementation(async (_url: string, init: RequestInit) => {
      const env = JSON.parse(init.body as string) as { body: string };
      const wire = Buffer.from(env.body, 'base64');
      const nonce = wire.subarray(0, 12);
      const ct = wire.subarray(12);
      const ad = __pairKeyAADForTesting(MOBILE_ID, 'POST', '/api/echo', false);
      observedRequestBody = new ChaCha20Poly1305(key).open(
        new Uint8Array(nonce),
        new Uint8Array(ct),
        ad,
      );
      // Echo back encrypted with response AAD.
      const respAd = __pairKeyAADForTesting(MOBILE_ID, 'POST', '/api/echo', true);
      const respNonce = crypto.getRandomValues(new Uint8Array(12));
      const respCt = new ChaCha20Poly1305(key).seal(
        respNonce,
        new TextEncoder().encode('{"echo":1}'),
        respAd,
      );
      const wireResp = new Uint8Array(respNonce.length + respCt.length);
      wireResp.set(respNonce, 0);
      wireResp.set(respCt, respNonce.length);
      return makeResponse(200, wireResp);
    });

    await pairKeyFetch(
      RELAY,
      DESKTOP_ID,
      MOBILE_ID,
      key,
      {
        method: 'POST',
        path: '/api/echo',
        body: 'hello',
      },
      DEVICE_TOKEN,
      ED_PRIV,
    );
    expect(observedRequestBody).not.toBeNull();
    expect(new TextDecoder().decode(observedRequestBody!)).toBe('hello');
  });

  it('rejects a 32-byte mismatch in the supplied pairKey', async () => {
    await expect(
      pairKeyFetch(
        RELAY,
        DESKTOP_ID,
        MOBILE_ID,
        new Uint8Array(31),
        { method: 'GET', path: '/' },
        DEVICE_TOKEN,
        ED_PRIV,
      ),
    ).rejects.toThrow(/32 bytes/);
  });

  it('throws TunnelRelayError tagged "relay" when X-Niuniu-Error is set', async () => {
    mockFetch.mockResolvedValue(
      makeResponse(409, '{"error":"pair_key_unprovisioned"}', {
        'X-Niuniu-Error': 'pair_key_unprovisioned',
      }),
    );
    let captured: TunnelRelayError | null = null;
    try {
      await pairKeyFetch(
        RELAY,
        DESKTOP_ID,
        MOBILE_ID,
        freshKey(),
        { method: 'GET', path: '/' },
        DEVICE_TOKEN,
        ED_PRIV,
      );
    } catch (err) {
      captured = err as TunnelRelayError;
    }
    expect(captured).toBeInstanceOf(TunnelRelayError);
    expect(captured!.status).toBe(409);
    expect(captured!.errorCode).toBe('pair_key_unprovisioned');
    expect(captured!.message).toMatch(/relay error/);
  });

  it('throws TunnelRelayError tagged "desktop" when X-Niuniu-Desktop-Error is set', async () => {
    // The desktop's writePKErr writes plain text; mobile must NOT try to
    // AEAD-decrypt a desktop error body or the underlying reason vanishes.
    mockFetch.mockResolvedValue(
      makeResponse(400, 'relayclient: decrypt: chacha20poly1305: bla', {
        'Content-Type': 'text/plain; charset=utf-8',
        'X-Niuniu-Desktop-Error': 'decrypt_failed',
      }),
    );
    let captured: TunnelRelayError | null = null;
    try {
      await pairKeyFetch(
        RELAY,
        DESKTOP_ID,
        MOBILE_ID,
        freshKey(),
        { method: 'GET', path: '/' },
        DEVICE_TOKEN,
        ED_PRIV,
      );
    } catch (err) {
      captured = err as TunnelRelayError;
    }
    expect(captured).toBeInstanceOf(TunnelRelayError);
    expect(captured!.status).toBe(400);
    expect(captured!.errorCode).toBe('decrypt_failed');
    expect(captured!.message).toMatch(/desktop error/);
    expect(captured!.message).toMatch(/relayclient: decrypt/);
  });

  it('rejects a response whose AEAD AD does not match (cross-direction)', async () => {
    const key = freshKey();
    mockFetch.mockImplementation(
      fakeDesktop({
        pairKey: key,
        method: 'GET',
        path: '/api/ping',
        // corruptResponseAAD=true makes the desktop "encrypt the response
        // with the request AAD" by mistake — mobile must reject it,
        // because that exact mistake would let an attacker re-frame a
        // request as a response.
        corruptResponseAAD: true,
      }),
    );
    await expect(
      pairKeyFetch(
        RELAY,
        DESKTOP_ID,
        MOBILE_ID,
        key,
        { method: 'GET', path: '/api/ping' },
        DEVICE_TOKEN,
        ED_PRIV,
      ),
    ).rejects.toThrow(/AEAD decrypt failed/);
  });

  it('rejects a response shorter than 12+16 bytes', async () => {
    mockFetch.mockResolvedValue(makeResponse(200, new Uint8Array(20)));
    await expect(
      pairKeyFetch(
        RELAY,
        DESKTOP_ID,
        MOBILE_ID,
        freshKey(),
        { method: 'GET', path: '/' },
        DEVICE_TOKEN,
        ED_PRIV,
      ),
    ).rejects.toThrow(/too short/);
  });
});
