import { describe, expect, it, beforeEach, vi } from 'vitest'
import type { QueryClient } from '@tanstack/react-query'

vi.mock('@/router', () => ({
  router: { navigate: vi.fn(), state: { location: { pathname: '/' } } },
}))

const successMock = vi.fn()
const infoMock = vi.fn()
const errorMock = vi.fn()
const warningMock = vi.fn()
const dismissMock = vi.fn()

vi.mock('sonner', () => ({
  toast: Object.assign(
    (msg: string, opts?: unknown) => infoMock(msg, opts),
    {
      success: (msg: string, opts?: unknown) => successMock(msg, opts),
      info: (msg: string, opts?: unknown) => infoMock(msg, opts),
      error: (msg: string, opts?: unknown) => errorMock(msg, opts),
      warning: (msg: string, opts?: unknown) => warningMock(msg, opts),
      dismiss: (id?: unknown) => dismissMock(id),
    }
  ),
}))

vi.mock('@/lib/notification-sound', () => ({ playNotificationSound: vi.fn() }))

const settingsState = {
  toastEnabled: true,
  soundEnabled: false,
  soundName: 'default',
  volume: 1,
  alertScope: 'all',
}
vi.mock('@/stores/notification-settings-store', () => ({
  useNotificationSettings: { getState: () => settingsState },
}))

import { dispatchNotification, __resetForTests } from './notification-dispatcher'

const qc = {
  invalidateQueries: vi.fn(),
  cancelQueries: vi.fn(),
  removeQueries: vi.fn(),
} as unknown as QueryClient

beforeEach(() => {
  __resetForTests()
  successMock.mockReset()
  infoMock.mockReset()
  errorMock.mockReset()
  warningMock.mockReset()
  dismissMock.mockReset()
})

describe('model_budget toasts', () => {
  it('warns once for an exceeded owner budget in the same period', () => {
    const n = {
      topic: 'model_budget',
      action: 'exceeded',
      extra: { period: '2026-11', used: 1200, limit: 1000 },
    }
    dispatchNotification(qc, n)
    dispatchNotification(qc, n)

    expect(errorMock).toHaveBeenCalledTimes(1)
    expect(errorMock.mock.calls[0][0]).toContain('2026-11')
  })

  it('warns once per period — a new period alerts again', () => {
    dispatchNotification(qc, {
      topic: 'model_budget',
      action: 'exceeded',
      extra: { period: '2026-11', used: 1200, limit: 1000 },
    })
    dispatchNotification(qc, {
      topic: 'model_budget',
      action: 'exceeded',
      extra: { period: '2026-12', used: 1200, limit: 1000 },
    })

    expect(errorMock).toHaveBeenCalledTimes(2)
  })

  it('raises a project warning once per project and period', () => {
    const n = {
      topic: 'model_budget',
      action: 'project_warning',
      extra: { period: '2026-11', used: 120, limit: 100, project_id: 7 },
    }
    dispatchNotification(qc, n)
    dispatchNotification(qc, n)

    expect(warningMock).toHaveBeenCalledTimes(1)
  })

  it('refreshes the budget and usage queries', () => {
    dispatchNotification(qc, {
      topic: 'model_budget',
      action: 'exceeded',
      extra: { period: '2026-11', used: 1200, limit: 1000 },
    })

    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['model-budget'] })
    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['model-usage'] })
  })
})
