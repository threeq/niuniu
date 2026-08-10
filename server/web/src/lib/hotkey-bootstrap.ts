/**
 * Seed `window.__NIUNIU_HOTKEYS__` from the URL hash the desktop shell appends
 * when it navigates the main window to the SPA (`#__nnhk=<base64url json>`, see
 * desktop hotkeywin.go hotkeyURLHash / withHotkeyHash).
 *
 * This is the ONLY reliable Go→URL-loaded-SPA delivery channel on the current
 * Wails/WebView2 build: `ExecJS`, document-created `options.JS`, and the async
 * query→broadcast echo are all dropped across the main window's splash→`SetURL`→
 * SPA hand-off (verified from the desktop log: injection fires in Go, config is
 * correct, yet the page never sees the global). The URL hash rides the navigation
 * itself, so the settings page can read the persisted combos synchronously on a
 * fresh launch.
 *
 * Runs once at module import — imported FIRST in main.tsx so it executes before
 * the router reads the URL — and strips the fragment afterward so it neither
 * lingers in the address nor confuses routing.
 */

function base64UrlDecode(s: string): string {
  let t = s.replace(/-/g, '+').replace(/_/g, '/');
  while (t.length % 4) t += '=';
  return atob(t);
}

const SESSION_KEY = '__nnhk_cache';

(function seedHotkeysFromHash() {
  try {
    if (typeof window === 'undefined') return;
    const hash = window.location.hash || '';
    const m = hash.match(/__nnhk=([^&]+)/);
    let parsed: Record<string, unknown> | null = null;
    if (m) {
      parsed = JSON.parse(base64UrlDecode(m[1])) as Record<string, unknown>;
      // Cache for an in-session reload (F5): a plain reload navigates the stripped
      // URL (no hash), but sessionStorage survives within the WebView2 session.
      try {
        sessionStorage.setItem(SESSION_KEY, JSON.stringify(parsed));
      } catch {
        /* ignore */
      }
    } else {
      const cached = sessionStorage.getItem(SESSION_KEY);
      if (cached) parsed = JSON.parse(cached) as Record<string, unknown>;
    }
    if (!parsed) return;
    const w = window as unknown as { __NIUNIU_HOTKEYS__?: Record<string, unknown> };
    w.__NIUNIU_HOTKEYS__ = { ...(w.__NIUNIU_HOTKEYS__ || {}), ...parsed };
    if (m) {
      // The desktop appends only `#__nnhk=…` (withHotkeyHash strips any prior
      // fragment), so clearing the whole hash is safe and keeps the URL clean.
      window.history.replaceState(
        null,
        '',
        window.location.pathname + window.location.search,
      );
    }
  } catch {
    /* ignore — plain browser or malformed fragment */
  }
})();
