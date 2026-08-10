import './lib/hotkey-bootstrap';
import './lib/theme-bootstrap';
import './i18n';
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { router } from './router'
import { ErrorBoundary } from './components/shared/error-boundary'
import { ThemedToaster } from './components/layout/themed-toaster'
import { ConfirmHost } from './lib/confirm-host'
import { useOrgStore } from './stores/org-store'
import { useConfigStore } from './stores/config-store'
import { useAuthStore, refreshUser, refreshAccessToken, isAccessTokenNearExpiry } from './stores/auth-store'
import { startUpdateChecker } from './lib/update-checker'
import { postDesktopRunnerToken, isDesktopShell } from './lib/desktop-runner-context'
import { installIMECaretFix } from './lib/ime-caret-fix'
import './index.css'

// WebView2 (Windows desktop) IME workaround. RE-ENABLED after the ws-683
// real-device baseline check: with it disabled, the IME candidate window
// mispositioned to the screen's top-left (it stopped following the caret), so
// the DOM fallback IS still needed. The window-focus blur→refocus re-arm forces
// WebView2 to recompute the composition-window position with fresh coordinates,
// re-anchoring the candidate to the caret. No-op on non-Windows. Root-cause fix
// for candidate positioning is a WebView2 Runtime upgrade (see issue #478 / docs).
installIMECaretFix()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 60000,
    },
  },
})

// SPA boot: see comment block below for why we await config before render.
;(async () => {
  // Load auth_enabled before rendering so settings page and route guards
  // see the correct edition signal on first paint.
  await useConfigStore.getState().load()

  // SPA boot:
  //
  //   * refreshUser is fired UNCONDITIONALLY. It works in three modes:
  //     - With a token (team-edition logged in): refreshes the user snapshot
  //       so admin role flips made by another admin take effect on next page
  //       refresh without re-login.
  //     - Without a token, Auth.Enabled=false (personal edition): the
  //       IdentityResolver middleware seeds the request from the SingleUser
  //       default, so /me returns the local user. This is what populates
  //       auth-store.user in the personal edition — without it the SPA
  //       defaults to id=0 in OwnerPicker / create dialogs and triggers
  //       403 on creates.
  //     - Without a token, Auth.Enabled=true (team-edition /login visitor):
  //       /me returns 401 and refreshUser silently no-ops; api.ts does NOT
  //       force the /login redirect because refreshUser uses raw fetch, not
  //       apiFetch.
  //
  //   * useOrgStore.fetch only fires when a token is present. It's a
  //     team-edition concept — personal edition has no orgs by default.
  void refreshUser()
  if (useAuthStore.getState().accessToken) {
    void useOrgStore.getState().fetch()
  }

  // Background update poll. No-ops in team-edition / standalone-server mode
  // (where personal_mode=false). In personal mode, fires 5s after boot then
  // every 6h; surfaces a sonner toast with download CTA on new version.
  // Self-throttles via localStorage so refreshing a tab doesn't re-fire.
  const cfg = useConfigStore.getState()
  if (cfg.personalMode && cfg.serverVersion) {
    startUpdateChecker(cfg.serverVersion)
  }

  // Keep the desktop local-runner's copy of the auth token fresh. The SPA
  // rotates its access token; the desktop reuses it for the reverse-channel
  // handshake, so a stale snapshot makes every reconnect after expiry fail with
  // 401. Push the current token now and on every rotation (no-op off-desktop).
  postDesktopRunnerToken(useAuthStore.getState().accessToken ?? '')
  useAuthStore.subscribe((state, prev) => {
    if (state.accessToken && state.accessToken !== prev.accessToken) {
      postDesktopRunnerToken(state.accessToken)
    }
  })

  // Desktop reverse-channel token self-healing (#526). This connection webview
  // is the SOLE owner of the session's rotating (single-use) refresh token — the
  // desktop process cannot refresh independently without invalidating this
  // session. So the webview keeps the token alive itself:
  //
  //   * Proactive heartbeat — refresh a little before the 15-min access token
  //     expires (checked every 45s). The subscribe above then pushes each fresh
  //     token to the desktop, so the reverse channel's next reconnect always has
  //     a valid JWT and never gets permanently stuck on 401.
  //   * Manual hook `window.__niuniuRunnerRefresh__()` — the desktop calls this
  //     via ExecJS when a reverse-channel dial actually returns 401 (e.g. after
  //     sleep/wake froze the heartbeat), forcing an immediate refresh + push so
  //     recovery doesn't wait for the next tick.
  //
  // Both no-op off-desktop. refreshAccessToken() dedupes concurrent calls and
  // logs out only when the refresh token itself is gone/expired (a genuinely
  // ended session), so this can't spin forever.
  if (isDesktopShell()) {
    window.setInterval(() => {
      if (isAccessTokenNearExpiry(120)) {
        void refreshAccessToken()
      }
    }, 45000)
    ;(window as unknown as { __niuniuRunnerRefresh__?: () => void }).__niuniuRunnerRefresh__ = () => {
      void refreshAccessToken().then((tok) => {
        if (tok) postDesktopRunnerToken(tok)
      })
    }
  }

  const rootElement = document.getElementById('root')
  if (!rootElement) {
    throw new Error('Could not find root element to mount to')
  }

  const root = createRoot(rootElement)
  root.render(
    <StrictMode>
      <ErrorBoundary>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
        <ThemedToaster />
        <ConfirmHost />
      </ErrorBoundary>
    </StrictMode>
  )
})()
