import { useEffect } from 'react';

// Wails webview doesn't bind Ctrl+R / Cmd+R to page reload by default, so users
// have no way to force a refresh when the SPA gets stuck on stale data. This
// hook installs a global keydown handler that intercepts the shortcut and calls
// window.location.reload() — wiping React Query cache and re-running boot.
export function useReloadShortcut() {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'r') {
        e.preventDefault();
        window.location.reload();
      }
    };

    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);
}
