import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { apiFetch } from './api'
import { useAuthStore } from '@/stores/auth-store'

// Regression: a caller passing a custom `headers` option used to clobber the
// merged auth headers because `...options` was spread AFTER the headers key,
// dropping the Authorization bearer and 401-ing the request (the consent gate's
// POST /consent/accept hit exactly this). The fix spreads options first and
// computes headers last. These tests lock that the Authorization header
// survives both when the caller passes headers and when it doesn't.

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('apiFetch header merging', () => {
  beforeEach(() => {
    useAuthStore.setState({ accessToken: 'tok-123', isAuthenticated: true })
  })
  afterEach(() => {
    vi.restoreAllMocks()
    useAuthStore.setState({ accessToken: null, isAuthenticated: false })
  })

  it('keeps Authorization when the caller passes a custom headers object', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/consent/accept', {
      method: 'POST',
      headers: { 'X-Custom': 'yes' },
      body: JSON.stringify({ version: 'v1' }),
    })

    const init = fetchMock.mock.calls[0][1] as RequestInit
    const headers = init.headers as Record<string, string>
    expect(headers['Authorization']).toBe('Bearer tok-123')
    expect(headers['X-Custom']).toBe('yes')
    expect(init.method).toBe('POST')
  })

  it('keeps Authorization when no custom headers are passed', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/consent/status')

    const init = fetchMock.mock.calls[0][1] as RequestInit
    const headers = init.headers as Record<string, string>
    expect(headers['Authorization']).toBe('Bearer tok-123')
  })
})
