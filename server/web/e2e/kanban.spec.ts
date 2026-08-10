import { test, expect } from '@playwright/test';

test.describe('Kanban Board E2E Tests', () => {
  test('shows empty state when no project is selected', async ({ page }) => {
    await page.goto('/');

    // Should show empty state text
    await expect(page.getByText(/未选择项目|暂无|项目/).first()).toBeVisible({ timeout: 10000 });
  });

  test('header has view switcher buttons', async ({ page }) => {
    await page.goto('/');

    // Should have multiple buttons in header
    const buttons = page.locator('[data-testid="app-header"] button');
    await expect(buttons.first()).toBeVisible({ timeout: 10000 });
  });
});

test.describe('Responsive Design E2E Tests', () => {
  test('header visible on desktop viewport', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/');

    // Header should be visible
    await expect(page.locator('[data-testid="app-header"]')).toBeVisible({ timeout: 10000 });
  });
});
