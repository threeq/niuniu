import { create } from 'zustand';
import {
  THEME_STORAGE_KEY,
  applyTheme,
  getSystemTheme,
  resolveTheme,
  type ResolvedTheme,
  type Theme,
} from '@/lib/theme';

interface ThemeState {
  theme: Theme;
  systemTheme: ResolvedTheme;
  resolvedTheme: ResolvedTheme;
  setTheme: (theme: Theme) => void;
  setSystemTheme: (systemTheme: ResolvedTheme) => void;
}

function readStoredTheme(): Theme {
  try {
    const raw = localStorage.getItem(THEME_STORAGE_KEY);
    if (raw === 'light' || raw === 'dark' || raw === 'system') return raw;
  } catch {
    /* localStorage blocked */
  }
  return 'system';
}

function writeStoredTheme(theme: Theme): void {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    /* localStorage blocked */
  }
}

const initialTheme = readStoredTheme();
const initialSystem = getSystemTheme();
const initialResolved = resolveTheme(initialTheme, initialSystem);

export const useThemeStore = create<ThemeState>((set, get) => ({
  theme: initialTheme,
  systemTheme: initialSystem,
  resolvedTheme: initialResolved,

  // User preference — persists to localStorage (raw string, see theme.ts).
  setTheme: (theme) => {
    const { systemTheme } = get();
    const resolvedTheme = resolveTheme(theme, systemTheme);
    writeStoredTheme(theme);
    applyTheme(resolvedTheme);
    set({ theme, resolvedTheme });
  },

  // Ambient OS signal — DO NOT persist. User preference stays 'system'
  // and merely derives through this value. Persisting here would convert
  // the ambient signal into a user choice, which is wrong.
  setSystemTheme: (systemTheme) => {
    const { theme } = get();
    const resolvedTheme = resolveTheme(theme, systemTheme);
    applyTheme(resolvedTheme);
    set({ systemTheme, resolvedTheme });
  },
}));
