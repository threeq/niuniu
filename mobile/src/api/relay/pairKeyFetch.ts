import { ChaCha20Poly1305 } from '@stablelib/chacha20poly1305';
import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';
import { buildDPoPHeader } from '../../crypto/dpop';
import { generateIdempotencyKey } from '../../utils/idempotency';
import { fetchOrThrow } from '../fetchDiag';
import {
  TunnelRelayError,
  type TunnelRequest,
  type TunnelResponse,
} from './tunnelFetch';

/**
 * pairKeyFetch — pairkey transport. Uses a stable per-pair 32-byte
 * ChaCha20-Poly1305 key (provisioned at scan-pairing time, stored on the
 * relay's desktop_mobile_links row, returned to the mobile in the claim
 * long-poll response) plus a fresh random 96-bit nonce per request.
 *
 * Only the request/response bodies are encrypted; method, path, and
 * headers travel in the cleartext outer envelope (the relay needs them
 * for routing and we already trust the relay with that metadata in the
 * plaintext mode). AAD binds the ciphertext to (mobile_id, method, path)
 * so a replay against a different route or a different pair fails AEAD
 * verification.
 *
 * Each frame is independent — random nonces, no shared counter — so
 * concurrent and out-of-order RPCs don't poison cipher state. This is
 * the design fix for the 2026-05-02 incident where the legacy Noise KK
 * transport's monotonic-counter CipherPair flipped to permanently
 * invalid on the first out-of-order decrypt.
 *
 * Wire format:
 *   POST /d/:desktopId/rpc
 *   {
 *     cipher_kind: "pairkey",
 *     method, path, headers,
 *     body: <base64(nonce(12) || ciphertext)>
 *   }
 *   Response body: raw bytes `nonce(12) || ciphertext` (no JSON wrapper);
 *   outer HTTP status + headers carry the local server's response status.
 */
