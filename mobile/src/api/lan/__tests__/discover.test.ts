import { probeEndpoint, findLanEndpoint, IdentityResponse } from '../discover';
import type { PairedDesktop } from '../../../stores/pairedDesktopsStore';

// ─── Helpers ──────────────────────────────────────────────────────────────────

/** Build a minimal PairedDesktop for testing. xpub = 32 bytes of 0xAB (base64). */
function makeDesktop(overrides: Partial<PairedDesktop> = {}): PairedDesktop {
  // 32 bytes of 0xAB base64-encoded:
  // Buffer.alloc(32, 0xAB).toString('base64') = 'q6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6s='
  const xpub = 'q6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6s=';
  // fingerprintXpub(0xAB * 32) = '9a2db2e23f1504cd'
  return {
    desktopId: 'desktop-test-id',
    desktopName: 'Test Desktop',
    xpub,
    signPub: 'signpubbase64',
    relayDeviceToken: 'token',
    pairedAt: '2026-01-01T00:00:00Z',
    lastKnownLanEndpoints: [{ host: '192.168.1.100', port: 9988, lastSeen: Date.now() }],
    ...overrides,
  };
}

const MATCHING_IDENTITY: IdentityResponse = {
  version: 1,
  desktop_id: 'desktop-test-id',
  xpub_fingerprint: '9a2db2e23f1504cd',
};

// ─── probeEndpoint ────────────────────────────────────────────────────────────

describe('probeEndpoint', () => {
  beforeEach(() => {
    (global.fetch as jest.Mock).mockReset?.();
    global.fetch = jest.fn();
  });

  it('returns identity when server responds 200 with valid body', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      json: async () => MATCHING_IDENTITY,
    });

    const result = await probeEndpoint('192.168.1.100', 9988);
    expect(result).toEqual(MATCHING_IDENTITY);
    expect(global.fetch).toHaveBeenCalledWith(
      'http://192.168.1.100:9988/.well-known/niuniu-identity',
      expect.objectContaining({ signal: expect.anything() }),
    );
  });

  it('returns null when server responds with non-200 status', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({ ok: false, status: 404 });

    const result = await probeEndpoint('192.168.1.100', 9988);
    expect(result).toBeNull();
  });

  it('returns null when version is not 1', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      json: async () => ({ version: 2, desktop_id: 'x', xpub_fingerprint: 'y' }),
    });

    const result = await probeEndpoint('192.168.1.100', 9988);
    expect(result).toBeNull();
  });

  it('returns null when fetch throws (network error)', async () => {
    (global.fetch as jest.Mock).mockRejectedValue(new Error('Network failure'));

    const result = await probeEndpoint('192.168.1.100', 9988);
    expect(result).toBeNull();
  });

  it('returns null when request is aborted (timeout)', async () => {
    (global.fetch as jest.Mock).mockImplementation(
      () => new Promise((_, reject) => setTimeout(() => reject(new DOMException('Aborted', 'AbortError')), 50)),
    );

    const result = await probeEndpoint('192.168.1.100', 9988, 10);
    expect(result).toBeNull();
  });
});

// ─── findLanEndpoint ──────────────────────────────────────────────────────────

describe('findLanEndpoint', () => {
  beforeEach(() => {
    global.fetch = jest.fn();
  });

  afterEach(() => {
    jest.resetAllMocks();
  });

  it('returns endpoint when fingerprint and desktopId match', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      json: async () => MATCHING_IDENTITY,
    });

    const desktop = makeDesktop();
    const result = await findLanEndpoint(desktop, 3000);
    expect(result).toEqual({ host: '192.168.1.100', port: 9988 });
  });

  it('returns null when fingerprint does not match', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      json: async () => ({
        version: 1,
        desktop_id: 'desktop-test-id',
        xpub_fingerprint: 'wrongfingerprint',
      }),
    });

    const desktop = makeDesktop();
    const result = await findLanEndpoint(desktop, 3000);
    expect(result).toBeNull();
  });

  it('returns null when desktopId does not match', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      json: async () => ({
        version: 1,
        desktop_id: 'some-other-desktop',
        xpub_fingerprint: '9a2db2e23f1504cd',
      }),
    });

    const desktop = makeDesktop();
    const result = await findLanEndpoint(desktop, 3000);
    expect(result).toBeNull();
  });

  it('returns null when all endpoints return 404', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({ ok: false, status: 404 });

    const desktop = makeDesktop();
    const result = await findLanEndpoint(desktop, 3000);
    expect(result).toBeNull();
  });

  it('returns null when no cached endpoints exist', async () => {
    const desktop = makeDesktop({ lastKnownLanEndpoints: [] });
    const result = await findLanEndpoint(desktop, 3000);
    expect(result).toBeNull();
    expect(global.fetch).not.toHaveBeenCalled();
  });

  it('returns null when lastKnownLanEndpoints is undefined', async () => {
    const desktop = makeDesktop({ lastKnownLanEndpoints: undefined });
    const result = await findLanEndpoint(desktop, 3000);
    expect(result).toBeNull();
  });

  it('returns first matching endpoint when multiple candidates exist', async () => {
    // Second endpoint matches; first fails
    (global.fetch as jest.Mock)
      .mockResolvedValueOnce({ ok: false, status: 404 })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => MATCHING_IDENTITY,
      });

    const desktop = makeDesktop({
      lastKnownLanEndpoints: [
        { host: '10.0.0.1', port: 9988, lastSeen: Date.now() },
        { host: '192.168.1.100', port: 9988, lastSeen: Date.now() },
      ],
    });

    const result = await findLanEndpoint(desktop, 3000);
    expect(result).not.toBeNull();
    expect(result?.port).toBe(9988);
  });
});
