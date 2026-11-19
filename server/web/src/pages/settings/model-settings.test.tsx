import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ModelSettings } from './model-settings'
import * as modelApi from '@/lib/model-api'
import type { ModelBudget, ModelItem } from '@/types/model'

vi.mock('@/lib/model-api', () => ({
  listModels: vi.fn(),
  upsertModel: vi.fn(),
  updateModel: vi.fn(),
  deleteModel: vi.fn(),
  refreshPreview: vi.fn(),
  importModels: vi.fn(),
  getBudget: vi.fn(),
  getUsage: vi.fn(),
  setBudget: vi.fn(),
  setProjectBudget: vi.fn(),
  deleteProjectBudget: vi.fn(),
}))

vi.mock('@/lib/hooks/use-projects', () => ({
  useProjects: () => ({
    projects: [{ id: 7, name: 'Alpha' }],
    isLoading: false,
  }),
}))

const systemModel: ModelItem = {
  id: 1,
  owner_type: 'user',
  owner_id: 0,
  name: 'glm-4',
  display_name: 'GLM-4',
  enabled: true,
  source: 'builtin',
}

const ownerModel: ModelItem = {
  id: 2,
  owner_type: 'user',
  owner_id: 2,
  name: 'my-model',
  display_name: 'My Model',
  enabled: true,
  source: 'manual',
}

const budget: ModelBudget = {
  owner_type: 'user',
  owner_id: 2,
  monthly_tokens: 1000,
  used_tokens: 850,
  period: '2026-11',
  projects: [{ project_id: 7, monthly_tokens: 100, used_tokens: 120 }],
}

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <ModelSettings />
    </QueryClientProvider>
  )
}

describe('ModelSettings', () => {
  beforeEach(() => {
    vi.mocked(modelApi.listModels).mockResolvedValue([systemModel, ownerModel])
    vi.mocked(modelApi.upsertModel).mockResolvedValue(systemModel)
    vi.mocked(modelApi.updateModel).mockResolvedValue(ownerModel)
    vi.mocked(modelApi.deleteModel).mockResolvedValue(undefined)
    vi.mocked(modelApi.getBudget).mockResolvedValue(budget)
    vi.mocked(modelApi.setBudget).mockResolvedValue(undefined)
    vi.mocked(modelApi.setProjectBudget).mockResolvedValue(undefined)
    vi.mocked(modelApi.deleteProjectBudget).mockResolvedValue(undefined)
  })

  it('lists system and owner rows with system rows read-only', async () => {
    renderPage()

    await screen.findByText('glm-4')
    expect(screen.getByText('my-model')).toBeInTheDocument()

    // System rows can only be hidden.
    expect(screen.getByRole('button', { name: '隐藏' })).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: '编辑 glm-4 的显示名' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: '删除 glm-4' })
    ).not.toBeInTheDocument()

    // Owner rows are editable.
    expect(
      screen.getByRole('switch', { name: '切换 my-model 的启用状态' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: '删除 my-model' })
    ).toBeInTheDocument()
  })

  it('hides a system model by creating a disabled owner override', async () => {
    renderPage()
    await screen.findByText('glm-4')

    fireEvent.click(screen.getByRole('button', { name: '隐藏' }))

    await waitFor(() =>
      expect(modelApi.upsertModel).toHaveBeenCalledWith('glm-4', 'GLM-4', false)
    )
  })

  it('filters the library by search', async () => {
    renderPage()
    await screen.findByText('glm-4')

    fireEvent.change(screen.getByPlaceholderText('搜索模型'), {
      target: { value: 'my-model' },
    })

    expect(screen.queryByText('glm-4')).not.toBeInTheDocument()
    expect(screen.getByText('my-model')).toBeInTheDocument()
  })

  it('adds a model from the dialog', async () => {
    renderPage()
    await screen.findByText('glm-4')

    fireEvent.click(screen.getByRole('button', { name: '添加模型' }))
    fireEvent.change(screen.getByLabelText('模型名'), {
      target: { value: 'new-model' },
    })
    fireEvent.change(screen.getByLabelText('显示名'), {
      target: { value: 'New Model' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() =>
      expect(modelApi.upsertModel).toHaveBeenCalledWith(
        'new-model',
        'New Model',
        true
      )
    )
  })

  it('shows the usage total and per-model breakdown', async () => {
    vi.mocked(modelApi.getUsage).mockImplementation(async (groupBy) => ({
      total_tokens: 500,
      requests: 5,
      ...(groupBy === 'model'
        ? {
            by_model: [{ key: 'usage-model', total_tokens: 500, requests: 5 }],
          }
        : {}),
    }))
    renderPage()
    await screen.findByText('glm-4')

    fireEvent.click(screen.getByRole('tab', { name: '用量' }))

    await screen.findByText('usage-model')
    expect(screen.getByText('总 token')).toBeInTheDocument()
  })

  it('shows owner usage progress and saves a new cap', async () => {
    renderPage()
    await screen.findByText('glm-4')

    fireEvent.click(screen.getByRole('tab', { name: '预算' }))

    await screen.findByText(/本月已用/)
    expect(screen.getByRole('progressbar')).toHaveAttribute(
      'aria-valuenow',
      '85'
    )

    fireEvent.change(screen.getByLabelText('月度上限（token）'), {
      target: { value: '2000' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(modelApi.setBudget).toHaveBeenCalledWith(2000))
  })

  it('renders project soft caps with an exceeded status', async () => {
    renderPage()
    await screen.findByText('glm-4')

    fireEvent.click(screen.getByRole('tab', { name: '预算' }))

    await screen.findByText('Alpha')
    expect(screen.getByText('已超限')).toBeInTheDocument()
  })
})
