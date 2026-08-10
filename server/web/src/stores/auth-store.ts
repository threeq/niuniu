import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface AuthUser {
  id: number;
  username: string;
  display_name: string;
  role: string;
}

interface AuthState {
  accessToken: string | null;
  refreshToken: string | null;
  user: AuthUser | null;
  isAuthenticated: boolean;

  setTokens: (accessToken: string, refreshToken: string) => void;
  setUser: (user: AuthUser) => void;
  logout: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      accessToken: null,
      refreshToken: null,
      user: null,
      isAuthenticated: false,

      setTokens: (accessToken, refreshToken) =>
        set({ accessToken, refreshToken, isAuthenticated: true }),

      setUser: (user) => set({ user }),

      logout: () =>
        set({ accessToken: null, refreshToken: null, user: null, isAuthenticated: false }),
    }),
    {
      name: 'niuniu-auth-storage',
    }
  )
);

/** Get the current access token (for use outside React components) */
export function getAccessToken(): string | null {
  return useAuthStore.getState().accessToken;
}

/**
 * Parse a JWT's `exp` claim (seconds since epoch) without verifying the
 * signature. Returns null if the token isn't a well-formed JWT — caller
 * treats null as "unknown, assume not expired".
 */
function parseJwtExp(token: string): number | null {
  const parts = token.split('.')
  if (parts.length !== 3) return null
  try {
    const payload = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')))
    return typeof payload.exp === 'number' ? payload.exp : null
  } catch {
    return null
  }
}

/**
 * Returns true when the current access token is within `marginSeconds` of
 * expiry (default 30s). Used by apiFetch to proactively refresh and avoid
 * a 401 round-trip — which would still be logged by Chrome DevTools as a
 * console error even though we silently retry.
 */
export function isAccessTokenNearExpiry(marginSeconds = 30): boolean {
  const tok = getAccessToken()
  if (!tok) return false
  const exp = parseJwtExp(tok)
  if (exp === null) return false
  return exp - Math.floor(Date.now() / 1000) <= marginSeconds
}

/**
 * Re-pull /api/auth/me into auth-store.user.
 *
 * Three modes, all handled by this single call:
 *
 *   1. **Token present (team edition, logged in)**: send the bearer token,
 *      get back the JWT-resolved user. On 401, try one refresh, then logout.
 *
 *   2. **No token, Auth.Enabled=false (personal edition / single-user)**:
 *      send no Authorization header. The IdentityResolver middleware seeds
 *      `auth_user_id` from the SingleUser (default username `local`) so /me
 *      returns the local user. Without this, auth-store.user stays null and
 *      OwnerPicker / create-project dialogs fall back to id=0, triggering
 *      403 from EnsureOwnerWritable.
 *
 *   3. **No token, Auth.Enabled=true (team edition on /login page)**: /me
 *      returns 401. Silently no-op — leave auth-store.user as the previous
 *      snapshot (could be null if first visit). The login flow does its own
 *      setUser after success.
 *
 * Network errors leave the persisted user unchanged.
 */
export async function refreshUser(): Promise<void> {
  // Same proactive-refresh trick as apiFetch — avoids logging a 401 to the
  // console when the access token is about to expire.
  if (isAccessTokenNearExpiry()) {
    await refreshAccessToken();
  }
  const token = useAuthStore.getState().accessToken;
  try {
    const headers: Record<string, string> = {};
    if (token) headers['Authorization'] = `Bearer ${token}`;
    const res = await fetch('/api/auth/me', { headers });
    if (res.ok) {
      useAuthStore.getState().setUser(await res.json());
      return;
    }
    if (res.status === 401 && token) {
      // We had a token but it was bad — try refresh once, then retry.
      const newToken = await refreshAccessToken();
      if (!newToken) {
        useAuthStore.getState().logout();
        return;
      }
      const retry = await fetch('/api/auth/me', {
        headers: { 'Authorization': `Bearer ${newToken}` },
      });
      if (retry.ok) {
        useAuthStore.getState().setUser(await retry.json());
      } else {
        useAuthStore.getState().logout();
      }
    }
    // No-token + 401 = unauthenticated team-edition visitor; leave user as-is.
  } catch {
    // Network error — leave persisted user as-is rather than logging out.
  }
}

/**
 * Attempt to refresh the access token using the stored refresh token.
 * Deduplicates concurrent calls — only one refresh request is in-flight at a time.
 */
let inflightRefresh: Promise<string | null> | null = null;

export function refreshAccessToken(): Promise<string | null> {
  if (inflightRefresh) return inflightRefresh;
  inflightRefresh = doRefresh().finally(() => { inflightRefresh = null; });
  return inflightRefresh;
}

async function doRefresh(): Promise<string | null> {
  const { refreshToken, setTokens, logout } = useAuthStore.getState();
  if (!refreshToken) {
    logout();
    return null;
  }

  try {
    const res = await fetch('/api/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });

    if (!res.ok) {
      logout();
      return null;
    }

    const data = await res.json();
    setTokens(data.access_token, data.refresh_token);
    return data.access_token;
  } catch {
    logout();
    return null;
  }
}
