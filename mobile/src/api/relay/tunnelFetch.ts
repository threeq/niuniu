import { encode as b64Encode } from '@stablelib/base64';
import type { CipherPair } from '../../crypto/noise';
import { buildDPoPHeader } from '../../crypto/dpop';
import { generateIdempotencyKey } from '../../utils/idempotency';
import { fetchOrThrow } from '../fetchDiag';

export interface TunnelRequest {
  method: string;
  path: string;
  headers?: Record<string, string | string[]>;
  body?: string | Uint8Array | null;
}

export interface TunnelResponse {
  status: number;
  headers: Record<string, string[]>;
  body: string;
  ok: boolean;
}

/**
 * TunnelRelayError — thrown by tunnelFetch when the OUTER relay response is
 * non-2xx. Carries the HTTP status and the `X-Niuniu-Error` header code
 * (e.g. "no_session") so callers can react without string-parsing the message.
 *
 * Notable codes:
 *   412 + "no_session" — the desktop lost its cipher cache for this mobile;
 *                       callers should evict the cached cipher and re-handshake.
 */
export class TunnelRelayError extends Error {
  readonly status: number;
  readonly errorCode: string | null;
  constructor(status: number, errorCode: string | null, message: string) {
    super(message);
    this.name = 'TunnelRelayError';
    this.status = status;
    this.errorCode = errorCode;
  }
}

/**
 * tunnelFetch — wraps a plain RPC request in Noise AEAD, posts it through
 * the relay's encrypted tunnel, and decrypts the response.
 *
 * Wire format (plaintext before encryption):
 *   JSON of { method, path, headers, body? }
 *   where body is a base64 string.  Go's []byte JSON field auto-decodes base64,
 *   so both sides use the same "body" field name.
 *
 * Relay endpoint: POST /d/:desktopId/rpc
 *   Request body:  { encrypted: true, body: base64(ciphertext) }
 *   Response body: raw ciphertext bytes (NOT a JSON wrapper)
 *     — the relay does io.Copy(stream → response) so the body is
 *       the desktop's encrypted blob verbatim.
 */
export async function tunnelFetch(
  relayBaseUrl: string,
  desktopId: string,
  req: TunnelRequest,
  cipher: CipherPair,
  deviceToken: string,
  edPriv: Uint8Array,
): Promise<TunnelResponse> {
  const deviceTokenBytes = new TextEncoder().encode(deviceToken);
  const dpopProof = buildDPoPHeader(edPriv, deviceTokenBytes, 'POST', `/d/${desktopId}/rpc`);

  // Serialize the inner request.
  // The "body" field carries a base64 string; Go's json.Unmarshal automatically
  // decodes base64 into []byte when the target field is typed []byte.
  // Normalise headers to Record<string, string[]> to match Go's map[string][]string.
  // Multi-value headers (e.g. Set-Cookie) are preserved; single-string values are
  // wrapped into a one-element array.
  const normHeaders: Record<string, string[]> = {};
  for (const [k, v] of Object.entries(req.headers ?? {})) {
    normHeaders[k] = Array.isArray(v) ? v : [v];
  }

  const innerBody: {
    method: string;
    path: string;
    headers: Record<string, string[]>;
    body?: string;
  } = {
    method: req.method.toUpperCase(),
    path: req.path,
    headers: normHeaders,
  };

  if (req.body != null) {
    const bodyBytes =
      req.body instanceof Uint8Array
        ? req.body
        : new TextEncoder().encode(req.body);
    innerBody.body = b64Encode(bodyBytes);
  }

  const plaintext = new TextEncoder().encode(JSON.stringify(innerBody));
  const ciphertext = cipher.encrypt(plaintext);

  const outerRes = await fetchOrThrow(`${relayBaseUrl}/d/${desktopId}/rpc`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${deviceToken}`,
      'X-DPoP': dpopProof,
      'X-Idempotency-Key': generateIdempotencyKey(),
    },
    body: JSON.stringify({
      encrypted: true,
      body: b64Encode(ciphertext),
    }),
  });

  if (!outerRes.ok) {
    // Read body as text first. When the desktop's encrypted_rpc handler hits
    // a writeErr branch (decrypt fail / unmarshal fail / build_request fail /
    // read_ciphertext fail) the relay forwards the desktop's plain-text
    // "relayclient: <msg>" body verbatim and surfaces the desktop's status as
    // the outer HTTP status. JSON.parse on that plaintext silently swallows
    // the only diagnostic we have, leaving the user staring at "400:".
    const rawBody = await outerRes.text().catch(() => '');
    let parsed: { error?: string | { message?: string }; last_seen_at?: number } = {};
    try {
      parsed = rawBody ? JSON.parse(rawBody) : {};
    } catch {
      // body is not JSON — fall through to rawBody below
    }
    const lastSeenAt = parsed?.last_seen_at ?? null;
    const lastSeenMsg =
      lastSeenAt != null
        ? ` (desktop last seen ${new Date(lastSeenAt * 1000).toLocaleString()})`
        : '';
    const errorCode = outerRes.headers.get('X-Niuniu-Error');
    const structured =
      typeof parsed?.error === 'object'
        ? parsed.error?.message
        : parsed?.error;
    const detail = structured || rawBody.trim() || outerRes.statusText;
    throw new TunnelRelayError(
      outerRes.status,
      errorCode,
      `tunnelFetch: relay error ${outerRes.status}: ${detail}${lastSeenMsg}`,
    );
  }

  // The relay writes the desktop's raw ciphertext bytes via io.Copy — no JSON wrapper.
  // Read the full response body as an ArrayBuffer and decrypt directly.
  const rawBuf = await outerRes.arrayBuffer();
  const encryptedResponse = new Uint8Array(rawBuf);
  if (encryptedResponse.length === 0) {
    throw new Error('tunnelFetch: empty response body from relay');
  }

  const decryptedBytes = cipher.decrypt(encryptedResponse);
  const responseText = new TextDecoder().decode(decryptedBytes);

  // Inner response shape: { status, headers, body }
  // headers is map[string][]string on the Go side, so each value is string[].
  const inner: {
    status: number;
    headers?: Record<string, string | string[]>;
    body?: string;
  } = JSON.parse(responseText);

  // Normalise response headers to Record<string, string[]> for consistency.
  const respHeaders: Record<string, string[]> = {};
  for (const [k, v] of Object.entries(inner.headers ?? {})) {
    respHeaders[k] = Array.isArray(v) ? v : [v];
  }

  return {
    status: inner.status,
    headers: respHeaders,
    body: inner.body ?? '',
    ok: inner.status >= 200 && inner.status < 300,
  };
}
