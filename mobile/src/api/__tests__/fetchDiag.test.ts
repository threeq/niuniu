/**
 * fetchOrThrow guards every outbound HTTP call in the mobile transport
 * stack so that React Native's opaque `TypeError: Network request failed`
 * always surfaces with the URL and method baked in. Without this wrapper
 * the user sees "加载失败 Network request failed" and there's no way to
 * tell whether the failure was the relay, a stale LAN endpoint, or a
 * typo in baseUrl.
 */

import { fetchOrThrow } from '../fetchDiag';

let originalFetch: typeof fetch;

beforeEach(() => {
  originalFetch = globalThis.fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

describe('fetchOrThrow', () => {
  it('passes through a successful response unchanged', async () => {
    const fakeResp = { ok: true, status: 200 } as Response;
    globalThis.fetch = jest.fn(async () => fakeResp) as unknown as typeof fetch;
    const got = await fetchOrThrow('https://x.example/api/y', { method: 'GET' });
    expect(got).toBe(fakeResp);
  });

  it('rejects empty / null / undefined URLs before calling fetch', async () => {
    globalThis.fetch = jest.fn(async () => {
      throw new Error('should not be called');
    }) as unknown as typeof fetch;
    await expect(fetchOrThrow('')).rejects.toThrow(/invalid URL/);
    await expect(
      fetchOrThrow(undefined as unknown as string),
    ).rejects.toThrow(/invalid URL/);
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it('rejects URLs that begin with the literal "undefined" / "null" (state hydration bug)', async () => {
    globalThis.fetch = jest.fn(async () => {
      throw new Error('should not be called');
    }) as unknown as typeof fetch;
    await expect(
      fetchOrThrow('undefined/d/abc/rpc'),
    ).rejects.toThrow(/state hydration bug/);
    await expect(
      fetchOrThrow('null/api/x'),
    ).rejects.toThrow(/state hydration bug/);
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it('wraps a TypeError with the URL + method + original cause', async () => {
    const original = new TypeError('Network request failed');
    globalThis.fetch = jest.fn(async () => {
      throw original;
    }) as unknown as typeof fetch;

    let captured: (Error & { cause?: unknown }) | null = null;
    try {
      await fetchOrThrow('https://niuniu-relay.niu6ai.com/d/desk-1/rpc', {
        method: 'POST',
      });
    } catch (err) {
      captured = err as Error & { cause?: unknown };
    }
    expect(captured).not.toBeNull();
    expect(captured!.message).toMatch(
      /fetch POST https:\/\/niuniu-relay\.niu6ai\.com\/d\/desk-1\/rpc failed: Network request failed/,
    );
    // Original error preserved on .cause for callers that introspect.
    expect(captured!.cause).toBe(original);
  });

  it('defaults method to GET when not supplied', async () => {
    globalThis.fetch = jest.fn(async () => {
      throw new TypeError('boom');
    }) as unknown as typeof fetch;
    await expect(fetchOrThrow('https://x.example/api/z')).rejects.toThrow(
      /fetch GET https:\/\/x\.example\/api\/z failed/,
    );
  });

  it('handles non-Error throwables by stringifying them', async () => {
    globalThis.fetch = jest.fn(async () => {
      // eslint-disable-next-line @typescript-eslint/only-throw-error
      throw 'string-rejection';
    }) as unknown as typeof fetch;
    await expect(fetchOrThrow('https://x.example/api/q')).rejects.toThrow(
      /fetch GET https:\/\/x\.example\/api\/q failed: string-rejection/,
    );
  });
});
