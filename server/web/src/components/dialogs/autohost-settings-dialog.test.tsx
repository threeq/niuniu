import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { it, expect, vi, beforeEach } from 'vitest';
import { AutohostSettingsDialog } from './autohost-settings-dialog';
import { api } from '@/lib/api';

vi.mock('@/lib/api');

const mockedGet = vi.mocked(api.get);
const mockedPut = vi.mocked(api.put);

beforeEach(() => {
  vi.clearAllMocks();
  mockedPut.mockResolvedValue({} as never);
});

it('loads existing env values into form fields when opened', async () => {
  mockedGet.mockResolvedValue({
    env: {
      NIUNIU_AUTOHOST_BUDGET: '20',
      NIUNIU_AUTOHOST_ERROR_BUDGET: '5',
      NIUNIU_AUTOHOST_CONTINUE_PROMPT: 'my custom continue',
      NIUNIU_AUTOHOST_RECOVER_PROMPT: 'my custom recover',
    },
  } as never);

  render(<AutohostSettingsDialog open workspaceId="42" onOpenChange={() => {}} />);

  await waitFor(() => {
    expect((screen.getByLabelText(/续跑次数/) as HTMLInputElement).value).toBe('20');
  });
  expect((screen.getByLabelText(/错误重试次数/) as HTMLInputElement).value).toBe('5');
  expect((screen.getByLabelText(/续跑提示词/) as HTMLTextAreaElement).value).toBe('my custom continue');
  expect((screen.getByLabelText(/恢复提示词/) as HTMLTextAreaElement).value).toBe('my custom recover');
});

it('falls back to defaults when env is empty', async () => {
  mockedGet.mockResolvedValue({ env: {} } as never);
  render(<AutohostSettingsDialog open workspaceId="42" onOpenChange={() => {}} />);

  await waitFor(() => {
    expect((screen.getByLabelText(/续跑次数/) as HTMLInputElement).value).toBe('12');
  });
  expect((screen.getByLabelText(/错误重试次数/) as HTMLInputElement).value).toBe('3');
});

it('saves trimmed prompts and normalized budgets, then closes', async () => {
  mockedGet.mockResolvedValue({ env: {} } as never);
  const onOpenChange = vi.fn();
  render(<AutohostSettingsDialog open workspaceId="42" onOpenChange={onOpenChange} />);

  await waitFor(() => screen.getByLabelText(/续跑次数/));

  fireEvent.change(screen.getByLabelText(/续跑次数/), { target: { value: '7' } });
  fireEvent.change(screen.getByLabelText(/错误重试次数/), { target: { value: '2' } });
  fireEvent.change(screen.getByLabelText(/续跑提示词/), { target: { value: '  custom continue with spaces  ' } });

  fireEvent.click(screen.getByRole('button', { name: /保存/ }));

  await waitFor(() =>
    expect(mockedPut).toHaveBeenCalledWith(
      '/workspaces/42/env',
      expect.objectContaining({
        env: expect.objectContaining({
          NIUNIU_AUTOHOST_BUDGET: '7',
          NIUNIU_AUTOHOST_ERROR_BUDGET: '2',
          NIUNIU_AUTOHOST_CONTINUE_PROMPT: 'custom continue with spaces',
          NIUNIU_AUTOHOST_RECOVER_PROMPT: '',
        }),
      })
    )
  );
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

it('rejects non-integer budget input and falls back to defaults on save', async () => {
  mockedGet.mockResolvedValue({ env: {} } as never);
  render(<AutohostSettingsDialog open workspaceId="42" onOpenChange={() => {}} />);

  await waitFor(() => screen.getByLabelText(/续跑次数/));

  // -5 fails the /^\d+$/ test → falls back to default '12'.
  // "abc" likewise → '3' for error budget.
  fireEvent.change(screen.getByLabelText(/续跑次数/), { target: { value: '-5' } });
  fireEvent.change(screen.getByLabelText(/错误重试次数/), { target: { value: 'abc' } });
  fireEvent.click(screen.getByRole('button', { name: /保存/ }));

  await waitFor(() =>
    expect(mockedPut).toHaveBeenCalledWith(
      '/workspaces/42/env',
      expect.objectContaining({
        env: expect.objectContaining({
          NIUNIU_AUTOHOST_BUDGET: '12',
          NIUNIU_AUTOHOST_ERROR_BUDGET: '3',
        }),
      })
    )
  );
});

it('reset-to-default button populates textarea with the bundled default prompt', async () => {
  mockedGet.mockResolvedValue({
    env: { NIUNIU_AUTOHOST_CONTINUE_PROMPT: 'overridden' },
  } as never);
  render(<AutohostSettingsDialog open workspaceId="42" onOpenChange={() => {}} />);

  const textarea = await screen.findByLabelText(/续跑提示词/);
  expect((textarea as HTMLTextAreaElement).value).toBe('overridden');

  // First "恢复默认" button targets the continue prompt section (it appears
  // first in DOM order, before the recover prompt section).
  const resetButtons = screen.getAllByRole('button', { name: /恢复默认/ });
  fireEvent.click(resetButtons[0]);

  expect((textarea as HTMLTextAreaElement).value).toContain('继续推进当前任务的剩余步骤');
  expect((textarea as HTMLTextAreaElement).value).toContain('[AUTOHOST_DONE]');
});

it('cancel button closes without saving', async () => {
  mockedGet.mockResolvedValue({ env: {} } as never);
  const onOpenChange = vi.fn();
  render(<AutohostSettingsDialog open workspaceId="42" onOpenChange={onOpenChange} />);

  await waitFor(() => screen.getByLabelText(/续跑次数/));

  fireEvent.change(screen.getByLabelText(/续跑次数/), { target: { value: '99' } });
  fireEvent.click(screen.getByRole('button', { name: /取消/ }));

  expect(mockedPut).not.toHaveBeenCalled();
  expect(onOpenChange).toHaveBeenCalledWith(false);
});
