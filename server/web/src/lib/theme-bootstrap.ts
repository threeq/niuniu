import { useThemeStore } from '@/stores/theme-store';
import { applyTheme } from './theme';

// Import-time side effects: subscribe to system theme changes and re-apply
// the initial theme. The inline <script> in index.html already applied .dark
// to avoid FOUC; this module keeps theme in sync with OS after React mounts.
if (typeof window !== 'undefined' && window.matchMedia) {
  const mq = window.matchMedia('(prefers-color-scheme: dark)');

  const handler = (event: MediaQueryListEvent) => {
    useThemeStore.getState().setSystemTheme(event.matches ? 'dark' : 'light');
  };

  mq.addEventListener('change', handler);

  // Re-apply on mount so the <html> class matches the store's resolvedTheme
  // even if the inline script read stale data for any reason.
  applyTheme(useThemeStore.getState().resolvedTheme);
}
