import { hkdfSHA256 } from './hkdf';

/**
 * threeChoicePicker — given the correct 6-digit SAS code, returns a shuffled
 * array of three choices (correct + 2 random decoys) and the index of the
 * correct one.
 *
 * Uses Math.random() — suitable only for UI display, not for cryptographic
 * operations (the SAS itself is computed via deriveSAS which is crypto-grade).
 */
export function threeChoicePicker(correct: string): { choices: string[]; correctIndex: number } {
  if (correct.length !== 6) throw new Error('correct must be 6 digits');
  const choices = new Set<string>([correct]);
  while (choices.size < 3) {
    const decoy = String(Math.floor(Math.random() * 1_000_000)).padStart(6, '0');
    choices.add(decoy);
  }
  // Fisher-Yates shuffle
  const arr = Array.from(choices);
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [arr[i], arr[j]] = [arr[j], arr[i]];
  }
  return { choices: arr, correctIndex: arr.indexOf(correct) };
}

/**
 * Encode a uint16 big-endian into 2 bytes.
 */
function uint16BE(n: number): Uint8Array {
  return new Uint8Array([(n >> 8) & 0xff, n & 0xff]);
}

/**
 * Append a length-prefixed field to a growing byte array.
 * Layout: uint16_be(len) || bytes
 */
function appendLenPrefixed(dst: number[], field: Uint8Array): void {
  const lb = uint16BE(field.length);
  dst.push(...lb);
  dst.push(...field);
}

/**
 * deriveSAS computes the 6-digit SAS verification code.
 *
 * Canonical byte layout:
 *   "niuniu-pair/v1" ||
 *   uint16_be(len(nonce))     || nonce ||
 *   uint16_be(len(desktop_x)) || desktop_x ||
 *   uint16_be(len(mobile_x))  || mobile_x ||
 *   uint16_be(len(desktop_s)) || desktop_s ||
 *   uint16_be(len(mobile_s))  || mobile_s
 *
 * HKDF-SHA256(salt=empty, ikm=input, info="niuniu-sas-v1", length=32).
 * First 4 bytes as big-endian uint32, mod 1_000_000, zero-padded to 6 digits.
 */
export function deriveSAS(
  nonce: Uint8Array,
  desktopXpub: Uint8Array,
  mobileXpub: Uint8Array,
  desktopSignPub: Uint8Array,
  mobileSignPub: Uint8Array,
): string {
  const prefix = new TextEncoder().encode('niuniu-pair/v1');
  const buf: number[] = [...prefix];

  appendLenPrefixed(buf, nonce);
  appendLenPrefixed(buf, desktopXpub);
  appendLenPrefixed(buf, mobileXpub);
  appendLenPrefixed(buf, desktopSignPub);
  appendLenPrefixed(buf, mobileSignPub);

  const ikm = new Uint8Array(buf);
  const out = hkdfSHA256(ikm, new Uint8Array(0), 'niuniu-sas-v1', 32);

  // First 4 bytes as big-endian uint32
  const code =
    ((out[0] << 24) >>> 0) +
    ((out[1] << 16) >>> 0) +
    ((out[2] << 8) >>> 0) +
    (out[3] >>> 0);

  return String(code % 1_000_000).padStart(6, '0');
}
