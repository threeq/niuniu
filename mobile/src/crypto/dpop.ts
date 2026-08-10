import { sign } from '@stablelib/ed25519';
import { encode as b64Encode } from '@stablelib/base64';
import { HMAC } from '@stablelib/hmac';
import { SHA256 } from '@stablelib/sha256';

/**
 * Encode bytes as URL-safe base64 without padding.
 */
function b64url(bytes: Uint8Array): string {
  return b64Encode(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

/**
 * signDPoP creates a DPoP proof token per spec §7.4.
 *
 * sig_input = Ed25519.Sign(sk_mobile, HMAC-SHA256(deviceTokenPlain, method+" "+path+" "+unixSeconds))
 *
 * The HMAC key is the plaintext device token — known to the mobile that holds it
 * and re-derivable by the server from the presented Bearer token.
 *
 * Returns: base64url(signature) + "." + timestamp
 */
export function signDPoP(
  edPriv: Uint8Array,
  deviceTokenPlain: Uint8Array,
  method: string,
  path: string,
  unixSeconds: number,
): string {
  const macIn = new TextEncoder().encode(`${method} ${path} ${unixSeconds}`);
  const hmac = new HMAC(SHA256, deviceTokenPlain);
  hmac.update(macIn);
  const digest = hmac.digest();
  const sig = sign(edPriv, digest);
  return `${b64url(sig)}.${unixSeconds}`;
}

/**
 * Build DPoP header value for use in requests.
 */
export function buildDPoPHeader(
  edPriv: Uint8Array,
  deviceTokenPlain: Uint8Array,
  method: string,
  path: string,
): string {
  const ts = Math.floor(Date.now() / 1000);
  return signDPoP(edPriv, deviceTokenPlain, method, path, ts);
}
