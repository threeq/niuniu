/**
 * RelayClient — authenticated HTTP client for relay account endpoints.
 *
 * Token storage is NOT owned here. Construct with a `RelayTokenStore`
 * adapter (see `src/stores/relayAccountStore.ts#createRelayTokenStore`)
 * so the zustand store remains the single source of truth for access +
 * refresh tokens. The adapter is wired synchronously on reads and writes;
 * persistence to SecureStore is handled by the store's persist middleware.
 */

export interface RelayTokenStore {
  getAccessToken(): string | null;
  getRefreshToken(): string | null;
  setTokens(pair: RelayTokenPair): void;
  clearTokens(): void;
}

export interface RelayTokenPair {
  access_token: string;
  refresh_token: string;
  expires_in?: number;
}

/**
 * Normalized shape returned by {@link RelayClient.listPairedDesktops}.
 *
 * Note: the relay response intentionally omits the relay device token
 * (it is issued write-once at pair time and stored only on the mobile).
 * Callers must therefore preserve any locally-stored token for a
 * desktopId they already know about, rather than treating this payload
 * as authoritative for the full {@link PairedDesktop} store entry.
 */
export interface PairedDesktop {
  desktopId: string;
  desktopName: string;
  xpub: string;
  signPub: string;
  pairedAt: string;
}

// Raw wire shape from the relay (GET /api/my/paired-desktops).
// See relay/internal/api/mobile_devices_handler.go:pairedDesktopResp.
interface rawPairedDesktop {
  id: string;
  name: string;
  desktop_xpub_b64: string;
  desktop_sign_pub_b64: string;
  online?: boolean;
  last_seen_at?: number | null;
}

export class RelayApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = 'RelayApiError';
  }
}

// ─── Email-code (passwordless) login ──────────────────────────────────────────
//
// Standalone helpers, NOT methods on RelayClient: these endpoints are
// pre-auth (no Bearer token yet) and are called from the login screen +
// relayAccountStore directly with a baseUrl. They throw a plain `Error`
// whose `message` is the backend-supplied error code (e.g. `"invalid_code"`,
// `"expired_code"`, `"too_many_attempts"`, `"invalid_email"`) so UI code can
// branch on `err.message === 'invalid_code'`. On network/HTTP failure with
// no parseable body, the message is `"HTTP <status>"`.

export type RequestLoginCodeResponse = {
  sent: boolean;
  expires_in: number;
  resend_after: number;
};

export type VerifyLoginCodeResponse = {
  access_token: string;
  refresh_token: string;
  email: string;
  is_new_user: boolean;
  expires_in?: number;
};

export async function requestLoginCode(
  baseUrl: string,
  email: string,
): Promise<RequestLoginCodeResponse> {
  const res = await fetch(`${baseUrl}/api/accounts/email-code/request`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({} as { error?: string }));
    throw new Error(body?.error || `HTTP ${res.status}`);
  }
  return res.json();
}

export async function verifyLoginCode(
  baseUrl: string,
  email: string,
  code: string,
): Promise<VerifyLoginCodeResponse> {
  const res = await fetch(`${baseUrl}/api/accounts/email-code/verify`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, code }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({} as { error?: string }));
    throw new Error(body?.error || `HTTP ${res.status}`);
  }
  return res.json();
}

export class RelayClient {
  constructor(
    public readonly baseUrl: string,
    private readonly tokenStore: RelayTokenStore,
  ) {}

  // ─── Token Storage ──────────────────────────────────────────────────────────

  getAccessToken(): string | null {
    return this.tokenStore.getAccessToken();
  }

  getRefreshToken(): string | null {
    return this.tokenStore.getRefreshToken();
  }

  storeTokens(pair: RelayTokenPair): void {
    this.tokenStore.setTokens(pair);
  }

  clearTokens(): void {
    this.tokenStore.clearTokens();
  }

  // ─── HTTP helpers ───────────────────────────────────────────────────────────

  /**
   * Authenticated request to the relay. Prepends `baseUrl`, attaches the
   * `Authorization: Bearer` header from the token store (unless
   * `skipAuth: true`), auto-retries once on 401 after a token refresh,
   * and parses JSON on success. Throws {@link RelayApiError} on non-2xx.
   *
   * Consumers outside this module SHOULD use this method rather than
   * hand-rolled `fetch(client.baseUrl + ...)` — the hand-rolled path
   * misses the 401 auto-refresh and has to re-implement error parsing.
   * Accepts both relay error shapes: `{ error: { code, message } }` and
   * `{ error: "string" }` (some billing endpoints use the latter).
   */
  async request<T>(
    path: string,
    opts: RequestInit & { skipAuth?: boolean } = {},
  ): Promise<T> {
    const { skipAuth, ...fetchOpts } = opts;
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...(fetchOpts.headers as Record<string, string>),
    };

