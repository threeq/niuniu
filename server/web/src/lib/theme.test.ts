import { describe, it, expect, beforeEach } from 'vitest';
import {
  THEME_STORAGE_KEY,
  resolveTheme,
  applyTheme,
  getSystemTheme,
} from './theme';

describe('theme utils', () => {
  beforeEach(() => {
    document.documentElement.className = '';
    document.documentElement.style.colorScheme = '';
  });

  it('THEME_STORAGE_KEY is stable', () => {
    expect(THEME_STORAGE_KEY).toBe('niuniu.theme');
  });

  it('resolveTheme returns concrete value for light/dark', () => {
    expect(resolveTheme('light', 'dark')).toBe('light');
    expect(resolveTheme('dark', 'light')).toBe('dark');
  });

  it('resolveTheme follows system when theme is "system"', () => {
    expect(resolveTheme('system', 'dark')).toBe('dark');
    expect(resolveTheme('system', 'light')).toBe('light');
  });

  it('applyTheme toggles .dark class and color-scheme', () => {
    applyTheme('dark');
    expect(document.documentElement.classList.contains('dark')).toBe(true);
    expect(document.documentElement.style.colorScheme).toBe('dark');

    applyTheme('light');
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    expect(document.documentElement.style.colorScheme).toBe('light');
  });

  it('applyTheme updates dynamic <meta name="theme-color">', () => {
    // Clean any pre-existing dynamic meta
    document
      .querySelectorAll('meta[name="theme-color"]:not([media])')
      .forEach((m) => m.remove());

    applyTheme('dark');
    const meta = document.querySelector<HTMLMetaElement>(
      'meta[name="theme-color"]:not([media])'
    );
    expect(meta).not.toBeNull();
    expect(meta!.content).toBe('#0b1220');

    applyTheme('light');
    expect(meta!.content).toBe('#ffffff');
  });

  it('getSystemTheme reads prefers-color-scheme', () => {
    // jsdom defaults matchMedia to matches: false → 'light'
    expect(['light', 'dark']).toContain(getSystemTheme());
  });
});
