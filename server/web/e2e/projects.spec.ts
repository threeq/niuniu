import { test, expect } from '@playwright/test';

test.describe('App Layout E2E Tests', () => {
  test('page loads without errors', async ({ page }) => {
    await page.goto('/');

    // Check that the page has loaded
    await expect(page).toHaveTitle(/Niuniu/);
  });

  test('header is visible', async ({ page }) => {
    await page.goto('/');

    // Header should be visible
    const header = page.locator('[data-testid="app-header"]');
    await expect(header).toBeVisible();
  });

  test('has navigation buttons', async ({ page }) => {
    await page.goto('/');

    // Should have navigation buttons
    const buttons = page.getByRole('button');
    await expect(buttons.first()).toBeVisible();
  });
});
