import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { useThemeStore } from './theme-store';
import { THEME_STORAGE_KEY } from '@/lib/theme';

describe('theme-store', () => {
  beforeEach(() => {
    localStorage.clear();
    useThemeStore.setState({ theme: 'system', systemTheme: 'light' });
    document.documentElement.className = '';
    document.documentElement.style.colorScheme = '';
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('default theme is "system"', () => {
    expect(useThemeStore.getState().theme).toBe('system');
  });

  it('setTheme updates theme, persists to localStorage, and applies .dark class', () => {
    useThemeStore.getState().setTheme('dark');

    expect(useThemeStore.getState().theme).toBe('dark');
    expect(useThemeStore.getState().resolvedTheme).toBe('dark');
    expect(document.documentElement.classList.contains('dark')).toBe(true);

    const raw = localStorage.getItem(THEME_STORAGE_KEY);
    expect(raw).toBe('dark');
  });

  it('setTheme("light") clears .dark class', () => {
    useThemeStore.getState().setTheme('dark');
    useThemeStore.getState().setTheme('light');
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    expect(useThemeStore.getState().resolvedTheme).toBe('light');
  });

  it('resolvedTheme follows systemTheme when theme is "system"', () => {
    useThemeStore.setState({ theme: 'system', systemTheme: 'dark' });
    useThemeStore.getState().setTheme('system');
    expect(useThemeStore.getState().resolvedTheme).toBe('dark');

    useThemeStore.getState().setSystemTheme('light');
    expect(useThemeStore.getState().resolvedTheme).toBe('light');
  });

  it('persists raw string (not JSON) under niuniu.theme', () => {
    useThemeStore.getState().setTheme('dark');
    // raw-string contract — must match inline FOUC script
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark');
  });

  it('setSystemTheme does NOT write to localStorage (ambient signal)', () => {
    useThemeStore.getState().setTheme('system');
    localStorage.clear();

    useThemeStore.getState().setSystemTheme('dark');
    expect(useThemeStore.getState().resolvedTheme).toBe('dark');
    // Ambient invariant: system theme changes must not become user preference
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBeNull();
  });
});
