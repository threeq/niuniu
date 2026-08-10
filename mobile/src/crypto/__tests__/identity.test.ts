/**
 * Identity tests — mocks expo-secure-store via jest.config.js moduleNameMapper
 */

// Mutable store shared within jest.mock factory — reset via clearStore()
const mockStore: Record<string, string> = {};
const clearStore = () => { Object.keys(mockStore).forEach(k => delete mockStore[k]); };

jest.mock('expo-secure-store', () => ({
  getItemAsync: jest.fn((key: string) => Promise.resolve(mockStore[key] ?? null)),
  setItemAsync: jest.fn((key: string, value: string) => {
    mockStore[key] = value;
    return Promise.resolve();
  }),
  deleteItemAsync: jest.fn((key: string) => {
    delete mockStore[key];
    return Promise.resolve();
  }),
}));

import { generateIdentity, saveIdentity, getIdentity, getOrCreateIdentity } from '../identity';

describe('Identity', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    clearStore();
  });

  it('generateIdentity produces valid key lengths', () => {
    const id = generateIdentity();
    expect(id.xPriv.length).toBe(32);
    expect(id.xPub.length).toBe(32);
    // ed25519 key lengths: private = 64, public = 32
    expect(id.edPub.length).toBe(32);
    expect(id.edPriv.length).toBeGreaterThanOrEqual(32); // 64 for stablelib
  });

  it('two generateIdentity calls produce different keys', () => {
    const a = generateIdentity();
    const b = generateIdentity();
    expect(Buffer.from(a.xPub).toString('hex')).not.toBe(
      Buffer.from(b.xPub).toString('hex'),
    );
  });

  it('saveIdentity + getIdentity round-trips', async () => {
    const id = generateIdentity();
    await saveIdentity(id);
    const loaded = await getIdentity();
    expect(loaded).not.toBeNull();
    expect(Buffer.from(loaded!.xPub).toString('hex')).toBe(
      Buffer.from(id.xPub).toString('hex'),
    );
    expect(Buffer.from(loaded!.xPriv).toString('hex')).toBe(
      Buffer.from(id.xPriv).toString('hex'),
    );
    expect(Buffer.from(loaded!.edPub).toString('hex')).toBe(
      Buffer.from(id.edPub).toString('hex'),
    );
    expect(Buffer.from(loaded!.edPriv).toString('hex')).toBe(
      Buffer.from(id.edPriv).toString('hex'),
    );
  });

  it('getIdentity returns null when no keys stored', async () => {
    const result = await getIdentity();
    expect(result).toBeNull();
  });

  it('getOrCreateIdentity generates and persists identity when none exists', async () => {
    const id1 = await getOrCreateIdentity();
    expect(id1.xPub.length).toBe(32);
  });

  it('getOrCreateIdentity returns same identity on second call', async () => {
    const id1 = await getOrCreateIdentity();
    const id2 = await getOrCreateIdentity();
    expect(Buffer.from(id1.xPub).toString('hex')).toBe(
      Buffer.from(id2.xPub).toString('hex'),
    );
  });

  it('getIdentity derives pubs from privs even when stored pubs are stale', async () => {
    // Seed the store with real priv keys from one generation, but pub keys
    // from a different generation — the drift that causes Noise KK MAC failure.
    const genA = generateIdentity();
    const genB = generateIdentity();
    const { encode: b64 } = await import('@stablelib/base64');
    mockStore['niuniu.x-priv'] = b64(genA.xPriv);
    mockStore['niuniu.x-pub'] = b64(genB.xPub); // stale!
    mockStore['niuniu.ed-priv'] = b64(genA.edPriv);
    mockStore['niuniu.ed-pub'] = b64(genB.edPub); // stale!

    const loaded = await getIdentity();
    expect(loaded).not.toBeNull();
    // Derived pubs must match genA (the priv's actual generation), not genB.
    expect(Buffer.from(loaded!.xPub).toString('hex')).toBe(
      Buffer.from(genA.xPub).toString('hex'),
    );
    expect(Buffer.from(loaded!.edPub).toString('hex')).toBe(
      Buffer.from(genA.edPub).toString('hex'),
    );
  });
});
