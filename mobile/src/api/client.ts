import { useAuthStore } from '../stores/authStore';
import { useServerStore } from '../stores/serverStore';
import { useTransportStore } from '../stores/transportStore';
import { usePairedDesktopsStore } from '../stores/pairedDesktopsStore';
import { fetchOrThrow } from './fetchDiag';
import type { TokenPair } from './types';

const APP_VERSION = '1.0.0';

function isVersionLessThan(current: string, required: string): boolean {
  const a = current.split('.').map(Number);
  const b = required.split('.').map(Number);
  for (let i = 0; i < 3; i++) {
    if ((a[i] ?? 0) < (b[i] ?? 0)) return true;
    if ((a[i] ?? 0) > (b[i] ?? 0)) return false;
  }
  return false;
}

class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

async function getBaseUrl(): Promise<string> {
  const url = useServerStore.getState().getBaseUrl();
  if (!url) throw new ApiError(0, 'NO_SERVER', 'No server selected');
  return url;
}

async function refreshAccessToken(): Promise<boolean> {
  const { refreshToken, setTokens, clearTokens } = useAuthStore.getState();
  if (!refreshToken) return false;

  try {
    const baseUrl = await getBaseUrl();
    const res = await fetchOrThrow(`${baseUrl}/api/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });

    if (!res.ok) {
      await clearTokens();
      return false;
    }

    const data: TokenPair = await res.json();
    await setTokens(data.access_token, data.refresh_token);
    return true;
  } catch {
    await clearTokens();
    return false;
  }
}

export async function apiFetch<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  // If the user has selected a paired desktop, route through the relay tunnel.
  const activeDesktopId = useTransportStore.getState().activeDesktopId;
  if (activeDesktopId) {
    const desktop = usePairedDesktopsStore.getState().desktops.find(
      (d) => d.desktopId === activeDesktopId,
    );
    if (desktop) {
      // Lazy import to avoid circular dependency (apiClient imports apiFetch from here).
      const { smartFetch } = await import('./apiClient');
      // Mobile UI calls api.get('/workspaces') without an /api prefix, and the
      // direct path below adds it via `${baseUrl}/api${path}`. The relay path
      // forwards the inner request verbatim to the desktop's local niuniu-server
      // (whose REST routes ARE under /api), so we MUST add the same prefix here
      // — otherwise the desktop's NoRoute handler returns the embedded SPA's
      // index.html and the mobile's `JSON.parse(body)` chokes on the leading `<`
      // ("JSON Parse error: Unexpected character: <"). This is the same /api
      // namespace contract on both sides; only the place that adds the prefix
      // differs.
      return smartFetch<T>(`/api${path}`, { ...options, desktopId: activeDesktopId });
    }
  }

  const baseUrl = await getBaseUrl();
  const { accessToken } = useAuthStore.getState();

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string>),
  };

  if (accessToken) {
    headers['Authorization'] = `Bearer ${accessToken}`;
  }

  let res = await fetchOrThrow(`${baseUrl}/api${path}`, { ...options, headers });

  // Auto-refresh on 401
  if (res.status === 401 && accessToken) {
    const refreshed = await refreshAccessToken();
    if (refreshed) {
      const newToken = useAuthStore.getState().accessToken;
      headers['Authorization'] = `Bearer ${newToken}`;
      res = await fetchOrThrow(`${baseUrl}/api${path}`, { ...options, headers });
    }
  }

  // Check API version compatibility (simple numeric comparison)
  const minClientVersion = res.headers.get('X-Min-Client-Version');
  if (minClientVersion && isVersionLessThan(APP_VERSION, minClientVersion)) {
    console.warn(`Server requires client >= ${minClientVersion}, current: ${APP_VERSION}`);
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const error = body?.error;
    throw new ApiError(
      res.status,
      error?.code ?? 'UNKNOWN',
      error?.message ?? `HTTP ${res.status}`,
    );
  }

  // Handle empty responses
  const text = await res.text();
  if (!text) return undefined as T;
  return JSON.parse(text);
}

// Convenience methods
export const api = {
  get: <T>(path: string) => apiFetch<T>(path),
  post: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, {
      method: 'POST',
      body: body ? JSON.stringify(body) : undefined,
    }),
  put: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, {
      method: 'PUT',
      body: body ? JSON.stringify(body) : undefined,
    }),
  delete: <T>(path: string) => apiFetch<T>(path, { method: 'DELETE' }),
};

// Multipart upload — does NOT route via the relay/LAN encrypted tunnel
// (Noise KK envelope can't carry raw multipart bytes). Falls back to a
// direct fetch against the selected server. If the user is on a relay-only
// transport, uploads will fail — surface that as an explicit error rather
// than silently sending plaintext over relay.
export async function apiUpload<T>(path: string, formData: FormData): Promise<T> {
  const activeDesktopId = useTransportStore.getState().activeDesktopId;
  if (activeDesktopId) {
    const desktop = usePairedDesktopsStore
      .getState()
      .desktops.find((d) => d.desktopId === activeDesktopId);
    if (desktop) {
      throw new ApiError(
        0,
        'UPLOAD_VIA_RELAY_UNSUPPORTED',
        '附件上传暂不支持桌面中继路径，请直连服务器后重试',
      );
    }
  }

  const baseUrl = await getBaseUrl();
  const { accessToken } = useAuthStore.getState();

  const headers: Record<string, string> = {};
  if (accessToken) headers['Authorization'] = `Bearer ${accessToken}`;

  let res = await fetchOrThrow(`${baseUrl}/api${path}`, {
    method: 'POST',
    headers,
    body: formData,
  });

  if (res.status === 401 && accessToken) {
    const refreshed = await refreshAccessToken();
    if (refreshed) {
      const newToken = useAuthStore.getState().accessToken;
      headers['Authorization'] = `Bearer ${newToken}`;
      res = await fetchOrThrow(`${baseUrl}/api${path}`, {
        method: 'POST',
        headers,
        body: formData,
      });
    }
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const error = body?.error;
    throw new ApiError(
      res.status,
      error?.code ?? 'UNKNOWN',
      error?.message ?? `HTTP ${res.status}`,
    );
  }

  const text = await res.text();
  if (!text) return undefined as T;
  return JSON.parse(text);
}

// Auth-specific API (no middleware)
export async function loginApi(username: string, password: string): Promise<TokenPair> {
  const baseUrl = await getBaseUrl();
  const res = await fetchOrThrow(`${baseUrl}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, 'UNAUTHORIZED', body?.error?.message ?? 'Login failed');
  }

  return res.json();
}
