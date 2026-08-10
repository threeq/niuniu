/**
 * generateIdempotencyKey — returns a RFC 4122 v4 UUID string suitable for use
 * as an X-Idempotency-Key header value.
 *
 * Prefers the native `crypto.randomUUID()` (available in React Native >= 0.73
 * and modern browsers) and falls back to a Math.random-based implementation
 * that satisfies the v4 UUID shape without requiring any native module.
 */
export function generateIdempotencyKey(): string {
  // Native path: available in React Native ≥ 0.73 / Hermes ≥ 0.12.
  if (
    typeof globalThis !== 'undefined' &&
    typeof (globalThis as { crypto?: { randomUUID?: () => string } }).crypto
      ?.randomUUID === 'function'
  ) {
    return (globalThis as { crypto: { randomUUID: () => string } }).crypto.randomUUID();
  }
  // Fallback: Math.random-based RFC 4122 v4 UUID.
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}
