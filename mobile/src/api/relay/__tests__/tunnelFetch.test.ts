/**
 * tunnelFetch.test.ts
 *
 * Verifies that tunnelFetch serialises the inner request envelope with the
 * field name "body" (not "body_b64"), matching the Go side's
 *   type innerEnvelope struct { Body []byte `json:"body"` }
 * where Go's encoding/json auto-decodes base64 strings into []byte.
 */

import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';
import type { CipherPair } from '../../../crypto/noise';
import type { TunnelRequest } from '../tunnelFetch';
import { tunnelFetch, TunnelRelayError } from '../tunnelFetch';
import { buildDPoPHeader } from '../../../crypto/dpop';

// ─── Mock global fetch ────────────────────────────────────────────────────────

// Capture the last outer request body so tests can inspect it.
let lastOuterBody: string | null = null;
// The relay response to return.
let mockRelayResponse: Response;

global.fetch = jest.fn(async (_url: string, init?: RequestInit) => {
  lastOuterBody = init?.body as string;
  return mockRelayResponse;
}) as jest.Mock;

// ─── Mock DPoP builder ────────────────────────────────────────────────────────
// buildDPoPHeader now takes (edPriv, deviceTokenPlain, method, path) but the
// test does not exercise the actual crypto — a constant is sufficient.
jest.mock('../../../crypto/dpop', () => ({
  buildDPoPHeader: jest.fn(() => 'mock-dpop-proof'),
}));

// ─── Helpers ──────────────────────────────────────────────────────────────────

/** A no-op cipher pair that returns plaintext as ciphertext and vice-versa. */
function identityCipher(): CipherPair {
  return {
    encrypt: (pt: Uint8Array) => pt,
    decrypt: (ct: Uint8Array) => ct,
    rekey: () => {},
    setRekeyInterval: () => {},
    rekeyInterval: () => 0,
  };
}

/**
 * Build a mock relay Response whose body is the encrypted (identity-ciphered)
 * inner response JSON.
 */
function buildMockRelayResponse(innerJson: object): Response {
  const plain = new TextEncoder().encode(JSON.stringify(innerJson));
  // identity cipher — ciphertext == plaintext
  const body = plain;
  return {
    ok: true,
    status: 200,
    statusText: 'OK',
    arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    json: async () => ({}),
  } as unknown as Response;
}

// ─── Tests ────────────────────────────────────────────────────────────────────

beforeEach(() => {
  lastOuterBody = null;
  (global.fetch as jest.Mock).mockClear();
});

describe('tunnelFetch inner envelope field name', () => {
  it('uses "body" (not "body_b64") for the request body field', async () => {
    const bodyText = 'hello relay';
    const bodyBytes = new TextEncoder().encode(bodyText);

    mockRelayResponse = buildMockRelayResponse({
      status: 200,
      headers: {},
      body: '',
    });

    const req: TunnelRequest = {
      method: 'POST',
      path: '/api/test',
      headers: { 'Content-Type': 'application/json' },
      body: bodyBytes,
    };

    await tunnelFetch(
      'https://relay.example.com',
      'desktop-123',
      req,
      identityCipher(),
      'device-token-abc',
      new Uint8Array(32), // ed25519 private key mock
    );

    // The outer POST body contains { encrypted: true, body: base64(ciphertext) }.
    // The ciphertext (via identity cipher) is the JSON-encoded inner envelope.
    expect(lastOuterBody).not.toBeNull();
    const outerJson = JSON.parse(lastOuterBody!);
    expect(outerJson.encrypted).toBe(true);
    expect(typeof outerJson.body).toBe('string');

    // Decode the outer body → inner envelope plaintext (identity cipher means b64decode == plaintext)
    const ciphertext = b64Decode(outerJson.body);
    const innerJson = JSON.parse(new TextDecoder().decode(ciphertext));

    // Must have "body" field, not "body_b64"
    expect(innerJson).toHaveProperty('body');
    expect(innerJson).not.toHaveProperty('body_b64');

    // The "body" field must be the base64 of the original bytes
    const decoded = b64Decode(innerJson.body);
    expect(new TextDecoder().decode(decoded)).toBe(bodyText);
  });

  it('omits the body field entirely when request has no body', async () => {
    mockRelayResponse = buildMockRelayResponse({ status: 200, headers: {}, body: '' });

    const req: TunnelRequest = {
      method: 'GET',
      path: '/api/ping',
    };

    await tunnelFetch(
      'https://relay.example.com',
      'desktop-456',
      req,
      identityCipher(),
      'device-token-xyz',
      new Uint8Array(32),
    );

    expect(lastOuterBody).not.toBeNull();
    const outerJson = JSON.parse(lastOuterBody!);
    const ciphertext = b64Decode(outerJson.body);
    const innerJson = JSON.parse(new TextDecoder().decode(ciphertext));

    expect(innerJson).not.toHaveProperty('body');
    expect(innerJson).not.toHaveProperty('body_b64');
  });
});

describe('tunnelFetch error surface', () => {
  it('throws TunnelRelayError carrying status and X-Niuniu-Error on outer non-2xx', async () => {
    const headers = new Headers({ 'X-Niuniu-Error': 'no_session' });
    mockRelayResponse = {
      ok: false,
      status: 412,
      statusText: 'Precondition Failed',
      headers,
      arrayBuffer: async () => new ArrayBuffer(0),
      json: async () => ({}),
      text: async () => '{"error":"no_session"}',
    } as unknown as Response;

    const req: TunnelRequest = { method: 'GET', path: '/api/test' };

    await expect(
      tunnelFetch(
        'https://relay.example.com',
        'desktop-412',
        req,
        identityCipher(),
        'tok',
        new Uint8Array(32),
      ),
    ).rejects.toMatchObject({
      name: 'TunnelRelayError',
      status: 412,
      errorCode: 'no_session',
    });

    // Also assert the instanceof so callers can structurally match.
    try {
      await tunnelFetch(
        'https://relay.example.com',
        'desktop-412',
        req,
        identityCipher(),
        'tok',
        new Uint8Array(32),
      );
      throw new Error('expected tunnelFetch to throw');
    } catch (err) {
      expect(err).toBeInstanceOf(TunnelRelayError);
    }
  });
});
