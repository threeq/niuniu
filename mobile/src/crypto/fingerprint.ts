/**
 * fingerprintXpub — mirrors Go FingerprintXpub from go-shared/lanproto/discovery.go
 *
 * Returns hex(sha256(xpub))[:16], i.e. the first 8 bytes as a 16-character hex string.
 * Must match the desktop's `FingerprintXpub` for identity verification over LAN.
 */

import { hash } from '@stablelib/sha256';

/**
 * Computes the xpub fingerprint: first 8 bytes of SHA-256(xpub) as lowercase hex.
 *
 * This is the canonical fingerprint used in mDNS TXT `ifp` and `/.well-known/niuniu-identity`
 * `xpub_fingerprint` fields. Must match Go FingerprintXpub exactly.
 */
export function fingerprintXpub(xpub: Uint8Array): string {
  const h = hash(xpub);
  return Array.from(h.slice(0, 8))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}
