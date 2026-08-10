import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, beforeEach } from 'vitest';
import { ThemeToggle } from './theme-toggle';
import { useThemeStore } from '@/stores/theme-store';

describe('<ThemeToggle />', () => {
  beforeEach(() => {
    localStorage.clear();
    useThemeStore.setState({ theme: 'system', systemTheme: 'light', resolvedTheme: 'light' });
    document.documentElement.className = '';
  });

  it('renders a button with aria-label', () => {
    render(<ThemeToggle />);
    expect(screen.getByRole('button', { name: /切换主题/ })).toBeInTheDocument();
  });

  it('opens dropdown and switches theme to dark', async () => {
    const user = userEvent.setup();
    render(<ThemeToggle />);

    await user.click(screen.getByRole('button', { name: /切换主题/ }));
    await user.click(screen.getByRole('menuitemradio', { name: /深色/ }));

    expect(useThemeStore.getState().theme).toBe('dark');
    expect(document.documentElement.classList.contains('dark')).toBe(true);
  });

  it('shows the current selection as checked', async () => {
    const user = userEvent.setup();
    useThemeStore.setState({ theme: 'dark', resolvedTheme: 'dark' });

    render(<ThemeToggle />);
    await user.click(screen.getByRole('button', { name: /切换主题/ }));

    const darkItem = screen.getByRole('menuitemradio', { name: /深色/ });
    expect(darkItem).toHaveAttribute('aria-checked', 'true');
  });
});
