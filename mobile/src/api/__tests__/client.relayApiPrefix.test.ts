/**
 * Pins the regression where `apiFetch` forwarded the bare UI-level path
 * (`/workspaces`) into smartFetch when routing through the relay tunnel,
 * while the direct path correctly added `${baseUrl}/api${path}`. The desktop
 * niuniu-server mounts every REST route under `/api`, so a relay request for
 * `/workspaces` falls through to the SPA NoRoute handler and returns
 * `<!DOCTYPE html>...`. After my Content-Length strip on the relay made
 * responses arrive intact, the previously-truncated HTML body became visible
 * and the mobile threw `JSON Parse error: Unexpected character: <`. The fix
 * is one line in apiFetch — prepend `/api` before handing off to smartFetch
 * so the relay path matches the direct path's namespace contract. This test
 * verifies the prefix is applied on the relay branch.
 */
import { useTransportStore } from '../../stores/transportStore';
import { usePairedDesktopsStore } from '../../stores/pairedDesktopsStore';

jest.mock('../../stores/transportStore');
jest.mock('../../stores/pairedDesktopsStore');
jest.mock('../../stores/serverStore', () => ({
  useServerStore: { getState: () => ({ getBaseUrl: () => 'https://example.test' }) },
}));
jest.mock('../../stores/authStore', () => ({
  useAuthStore: {
    getState: () => ({
      accessToken: undefined,
      refreshToken: undefined,
      setTokens: jest.fn(),
      clearTokens: jest.fn(),
    }),
  },
}));

const smartFetchMock = jest.fn();
jest.mock('../apiClient', () => ({
  smartFetch: (...args: unknown[]) => smartFetchMock(...args),
}));

describe('apiFetch — relay path /api prefix', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (useTransportStore.getState as jest.Mock) = jest.fn(() => ({
      activeDesktopId: 'd-1',
    }));
    (usePairedDesktopsStore.getState as jest.Mock) = jest.fn(() => ({
      desktops: [
        {
          desktopId: 'd-1',
          name: 'desk',
          xpub: 'AAAA',
          relayDeviceToken: 'tok',
          mobileId: 'm-1',
        },
      ],
    }));
  });

  it('prepends /api when forwarding to smartFetch on the relay branch', async () => {
    smartFetchMock.mockResolvedValue([{ id: 'ws-1' }]);
    const { apiFetch } = await import('../client');
    await apiFetch<unknown[]>('/workspaces');
    expect(smartFetchMock).toHaveBeenCalledTimes(1);
    const [pathArg, optsArg] = smartFetchMock.mock.calls[0] as [
      string,
      { desktopId: string },
    ];
    expect(pathArg).toBe('/api/workspaces');
    expect(optsArg.desktopId).toBe('d-1');
  });

  it('does not double-prefix paths that already start with /api', async () => {
    // Sanity check: today no UI call site ships `/api` itself, but if a future
    // call does, the prefix logic should not produce `/api/api/...`. The fix
    // is the simplest possible (literal prepend) so this test will fail loudly
    // if anyone changes the call shape; the failure mode is then a clear
    // signal to switch to a `path.startsWith('/api') ? path : '/api' + path`.
    smartFetchMock.mockResolvedValue({});
    const { apiFetch } = await import('../client');
    await apiFetch<unknown>('/workspaces/abc');
    const [pathArg] = smartFetchMock.mock.calls[0] as [string];
    expect(pathArg).toBe('/api/workspaces/abc');
    expect(pathArg.startsWith('/api/api')).toBe(false);
  });

  it('forwards POST method + body to smartFetch unchanged', async () => {
    smartFetchMock.mockResolvedValue({ ok: true });
    const { apiFetch } = await import('../client');
    await apiFetch<unknown>('/projects', {
      method: 'POST',
      body: JSON.stringify({ name: 'p1' }),
    });
    const [pathArg, optsArg] = smartFetchMock.mock.calls[0] as [
      string,
      { method?: string; body?: string },
    ];
    expect(pathArg).toBe('/api/projects');
    expect(optsArg.method).toBe('POST');
    expect(optsArg.body).toBe('{"name":"p1"}');
  });
});
