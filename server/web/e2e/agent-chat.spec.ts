import { test, expect } from '@playwright/test';

test.describe('Agent Chat E2E Tests', () => {
  test('chat panel UI elements are present when workspace is selected', async ({ page }) => {
    // Navigate to the app
    await page.goto('/');

    // Wait for the app to load fully
    await page.waitForLoadState('networkidle');

    // Check header is visible with navigation buttons
    const header = page.locator('[data-testid="app-header"]');
    await expect(header).toBeVisible();

    // Count buttons in header - should have Projects, Workspaces, Repositories nav buttons
    const headerButtons = header.locator('button');
    const buttonCount = await headerButtons.count();
    console.log('Header buttons count:', buttonCount);
    expect(buttonCount).toBeGreaterThanOrEqual(3);

    // Click on the Workspaces nav button (second button in the header nav)
    // Nav items order: Projects (0), Workspaces (1), Repositories (2)
    const workspacesButton = headerButtons.nth(1);
    await expect(workspacesButton).toBeVisible();
    await workspacesButton.click();

    // Wait for workspace view to render
    await page.waitForTimeout(1000);

    // Check if we see the workspace list or empty state
    // The workspace sidebar should be visible
    const sidebar = page.locator('[data-testid="app-sidebar"]');
    await expect(sidebar).toBeVisible();

    // Try to find a workspace to click on
    // Look for workspace items which have cursor-pointer class
    const workspaceItems = page.locator('[class*="cursor-pointer"]');

    // Check if workspace list is empty (shows "暂无工作空间" message)
    const noWorkspacesText = page.getByText('暂无工作空间');

    if (await noWorkspacesText.isVisible().catch(() => false)) {
      // No workspaces available - just verify the empty state is shown
      console.log('No workspaces available - empty state shown');
    } else {
      // Workspaces exist - click on the first one
      const workspaceCount = await workspaceItems.count();
      console.log('Found workspace items:', workspaceCount);

      if (workspaceCount > 0) {
        // Click on the first workspace
        await workspaceItems.first().click();

        // Wait for the workspace detail view to load
        await page.waitForTimeout(1000);

        // Verify ChatPanel is visible by checking for "Agent Chat" header
        const chatHeader = page.getByText('Agent Chat');
        await expect(chatHeader).toBeVisible({ timeout: 5000 });

        // Verify textarea exists and is visible
        const textarea = page.locator('textarea[placeholder*="Ask Claude Code"]');
        await expect(textarea).toBeVisible();

        // Verify Send button exists and is visible (but initially disabled since textarea is empty)
        const sendButton = page.getByRole('button', { name: 'Send' });
        await expect(sendButton).toBeVisible();
        // Note: button is disabled when textarea is empty, so we check isDisabled first
        await expect(sendButton).toBeDisabled();

        // Type a test message (use type() instead of fill() for React controlled components)
        const testMessage = 'Hello, this is a test message';
        await textarea.click();
        await textarea.type(testMessage);

        // Verify the message is in the textarea
        await expect(textarea).toHaveValue(testMessage);

        // Now the send button should be enabled
        await expect(sendButton).toBeEnabled();

        // Click Send button
        await sendButton.click();

        // Verify the message appears in the chat (optimistic UI)
        // Use .first() because there might be multiple matches (textarea + chat message)
        await expect(page.getByText(testMessage).first()).toBeVisible({ timeout: 3000 });

        // Verify textarea is cleared after sending
        await expect(textarea).toHaveValue('');
      }
    }
  });

  test('Ctrl+Enter keyboard shortcut works for sending messages', async ({ page }) => {
    // Navigate to the app and switch to workspace view
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Click on Workspaces nav button
    const header = page.locator('[data-testid="app-header"]');
    const workspacesButton = header.locator('button').nth(1);
    await workspacesButton.click();

    await page.waitForTimeout(1000);

    // Try to find a workspace to click on
    const workspaceItems = page.locator('[class*="cursor-pointer"]');
    const noWorkspacesText = page.getByText('暂无工作空间');

    if (!(await noWorkspacesText.isVisible().catch(() => false))) {
      const workspaceCount = await workspaceItems.count();

      if (workspaceCount > 0) {
        // Click on the first workspace
        await workspaceItems.first().click();

        await page.waitForTimeout(1000);

        // Verify ChatPanel is visible
        const chatHeader = page.getByText('Agent Chat');
        await expect(chatHeader).toBeVisible({ timeout: 5000 });

        // Find textarea
        const textarea = page.locator('textarea[placeholder*="Ask Claude Code"]');
        await expect(textarea).toBeVisible();

        // Type a message (use type() for React controlled components)
        const testMessage = 'Test via Ctrl+Enter';
        await textarea.click();
        await textarea.type(testMessage);

        // Press Ctrl+Enter to send
        await textarea.press('Control+Enter');

        // Verify the message appears in the chat (use .first() for strict mode)
        await expect(page.getByText(testMessage).first()).toBeVisible({ timeout: 3000 });
      }
    }
  });
});
