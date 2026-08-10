/**
 * client.test.ts — direct tests for RelayClient.request.
 *
 * Every authenticated relay call flows through this method, including
 * the load-bearing 401-auto-refresh path that the design doc §3
 * explicitly preserves. The 5 higher-level client methods (register /
 * login / refresh / logout / listPairedDesktops) exercise it
 * transitively, but no test covered it *directly* — this file fills
 * that gap with a fake RelayTokenStore + stubbed global fetch.
 */

import {
  RelayApiError,
  RelayClient,
  type RelayTokenPair,
  type RelayTokenStore,
} from '../client';

// ─── Fake token store ───────────────────────────────────────────────────────

function makeTokenStore(initial: { access: string | null; refresh: string | null }): RelayTokenStore & {
  tokens: { access: string | null; refresh: string | null };
  setTokens: jest.Mock<void, [RelayTokenPair]>;
  clearTokens: jest.Mock<void, []>;
} {
  const tokens = { ...initial };
  const setTokens = jest.fn((pair: RelayTokenPair) => {
    tokens.access = pair.access_token;
    tokens.refresh = pair.refresh_token;
  });
  const clearTokens = jest.fn(() => {
    tokens.access = null;
    tokens.refresh = null;
  });
  return {
    tokens,
    getAccessToken: () => tokens.access,
    getRefreshToken: () => tokens.refresh,
    setTokens,
    clearTokens,
  };
}

// ─── Response helpers ───────────────────────────────────────────────────────
//
// Build real `Response` objects (WHATWG fetch API, available globally in
// the Node test environment). Using real responses means the tests also
// exercise `res.ok` / `res.status` semantics correctly and won't silently
// pass if production code later starts reading `res.headers` or other
// Response-shape fields.

