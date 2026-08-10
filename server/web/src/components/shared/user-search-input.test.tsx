import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react';
import { UserSearchInput } from './user-search-input';
import type { SelectedUser } from '@/types/org';

vi.mock('@/lib/api', () => ({
  api: { searchUsers: vi.fn() },
}));
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }));

import { api } from '@/lib/api';
import { toast } from 'sonner';

const mockedSearch = vi.mocked(api.searchUsers);
const mockedToast = vi.mocked(toast.error);

describe('UserSearchInput', () => {
  beforeEach(() => {
    mockedSearch.mockReset();
    mockedToast.mockReset();
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function renderInput(value: SelectedUser | null = null) {
    const onChange = vi.fn();
    const utils = render(
      <UserSearchInput orgId={1} value={value} onChange={onChange} placeholder="搜索" />
    );
    return { ...utils, onChange };
  }

  it('shows hint when input is empty and focused', async () => {
    renderInput();
    const input = screen.getByPlaceholderText('搜索');
    fireEvent.focus(input);
    expect(await screen.findByText(/请输入用户名或姓名搜索/)).toBeInTheDocument();
  });

  it('debounces, calls api.searchUsers, shows results', async () => {
    mockedSearch.mockResolvedValueOnce({
      users: [
        { id: 1, username: 'alice', display_name: 'Alice' },
        { id: 2, username: 'alex', display_name: 'Alex' },
      ],
      total: 2,
    });
    renderInput();
    const input = screen.getByPlaceholderText('搜索');
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: 'al' } });
    expect(mockedSearch).not.toHaveBeenCalled();
    await act(async () => { vi.advanceTimersByTime(300); });
    await waitFor(() => expect(mockedSearch).toHaveBeenCalledWith(1, 'al'));
    expect(await screen.findByText('alice')).toBeInTheDocument();
    expect(await screen.findByText('alex')).toBeInTheDocument();
  });

  it('shows truncation notice when total > results', async () => {
    mockedSearch.mockResolvedValueOnce({
      users: [{ id: 1, username: 'a1', display_name: 'A1' }],
      total: 25,
    });
    renderInput();
    fireEvent.focus(screen.getByPlaceholderText('搜索'));
    fireEvent.change(screen.getByPlaceholderText('搜索'), { target: { value: 'a' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    expect(await screen.findByText(/显示前 \d+ 条/)).toBeInTheDocument();
  });

  it('selecting a candidate fires onChange', async () => {
    mockedSearch.mockResolvedValueOnce({
      users: [{ id: 7, username: 'bob', display_name: 'Bob' }],
      total: 1,
    });
    const { onChange } = renderInput();
    fireEvent.focus(screen.getByPlaceholderText('搜索'));
    fireEvent.change(screen.getByPlaceholderText('搜索'), { target: { value: 'bo' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    fireEvent.mouseDown(await screen.findByText('bob'));
    expect(onChange).toHaveBeenCalledWith({ id: 7, username: 'bob', display_name: 'Bob' });
  });

  it('clear button resets selection', () => {
    const { onChange } = renderInput({ id: 7, username: 'bob', display_name: 'Bob' });
    fireEvent.click(screen.getByRole('button', { name: /clear/i }));
    expect(onChange).toHaveBeenCalledWith(null);
  });

  it('keyboard ↓↓Enter selects highlighted', async () => {
    mockedSearch.mockResolvedValueOnce({
      users: [
        { id: 1, username: 'alice', display_name: 'Alice' },
        { id: 2, username: 'alex', display_name: 'Alex' },
      ],
      total: 2,
    });
    const { onChange } = renderInput();
    const input = screen.getByPlaceholderText('搜索');
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: 'al' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    await screen.findByText('alice');
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledWith({ id: 2, username: 'alex', display_name: 'Alex' });
  });

  it('lone exact-match-username + Enter shortcut', async () => {
    mockedSearch.mockResolvedValueOnce({
      users: [{ id: 9, username: 'carol', display_name: 'Carol' }],
      total: 1,
    });
    const { onChange } = renderInput();
    const input = screen.getByPlaceholderText('搜索');
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: 'carol' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    await screen.findByText('carol');
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledWith({ id: 9, username: 'carol', display_name: 'Carol' });
  });

  it('discards stale response when newer query in flight', async () => {
    let resolveFirst: (v: { users: SelectedUser[]; total: number }) => void = () => {};
    let resolveSecond: (v: { users: SelectedUser[]; total: number }) => void = () => {};
    mockedSearch
      .mockImplementationOnce(() => new Promise((r) => { resolveFirst = r; }))
      .mockImplementationOnce(() => new Promise((r) => { resolveSecond = r; }));
    renderInput();
    const input = screen.getByPlaceholderText('搜索');
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: 'a' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    fireEvent.change(input, { target: { value: 'al' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    resolveSecond({ users: [{ id: 2, username: 'alex', display_name: 'Alex' }], total: 1 });
    await waitFor(() => expect(screen.queryByText('alex')).toBeInTheDocument());
    resolveFirst({ users: [{ id: 1, username: 'alice', display_name: 'Alice' }], total: 1 });
    await act(async () => {});
    expect(screen.queryByText('alice')).not.toBeInTheDocument();
  });

  it('network error toasts and shows error state', async () => {
    mockedSearch.mockRejectedValueOnce(new Error('boom'));
    renderInput();
    const input = screen.getByPlaceholderText('搜索');
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: 'al' } });
    await act(async () => { vi.advanceTimersByTime(300); });
    await waitFor(() => expect(mockedToast).toHaveBeenCalled());
    expect(await screen.findByText(/搜索失败/)).toBeInTheDocument();
  });
});
