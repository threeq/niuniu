import { generateIdempotencyKey } from '../idempotency';

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

describe('generateIdempotencyKey', () => {
  it('returns a valid RFC 4122 v4 UUID string', () => {
    const key = generateIdempotencyKey();
    expect(typeof key).toBe('string');
    expect(key).toMatch(UUID_PATTERN);
  });

  it('returns a different key on each call', () => {
    const keys = new Set(Array.from({ length: 50 }, () => generateIdempotencyKey()));
    // All 50 should be unique (collision probability ≈ 0 for v4 UUIDs).
    expect(keys.size).toBe(50);
  });
});