const jsonResponse = (status: number, body: unknown): Response =>
  new Response(body == null ? '' : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const emptyResponse = (status: number): Response =>
  new Response('', { status });

/**
 * A Response whose `json()` rejects — simulates a non-JSON body (e.g.
 * gateway HTML error page). Real Response would behave the same way when
 * given invalid JSON, but rolling our own here avoids depending on
 * cross-runtime parse-error messages.
 */
const malformedResponse = (status: number): Response =>
  ({
    ok: false,
    status,
    json: async () => {
      throw new Error('not json');
    },
  }) as unknown as Response;

// ─── Tests ──────────────────────────────────────────────────────────────────

beforeEach(() => {
  (global.fetch as jest.Mock).mockReset();
});

describe('RelayClient.request — happy path', () => {
  it('sends Authorization: Bearer <accessToken> and returns parsed JSON', async () => {
    const store = makeTokenStore({ access: 'access-abc', refresh: 'r1' });
    const client = new RelayClient('https://relay.example.com', store);
    (global.fetch as jest.Mock).mockResolvedValueOnce(
      jsonResponse(200, { hello: 'world' }),
    );

    const result = await client.request<{ hello: string }>('/api/me');

    expect(result).toEqual({ hello: 'world' });
    expect(global.fetch).toHaveBeenCalledTimes(1);
    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe('https://relay.example.com/api/me');
    expect(init.headers).toMatchObject({
      'Content-Type': 'application/json',
      Authorization: 'Bearer access-abc',
    });
  });

  it('returns undefined when the response body is empty', async () => {
    const store = makeTokenStore({ access: 'a', refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    (global.fetch as jest.Mock).mockResolvedValueOnce(emptyResponse(200));

    const result = await client.request<unknown>('/api/noop', { method: 'POST' });

    expect(result).toBeUndefined();
  });
});

describe('RelayClient.request — skipAuth', () => {
  it('omits the Authorization header when skipAuth is true', async () => {
    const store = makeTokenStore({ access: 'access-abc', refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    (global.fetch as jest.Mock).mockResolvedValueOnce(
      jsonResponse(200, { ok: true }),
    );

    await client.request('/api/accounts/login', {
      method: 'POST',
      body: JSON.stringify({ email: 'x', password: 'y' }),
      skipAuth: true,
    });

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
  });

  it('does NOT trigger the 401-refresh retry when skipAuth is true', async () => {
    const store = makeTokenStore({ access: null, refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    // Login endpoint returning 401 (bad password) — should surface, not retry.
    (global.fetch as jest.Mock).mockResolvedValueOnce(
      jsonResponse(401, { error: { code: 'bad_credentials', message: 'wrong password' } }),
    );

    await expect(
      client.request('/api/accounts/login', {
        method: 'POST',
        skipAuth: true,
      }),
    ).rejects.toBeInstanceOf(RelayApiError);

    // Exactly one network call — no refresh attempt.
    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(store.setTokens).not.toHaveBeenCalled();
    expect(store.clearTokens).not.toHaveBeenCalled();
  });
});

describe('RelayClient.request — 401 auto-refresh', () => {
  it('refreshes and retries with the new token on 401', async () => {
    const store = makeTokenStore({ access: 'expired', refresh: 'valid-refresh' });
    const client = new RelayClient('https://relay.example.com', store);

    (global.fetch as jest.Mock)
      // 1st call: original request, 401
      .mockResolvedValueOnce(jsonResponse(401, { error: { code: 'expired' } }))
      // 2nd call: /api/accounts/refresh, returns new pair
      .mockResolvedValueOnce(
        jsonResponse(200, { access_token: 'new-access', refresh_token: 'new-refresh' }),
      )
      // 3rd call: retry of original request with new Bearer, succeeds
      .mockResolvedValueOnce(jsonResponse(200, { ok: true }));

    const result = await client.request<{ ok: boolean }>('/api/me');

    expect(result).toEqual({ ok: true });
    expect(global.fetch).toHaveBeenCalledTimes(3);

    // Refresh pushed new tokens through the adapter — this pins the
    // pass-through contract (wire shape reaches storeTokens unchanged).
    // If the client later needs to normalize tokens (e.g. strip a Bearer
    // prefix), update this expectation intentionally.
    expect(store.setTokens).toHaveBeenCalledWith({
      access_token: 'new-access',
      refresh_token: 'new-refresh',
    });
    expect(store.tokens.access).toBe('new-access');

    // Retry used the refreshed token.
    const [, retryInit] = (global.fetch as jest.Mock).mock.calls[2];
    expect((retryInit.headers as Record<string, string>).Authorization).toBe(
      'Bearer new-access',
    );
  });

  it('clears tokens and propagates the 401 when refresh itself fails', async () => {
    const store = makeTokenStore({ access: 'expired', refresh: 'revoked' });
    const client = new RelayClient('https://relay.example.com', store);

    (global.fetch as jest.Mock)
      // Original request: 401
      .mockResolvedValueOnce(jsonResponse(401, { error: { code: 'expired' } }))
      // Refresh endpoint: 401 (refresh revoked)
      .mockResolvedValueOnce(
        jsonResponse(401, { error: { code: 'refresh_revoked', message: 'revoked' } }),
      );

    await expect(client.request('/api/me')).rejects.toBeInstanceOf(RelayApiError);

    // Refresh clearTokens in the catch block → tokens are wiped.
    expect(store.clearTokens).toHaveBeenCalled();
    expect(store.tokens.access).toBeNull();
    expect(store.tokens.refresh).toBeNull();
    // Exactly 2 calls: original + refresh. No retry of the original.
    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it('returns false from refresh (and propagates 401) when no refresh token is stored', async () => {
    const store = makeTokenStore({ access: 'expired', refresh: null });
    const client = new RelayClient('https://relay.example.com', store);

    (global.fetch as jest.Mock).mockResolvedValueOnce(
      jsonResponse(401, { error: { code: 'expired' } }),
    );

    await expect(client.request('/api/me')).rejects.toBeInstanceOf(RelayApiError);
    // Only the original call — refresh short-circuited with no token.
    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(store.setTokens).not.toHaveBeenCalled();
  });
});

describe('RelayClient.request — error shapes', () => {
  it('maps {error: {code, message}} to RelayApiError(status, code, message)', async () => {
    const store = makeTokenStore({ access: 'a', refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    (global.fetch as jest.Mock).mockResolvedValueOnce(
      jsonResponse(403, {
        error: { code: 'forbidden', message: 'not allowed' },
      }),
    );

    const err = await client
      .request('/api/secret', { skipAuth: true })
      .catch((e) => e);
    expect(err).toBeInstanceOf(RelayApiError);
    expect(err).toMatchObject({
      status: 403,
      code: 'forbidden',
      message: 'not allowed',
    });
  });

  it('maps {error: "string"} to RelayApiError(status, string, "HTTP <status>: <string>")', async () => {
    const store = makeTokenStore({ access: 'a', refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    (global.fetch as jest.Mock).mockResolvedValueOnce(
      jsonResponse(404, { error: 'plan_not_found' }),
    );

    const err = await client
      .request('/api/billing/change-plan', { skipAuth: true })
      .catch((e) => e);
    expect(err).toBeInstanceOf(RelayApiError);
    expect(err).toMatchObject({
      status: 404,
      code: 'plan_not_found',
      message: 'HTTP 404: plan_not_found',
    });
  });

  it('falls back to UNKNOWN / HTTP <status> when the response is not JSON or has no error field', async () => {
    const store = makeTokenStore({ access: 'a', refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    (global.fetch as jest.Mock).mockResolvedValueOnce(malformedResponse(500));

    const err = await client.request('/api/thing', { skipAuth: true }).catch((e) => e);
    expect(err).toBeInstanceOf(RelayApiError);
    expect(err).toMatchObject({ status: 500, code: 'UNKNOWN', message: 'HTTP 500' });
  });
});

describe('RelayClient.request — refresh edge cases (invariants)', () => {
  it('treats a refresh response with missing token fields as a failed refresh', async () => {
    // Pins the invariant: if /api/accounts/refresh somehow returns 200 with
    // no access_token (malformed / partial), the client MUST NOT retry the
    // original request with undefined Bearer. Today this works because
    // refresh() passes the pair to storeTokens AS-IS and then request()
    // reads the (now set-to-undefined) token — but a future refactor that
    // adds validation should keep the same no-retry behavior.
    const store = makeTokenStore({ access: 'expired', refresh: 'valid-refresh' });
    const client = new RelayClient('https://relay.example.com', store);

    (global.fetch as jest.Mock)
      // Original: 401
      .mockResolvedValueOnce(jsonResponse(401, { error: { code: 'expired' } }))
      // Refresh: 200 but empty body → request<RelayTokenPair> returns undefined
      .mockResolvedValueOnce(emptyResponse(200));

    const err = await client.request('/api/me').catch((e) => e);
    expect(err).toBeInstanceOf(RelayApiError);
    // No retry of the original — refresh() threw internally (can't destructure
    // undefined.access_token) and was caught, clearTokens was called.
    expect(global.fetch).toHaveBeenCalledTimes(2);
    expect(store.clearTokens).toHaveBeenCalled();
  });

  it('does not recursively refresh when the retry itself returns 401 (no infinite loop)', async () => {
    // Pins the invariant: the 401-refresh block is a single if, not a while.
    // A future refactor to a loop-based retry would break this test.
    const store = makeTokenStore({ access: 'expired', refresh: 'valid-refresh' });
    const client = new RelayClient('https://relay.example.com', store);

    (global.fetch as jest.Mock)
      // Original: 401
      .mockResolvedValueOnce(jsonResponse(401, { error: { code: 'expired' } }))
      // Refresh: 200 with new pair
      .mockResolvedValueOnce(
        jsonResponse(200, { access_token: 'new-access', refresh_token: 'new-refresh' }),
      )
      // Retry: also 401 (rare: e.g. server just revoked the new token)
      .mockResolvedValueOnce(
        jsonResponse(401, { error: { code: 'revoked_mid_flight' } }),
      );

    const err = await client.request('/api/me').catch((e) => e);
    expect(err).toBeInstanceOf(RelayApiError);
    expect(err).toMatchObject({ status: 401, code: 'revoked_mid_flight' });
    // Exactly 3 calls: original + refresh + retry. No second refresh attempt.
    expect(global.fetch).toHaveBeenCalledTimes(3);
  });

  it('propagates network-level errors (fetch rejection) unchanged, not as RelayApiError', async () => {
    // Pins the invariant: offline / DNS failure surfaces the raw error so
    // callers can distinguish transport failures from API errors.
    const store = makeTokenStore({ access: 'a', refresh: 'r' });
    const client = new RelayClient('https://relay.example.com', store);
    const networkErr = new TypeError('Network request failed');
    (global.fetch as jest.Mock).mockRejectedValueOnce(networkErr);

    await expect(client.request('/api/me')).rejects.toBe(networkErr);
    // No retry, no refresh — network errors don't trigger the 401 path.
    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(store.clearTokens).not.toHaveBeenCalled();
  });
});
