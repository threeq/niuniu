import { fingerprintXpub } from '../fingerprint';

/**
 * Interop test vectors derived from Go FingerprintXpub in
 * go-shared/lanproto/discovery.go.
 *
 * FingerprintXpub(xpub) = hex(sha256(xpub)[:8])  →  16 lowercase hex chars.
 *
 * Vector computed with:
 *   node -e "const {createHash}=require('crypto'); \
 *     const b=Buffer.alloc(32,0xAB); \
 *     console.log(createHash('sha256').update(b).digest().slice(0,8).toString('hex'))"
 *   → 9a2db2e23f1504cd
 */

describe('fingerprintXpub', () => {
  it('produces 16-character lowercase hex string', () => {
    const xpub = new Uint8Array(32).fill(0x00);
    const fp = fingerprintXpub(xpub);
    expect(fp).toHaveLength(16);
    expect(fp).toMatch(/^[0-9a-f]+$/);
  });

  it('matches Go FingerprintXpub for 32-byte all-0xAB vector', () => {
    // Canonical vector: sha256(0xAB * 32)[:8] = 9a2db2e23f1504cd
    const xpub = new Uint8Array(32).fill(0xab);
    expect(fingerprintXpub(xpub)).toBe('9a2db2e23f1504cd');
  });

  it('matches Go FingerprintXpub for 32-byte all-zeros vector', () => {
    // sha256(0x00 * 32)[:8] = 66687aadf862bd77
    const xpub = new Uint8Array(32).fill(0x00);
    expect(fingerprintXpub(xpub)).toBe('66687aadf862bd77');
  });

  it('matches Go FingerprintXpub for 32-byte all-0xFF vector', () => {
    // sha256(0xFF * 32)[:8] = af9613760f72635f
    const xpub = new Uint8Array(32).fill(0xff);
    expect(fingerprintXpub(xpub)).toBe('af9613760f72635f');
  });

  it('different inputs produce different fingerprints', () => {
    const a = new Uint8Array(32).fill(0x01);
    const b = new Uint8Array(32).fill(0x02);
    expect(fingerprintXpub(a)).not.toBe(fingerprintXpub(b));
  });

  it('is deterministic', () => {
    const xpub = new Uint8Array(32).fill(0x42);
    expect(fingerprintXpub(xpub)).toBe(fingerprintXpub(xpub));
  });
});
