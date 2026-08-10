import { test, expect } from '@playwright/test';

test.describe('theme switching', () => {
  test('switching to dark persists across reload', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: '切换主题' }).click();
    await page.getByRole('menuitemradio', { name: '深色' }).click();

    await expect(page.locator('html')).toHaveClass(/\bdark\b/);

    await page.reload();
    await expect(page.locator('html')).toHaveClass(/\bdark\b/);
  });

  test('system mode follows prefers-color-scheme', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: '切换主题' }).click();
    await page.getByRole('menuitemradio', { name: '跟随系统' }).click();

    await page.emulateMedia({ colorScheme: 'dark' });
    await expect(page.locator('html')).toHaveClass(/\bdark\b/);

    await page.emulateMedia({ colorScheme: 'light' });
    await expect(page.locator('html')).not.toHaveClass(/\bdark\b/);
  });

  test('light mode removes .dark class', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: '切换主题' }).click();
    await page.getByRole('menuitemradio', { name: '浅色' }).click();

    await expect(page.locator('html')).not.toHaveClass(/\bdark\b/);
  });
});
