import { test, expect } from '@playwright/test'

// These E2E tests require a fully running niuniu dev server with a seeded
// workspace + agents AND a live Claude CLI session. They exist as skeleton
// specs documenting the expected verification flow; remove `.skip` once the
// harness can simulate SSE events via a debug hook.
test.describe('Agent Tool Monitoring', () => {
  test.skip('Agent tool dispatch shows subagent card in Team panel', async ({ page }) => {
    await page.goto('/workspaces/1')
    await page.getByTestId('team-panel').waitFor()

    // Inject the same SSE event niuniu-server would publish when the Claude
    // CLI emits an Agent tool_use. Requires window.__niuniuTest.simulateEvent
    // to be exposed by dev builds — wire this up first if not present.
    await page.evaluate(() => {
      const w = window as unknown as { __niuniuTest?: { simulateEvent?: (ev: unknown) => void } }
      w.__niuniuTest?.simulateEvent?.({
        type: 'team:agent_dispatched',
        data: {
          agentDispatched: {
            workspaceAgentId: 999,
            parentAgentId: 1,
            subagentType: 'researcher',
            subagentDescription: 'look up docs',
          },
        },
      })
    })

    await expect(page.locator('[data-agent-id="999"]')).toBeVisible()
    await expect(page.locator('[data-agent-id="999"]')).toContainText('researcher')

    // Completion event
    await page.evaluate(() => {
      const w = window as unknown as { __niuniuTest?: { simulateEvent?: (ev: unknown) => void } }
      w.__niuniuTest?.simulateEvent?.({
        type: 'team:agent_completed',
        data: { agentCompleted: { workspaceAgentId: 999, isError: false, exitedAt: Date.now() } },
      })
    })

    // Parent card shows "▶ 1 dispatched" button after the completed subagent is auto-folded
    await expect(page.locator('[data-agent-id="1"]').getByText(/dispatched/)).toBeVisible()
  })

  test.skip('inbox_sent triggers animated edge between sender and receiver', async ({ page }) => {
    await page.goto('/workspaces/1')
    await page.getByTestId('team-panel').waitFor()

    // Assumes two workspace_agents already rendered: id=1 (coordinator), id=2 (worker)
    await page.evaluate(() => {
      const w = window as unknown as { __niuniuTest?: { simulateEvent?: (ev: unknown) => void } }
      w.__niuniuTest?.simulateEvent?.({
        type: 'team:inbox_sent',
        data: { inboxSent: { workspaceId: 1, fromAgentId: 1, toAgentId: 2, subject: 'do this' } },
      })
    })

    // Edge appears within 500ms
    await expect(page.locator('svg line').first()).toBeVisible({ timeout: 500 })
    // Edge fades within ~2.2s
    await page.waitForTimeout(2200)
    await expect(page.locator('svg line')).toHaveCount(0)
  })
})