    if (!skipAuth) {
      const token = this.getAccessToken();
      if (token) headers['Authorization'] = `Bearer ${token}`;
    }

    const url = `${this.baseUrl}${path}`;
    let res = await fetch(url, { ...fetchOpts, headers });

    // Auto-refresh on 401
    if (res.status === 401 && !skipAuth) {
      const refreshed = await this.refresh().catch(() => false);
      if (refreshed) {
        const newToken = this.getAccessToken();
        if (newToken) headers['Authorization'] = `Bearer ${newToken}`;
        res = await fetch(url, { ...fetchOpts, headers });
      }
    }

    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      const err = body?.error;
      if (typeof err === 'string') {
        // String error shape: `{ error: "plan_not_found" }`. Used by
        // billing endpoints today; any endpoint that returns the string
        // form will be handled here.
        throw new RelayApiError(res.status, err, `HTTP ${res.status}: ${err}`);
      }
      throw new RelayApiError(
        res.status,
        err?.code ?? 'UNKNOWN',
        err?.message ?? `HTTP ${res.status}`,
      );
    }

    const text = await res.text();
    if (!text) return undefined as T;
    return JSON.parse(text) as T;
  }

  // ─── Auth ───────────────────────────────────────────────────────────────────

  async register(email: string, password: string): Promise<RelayTokenPair> {
    return this.request<RelayTokenPair>('/api/accounts/register', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
      skipAuth: true,
    });
    // Caller owns storage — the returned pair is pushed into the
    // token store by relayAccountStore.register.
  }

  async login(email: string, password: string): Promise<RelayTokenPair> {
    return this.request<RelayTokenPair>('/api/accounts/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
      skipAuth: true,
    });
    // Caller owns storage — the returned pair is pushed into the
    // token store by relayAccountStore.login.
  }

  async refresh(): Promise<boolean> {
    const refreshToken = this.getRefreshToken();
    if (!refreshToken) return false;
    try {
      const pair = await this.request<RelayTokenPair>('/api/accounts/refresh', {
        method: 'POST',
        body: JSON.stringify({ refresh_token: refreshToken }),
        skipAuth: true,
      });
      // refresh() IS called from fetch()'s 401 retry path, so it must
      // update tokens through the injected store — the caller of the
      // original request has no chance to observe the new pair.
      this.storeTokens(pair);
      return true;
    } catch {
      this.clearTokens();
      return false;
    }
  }

  async logout(): Promise<void> {
    const refreshToken = this.getRefreshToken();
    try {
      if (refreshToken) {
        await this.request('/api/accounts/logout', {
          method: 'POST',
          body: JSON.stringify({ refresh_token: refreshToken }),
        });
      }
    } finally {
      this.clearTokens();
    }
  }

  // ─── Paired Desktops ────────────────────────────────────────────────────────

  /**
   * List desktops paired to the current relay account.
   *
   * Normalizes the relay's response (bare JSON array; snake_case fields,
   * no last-seen → Date) into the camelCase {@link PairedDesktop} shape
   * the mobile store expects.  Falls back to /api/desktops on 404 for
   * older relay builds.
   */
  async listPairedDesktops(): Promise<PairedDesktop[]> {
    const fetchRaw = async (path: string): Promise<rawPairedDesktop[]> => {
      // Relay returns a bare array; older builds may have returned
      // `{desktops:[…]}` — tolerate both.
      const data = await this.request<rawPairedDesktop[] | { desktops: rawPairedDesktop[] }>(path);
      if (Array.isArray(data)) return data;
      return data?.desktops ?? [];
    };
    let raw: rawPairedDesktop[];
    try {
      raw = await fetchRaw('/api/my/paired-desktops');
    } catch (err) {
      if (err instanceof RelayApiError && err.status === 404) {
        raw = await fetchRaw('/api/desktops');
      } else {
        throw err;
      }
    }
    return raw.map((d) => ({
      desktopId: d.id,
      desktopName: d.name,
      xpub: d.desktop_xpub_b64,
      signPub: d.desktop_sign_pub_b64,
      pairedAt:
        d.last_seen_at != null
          ? new Date(d.last_seen_at * 1000).toISOString()
          : new Date().toISOString(),
    }));
  }
}
