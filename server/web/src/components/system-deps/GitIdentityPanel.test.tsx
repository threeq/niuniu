import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { I18nextProvider } from 'react-i18next'
import i18n from '@/i18n'
import { GitIdentityPanel } from './GitIdentityPanel'

vi.mock('@/lib/api', () => ({
  api: { setGitIdentity: vi.fn().mockResolvedValue(undefined) },
}))

const wrap = (ui: React.ReactElement) => (
  <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>
)

describe('GitIdentityPanel', () => {
  beforeEach(() => vi.clearAllMocks())

  it('shows view mode when configured', () => {
    render(
      wrap(
        <GitIdentityPanel
          initial={{ name: 'Alice', email: 'a@e.com', configured: true }}
          onSaved={() => {}}
        />,
      ),
    )
    expect(screen.getByText(/Alice/)).toBeInTheDocument()
    expect(screen.getByText(/a@e\.com/)).toBeInTheDocument()
  })

  it('shows edit mode when unconfigured and hides cancel', () => {
    render(
      wrap(
        <GitIdentityPanel
          initial={{ name: '', email: '', configured: false }}
          onSaved={() => {}}
        />,
      ),
    )
    expect(screen.getByPlaceholderText(/name/i)).toBeInTheDocument()
    expect(screen.queryByText(/取消|Cancel/)).not.toBeInTheDocument()
  })

  it('calls api.setGitIdentity on save', async () => {
    const { api } = await import('@/lib/api')
    const onSaved = vi.fn()
    render(
      wrap(
        <GitIdentityPanel
          initial={{ name: '', email: '', configured: false }}
          onSaved={onSaved}
        />,
      ),
    )
    fireEvent.change(screen.getByPlaceholderText(/name/i), { target: { value: 'Bob' } })
    fireEvent.change(screen.getByPlaceholderText(/email/i), { target: { value: 'b@e.com' } })
    fireEvent.click(screen.getByText(/保存|Save/))
    await waitFor(() => expect(api.setGitIdentity).toHaveBeenCalledWith('Bob', 'b@e.com'))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
  })
})