export async function pairKeyFetch(
  relayBaseUrl: string,
  desktopId: string,
  mobileId: string,
  pairKey: Uint8Array,
  req: TunnelRequest,
  deviceToken: string,
  edPriv: Uint8Array,
): Promise<TunnelResponse> {
  if (pairKey.length !== 32) {
    throw new Error(
      `pairKeyFetch: pair_key must be 32 bytes (got ${pairKey.length})`,
    );
  }
  const deviceTokenBytes = new TextEncoder().encode(deviceToken);
  const dpopProof = buildDPoPHeader(
    edPriv,
    deviceTokenBytes,
    'POST',
    `/d/${desktopId}/rpc`,
  );

  const method = req.method.toUpperCase();
  const normHeaders: Record<string, string[]> = {};
  for (const [k, v] of Object.entries(req.headers ?? {})) {
    normHeaders[k] = Array.isArray(v) ? v : [v];
  }

  // Encrypt only the request body bytes. Empty bodies are legal — the
  // AEAD tag (16 bytes) still anchors AAD so a tampered (mobile, method,
  // path) tuple fails verification.
  const bodyBytes =
    req.body == null
      ? new Uint8Array(0)
      : req.body instanceof Uint8Array
        ? req.body
        : new TextEncoder().encode(req.body);

  const aead = new ChaCha20Poly1305(pairKey);
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const reqAD = pairKeyAAD(mobileId, method, req.path, /*response*/ false);
  const ciphertext = aead.seal(nonce, bodyBytes, reqAD);

  const wireBody = new Uint8Array(nonce.length + ciphertext.length);
  wireBody.set(nonce, 0);
  wireBody.set(ciphertext, nonce.length);

  const outerRes = await fetchOrThrow(`${relayBaseUrl}/d/${desktopId}/rpc`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${deviceToken}`,
      'X-DPoP': dpopProof,
      'X-Idempotency-Key': generateIdempotencyKey(),
    },
    body: JSON.stringify({
      cipher_kind: 'pairkey',
      method,
      path: req.path,
      headers: normHeaders,
      body: b64Encode(wireBody),
    }),
  });

  // Disambiguate three response shapes before attempting AEAD decrypt:
  //
  //   1. Relay-level error  — `X-Niuniu-Error` set by proxy_handler.RPC
  //      (e.g. pair_key_unprovisioned, DesktopOffline). Body is JSON
  //      `{"error": "<code>"}`.
  //   2. Desktop-side error — `X-Niuniu-Desktop-Error` set by handlePairKeyRPC
  //      writePKErr (e.g. decrypt_failed, build_request_failed). Body is
  //      plain text "relayclient: <msg>". MUST short-circuit: the body is
  //      NOT a pairkey envelope, so attempting AEAD decrypt would fail
  //      with a generic "decrypt failed" that hides the original reason.
  //   3. Local-server response — neither header set. Body is `nonce(12) ||
  //      ciphertext`; outer status mirrors the local server's status.
  const relayErrorCode = outerRes.headers.get('X-Niuniu-Error');
  const desktopErrorCode = outerRes.headers.get('X-Niuniu-Desktop-Error');
  if (relayErrorCode || desktopErrorCode) {
    const rawBody = await outerRes.text().catch(() => '');
    let parsed: { error?: string | { message?: string }; last_seen_at?: number } = {};
    try {
      parsed = rawBody ? JSON.parse(rawBody) : {};
    } catch {
      // desktop errors are plain text — leave parsed empty
    }
    const structured =
      typeof parsed?.error === 'object' ? parsed.error?.message : parsed?.error;
    const detail = structured || rawBody.trim() || outerRes.statusText;
    const lastSeenAt = parsed?.last_seen_at ?? null;
    const lastSeenMsg =
      lastSeenAt != null
        ? ` (desktop last seen ${new Date(lastSeenAt * 1000).toLocaleString()})`
        : '';
    const source = relayErrorCode ? 'relay' : 'desktop';
    throw new TunnelRelayError(
      outerRes.status,
      relayErrorCode ?? desktopErrorCode,
      `pairKeyFetch: ${source} error ${outerRes.status}: ${detail}${lastSeenMsg}`,
    );
  }

  // Outer body is `nonce(12) || ciphertext(of response body)`. AEAD tag is
  // the trailing 16 bytes; an empty plaintext is legal so the floor is
  // 12 + 16 = 28 bytes.
  const rawBuf = await outerRes.arrayBuffer();
  const respBytes = new Uint8Array(rawBuf);
  if (respBytes.length < 12 + 16) {
    throw new Error(
      `pairKeyFetch: response too short (${respBytes.length} bytes)`,
    );
  }
  const respNonce = respBytes.slice(0, 12);
  const respCiphertext = respBytes.slice(12);
  const respAD = pairKeyAAD(mobileId, method, req.path, /*response*/ true);
  const respPlain = aead.open(respNonce, respCiphertext, respAD);
  if (!respPlain) {
    throw new Error('pairKeyFetch: AEAD decrypt failed on response');
  }

  // Headers come from the outer response (relay propagated the local
  // server's headers verbatim).
  const respHeaders: Record<string, string[]> = {};
  outerRes.headers.forEach((value, key) => {
    if (!respHeaders[key]) respHeaders[key] = [];
    respHeaders[key].push(value);
  });

  return {
    status: outerRes.status,
    headers: respHeaders,
    body: new TextDecoder().decode(respPlain),
    ok: outerRes.ok,
  };
}

// pairKeyAAD constructs the AEAD associated data — must byte-match
// server/internal/relayclient/pairkey.go:pairKeyAAD.
const PAIRKEY_AAD_REQ_LABEL = 'pairkey-rpc/v1';
const PAIRKEY_AAD_RESP_LABEL = 'pairkey-rpc-resp/v1';
const PAIRKEY_AAD_SEP = 0x1f;

function pairKeyAAD(
  mobileId: string,
  method: string,
  path: string,
  response: boolean,
): Uint8Array {
  const enc = new TextEncoder();
  const label = enc.encode(
    response ? PAIRKEY_AAD_RESP_LABEL : PAIRKEY_AAD_REQ_LABEL,
  );
  const m = enc.encode(mobileId);
  const meth = enc.encode(method);
  const p = enc.encode(path);
  const out = new Uint8Array(label.length + 1 + m.length + 1 + meth.length + 1 + p.length);
  let o = 0;
  out.set(label, o); o += label.length;
  out[o++] = PAIRKEY_AAD_SEP;
  out.set(m, o); o += m.length;
  out[o++] = PAIRKEY_AAD_SEP;
  out.set(meth, o); o += meth.length;
  out[o++] = PAIRKEY_AAD_SEP;
  out.set(p, o);
  return out;
}

/**
 * Test-only export: lets specs assert byte-for-byte parity with the Go
 * implementation in `server/internal/relayclient/pairkey.go`. Production
 * code must not import this name.
 */
export function __pairKeyAADForTesting(
  mobileId: string,
  method: string,
  path: string,
  response: boolean,
): Uint8Array {
  return pairKeyAAD(mobileId, method, path, response);
}

/**
 * decodePairKey — base64-decode a pairKey string from the store, validating
 * that it's exactly 32 bytes. Callers should treat any other length as
 * "no key provisioned" and fall back to the legacy transport.
 */
export function decodePairKey(b64: string | undefined): Uint8Array | null {
  if (!b64) return null;
  let bytes: Uint8Array;
  try {
    bytes = b64Decode(b64);
  } catch {
    return null;
  }
  if (bytes.length !== 32) return null;
  return bytes;
}
