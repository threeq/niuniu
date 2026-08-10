import { HKDF } from '@stablelib/hkdf';
import { SHA256 } from '@stablelib/sha256';

/**
 * HKDF-SHA256 wrapper.
 * @param ikm  Input key material
 * @param salt Salt bytes (may be empty; spec treats empty as zeroes)
 * @param info Context / info string or bytes
 * @param length Number of output bytes
 */
export function hkdfSHA256(
  ikm: Uint8Array,
  salt: Uint8Array | undefined,
  info: Uint8Array | string,
  length: number,
): Uint8Array {
  const infoBytes =
    typeof info === 'string' ? new TextEncoder().encode(info) : info;
  const hkdf = new HKDF(SHA256, ikm, salt ?? new Uint8Array(0), infoBytes);
  return hkdf.expand(length);
}

/**
 * Expand HKDF into `count` equal-length chunks of `chunkSize` bytes each.
 */
export function hkdfExpand(
  ikm: Uint8Array,
  salt: Uint8Array | undefined,
  info: Uint8Array | string,
  count: number,
  chunkSize = 32,
): Uint8Array[] {
  const total = hkdfSHA256(ikm, salt, info, count * chunkSize);
  const out: Uint8Array[] = [];
  for (let i = 0; i < count; i++) {
    out.push(total.slice(i * chunkSize, (i + 1) * chunkSize));
  }
  return out;
}
