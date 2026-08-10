import { encode as b64Encode } from '@stablelib/base64';
import { buildDPoPHeader } from '../../crypto/dpop';
import { generateIdempotencyKey } from '../../utils/idempotency';
import { fetchOrThrow } from '../fetchDiag';
import {
  TunnelRelayError,
  type TunnelRequest,
  type TunnelResponse,
} from './tunnelFetch';

/**
 * plainTunnelFetch — sends a request through the relay WITHOUT Noise
 * encryption.  Uses the relay's existing `encrypted: false` envelope mode,
 * which forwards the request to the desktop's `handleHTTP` path (the
 * default `case` in `server/internal/relayclient/proxy.go`).
 *
 * Why this exists:  the legacy Noise path uses a single `pairingcrypto.
 * CipherPair` with a monotonic nonce counter; the slightest concurrency
 * or stream-reordering on the desktop side flips it permanently invalid
 * with "chacha20poly1305: message authentication failed", which then
 * poisons every subsequent RPC. The pairkey transport (random per-message
 * nonce) is the long-term replacement; until every paired desktop has a
 * provisioned pair_key, plain serves as the apiClient fallback path so
 * users on older pairings keep working without a re-pair gate. Force it
 * for everyone with `RELAY_TRANSPORT_MODE = 'plain'`.
 *
 * Trust model: the relay sees full request/response payloads in this
 * mode. (pairkey is no different in this respect — the relay holds the
 * pair_key and could decrypt; only legacy Noise KK was end-to-end against
 * the relay operator.) The wire is still TLS-protected (HTTPS to the
 * relay, WSS from relay to desktop), so external observers see nothing
 * extra; only the relay operator does. Acceptable when you operate the
 * relay yourself.
 *
 * Relay endpoint: POST /d/:desktopId/rpc
 *   Request body:  { encrypted: false, method, path, headers, body }
 *                  — Go's []byte JSON field auto-decodes the body's
 *                    base64 string, so it survives a JSON round-trip.
 *   Response:      the local server's HTTP response, status + headers +
 *                  body propagated verbatim by relay+desktop.
 */
export async function plainTunnelFetch(
  relayBaseUrl: string,
  desktopId: string,
  req: TunnelRequest,
  deviceToken: string,
  edPriv: Uint8Array,
): Promise<TunnelResponse> {
  const deviceTokenBytes = new TextEncoder().encode(deviceToken);
  const dpopProof = buildDPoPHeader(
    edPriv,
    deviceTokenBytes,
    'POST',
    `/d/${desktopId}/rpc`,
  );

  // Normalise headers to Record<string, string[]> to match Go's
  // map[string][]string.
  const normHeaders: Record<string, string[]> = {};
  for (const [k, v] of Object.entries(req.headers ?? {})) {
    normHeaders[k] = Array.isArray(v) ? v : [v];
  }

  const bodyBytes =
    req.body == null
      ? new Uint8Array(0)
      : req.body instanceof Uint8Array
        ? req.body
        : new TextEncoder().encode(req.body);

  // Send both `cipher_kind: "plain"` (symmetric with pairKeyFetch's
  // `cipher_kind: "pairkey"`) and the legacy `encrypted: false` flag.
  // Modern relays read `CipherKind` first; relays that predate the
  // `cipher_kind` field silently ignore the unknown JSON key and fall
  // through to the `encrypted` bool — both paths route to the
  // plaintext branch.
  const envelope = {
    cipher_kind: 'plain',
    encrypted: false,
    method: req.method.toUpperCase(),
    path: req.path,
    headers: normHeaders,
    body: b64Encode(bodyBytes),
  };

  const outerRes = await fetchOrThrow(`${relayBaseUrl}/d/${desktopId}/rpc`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${deviceToken}`,
      'X-DPoP': dpopProof,
      'X-Idempotency-Key': generateIdempotencyKey(),
    },
    body: JSON.stringify(envelope),
  });

  // The plaintext path makes the relay forward the desktop's local-server
  // HTTP response body verbatim (no length-prefixed status header, no
  // ciphertext wrapping). So `outerRes.status` is whatever the local
  // server returned (or a relay-level error: 503 DesktopOffline, 401
  // bad_token, etc.). To distinguish relay-level failures from local
  // 4xx/5xx the caller can check `X-Niuniu-Error` headers and the body
  // shape — relay errors are JSON `{"error":"<code>"}`, local responses
  // are whatever the local handler returned.
  const rawBody = await outerRes.text().catch(() => '');

  const respHeaders: Record<string, string[]> = {};
  outerRes.headers.forEach((value, key) => {
    if (!respHeaders[key]) respHeaders[key] = [];
    respHeaders[key].push(value);
  });

  // For relay-level failures (4xx/5xx where the relay rejected before
  // ever reaching the desktop's local server) surface a TunnelRelayError
  // so callers can tell infrastructure failure apart from a plain 4xx
  // returned by their backend. We use the X-Niuniu-Error header as the
  // disambiguator: only relay-level rejections set it (handlers in
  // `relay/internal/api/proxy_handler.go` write it for offline / no
  // session / bandwidth / quota), local server responses don't.
  const errorCode = outerRes.headers.get('X-Niuniu-Error');
  if (!outerRes.ok && errorCode) {
    let parsed: { error?: string | { message?: string } } = {};
    try {
      parsed = rawBody ? JSON.parse(rawBody) : {};
    } catch {
      // body might be plain text — fine, leave parsed empty
    }
    const detail =
      typeof parsed?.error === 'object'
        ? parsed.error?.message
        : parsed?.error;
    throw new TunnelRelayError(
      outerRes.status,
      errorCode,
      `plainTunnelFetch: relay error ${outerRes.status}: ${detail ?? rawBody.trim() ?? outerRes.statusText}`,
    );
  }

  return {
    status: outerRes.status,
    headers: respHeaders,
    body: rawBody,
    ok: outerRes.ok,
  };
}
