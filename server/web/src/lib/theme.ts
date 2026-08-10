export type Theme = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

/**
 * CRITICAL: stored as a RAW STRING (not JSON).
 * The inline bootstrap script in index.html reads this same key as a raw
 * string via `localStorage.getItem('niuniu.theme')`. Any change here (key
 * name, serialization) must be mirrored in index.html or FOUC returns.
 * This is why theme-store.ts does NOT use Zustand's `persist` middleware
 * (which would JSON.stringify the value).
 */
export const THEME_STORAGE_KEY = 'niuniu.theme';

export function getSystemTheme(): ResolvedTheme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function resolveTheme(theme: Theme, systemTheme: ResolvedTheme): ResolvedTheme {
  return theme === 'system' ? systemTheme : theme;
}

export function applyTheme(resolved: ResolvedTheme): void {
  const el = document.documentElement;
  el.classList.toggle('dark', resolved === 'dark');
  el.style.colorScheme = resolved;
  updateThemeColorMeta(resolved);
}

function updateThemeColorMeta(resolved: ResolvedTheme): void {
  // Dynamic override meta (no `media` attr) wins over the static pair in
  // index.html. Needed when user picks Light/Dark explicitly while the OS
  // is set to the opposite.
  let meta = document.querySelector<HTMLMetaElement>(
    'meta[name="theme-color"]:not([media])'
  );
  if (!meta) {
    meta = document.createElement('meta');
    meta.name = 'theme-color';
    document.head.appendChild(meta);
  }
  meta.content = resolved === 'dark' ? '#0b1220' : '#ffffff';
}
