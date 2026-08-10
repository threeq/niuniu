import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import i18n from '@/i18n';
import type { Project } from '@/types/api';
import type { ImBotPendingChat } from '@/types/imbot';

// Mock the owner-level REST wrapper so we can assert the routed project_id.
const approveChatMock = vi.fn();
const reassignChatMock = vi.fn();
vi.mock('@/lib/imbot-api', () => ({
  imbotOwnerApi: {
    approveChat: (chatId: number, projectId: number) => approveChatMock(chatId, projectId),
    reassignChat: (chatId: number, projectId: number) => reassignChatMock(chatId, projectId),
  },
}));

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { OwnerPendingChats } from './owner-pending-chats';

const projects: Project[] = [
  { id: 11, name: 'Alpha', description: null, status: 'active', created_at: '', updated_at: '' },
  { id: 22, name: 'Beta', description: null, status: 'active', created_at: '', updated_at: '' },
];

const pending: ImBotPendingChat[] = [
  { id: 5, channel_id: 1, chat_ext_id: 'oc_abc', chat_name: 'Design chat', status: 'pending', project_id: null },
];

function wrap(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>
    </QueryClientProvider>
  );
}

describe('OwnerPendingChats', () => {
  beforeEach(() => {
    approveChatMock.mockReset();
    approveChatMock.mockResolvedValue({ id: 5, status: 'active' });
  });

  it('renders each pending chat with a project picker + approve button', () => {
    render(wrap(<OwnerPendingChats pending={pending} projects={projects} />));
    expect(screen.getByText('Design chat')).toBeInTheDocument();
    expect(screen.getByText('oc_abc')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Approve|批准/ })).toBeInTheDocument();
  });

  it('approves routing to the default (first) writable project when none is re-picked', async () => {
    render(wrap(<OwnerPendingChats pending={pending} projects={projects} />));
    fireEvent.click(screen.getByRole('button', { name: /Approve|批准/ }));
    await waitFor(() => expect(approveChatMock).toHaveBeenCalledTimes(1));
    // chatId 5, defaulting to projects[0].id (11).
    expect(approveChatMock).toHaveBeenCalledWith(5, 11);
  });

  it('disables approval and warns when there are no writable projects', () => {
    render(wrap(<OwnerPendingChats pending={pending} projects={[]} />));
    const btn = screen.getByRole('button', { name: /Approve|批准/ }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(
      screen.getByText(/no writable projects|没有可写入的项目|沒有可寫入的專案/i),
    ).toBeInTheDocument();
  });
});
