import React from 'react';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

vi.mock('@/lib/pinned-messages-api', () => ({
  listPinnedMessages: vi.fn(),
  deletePinnedMessage: vi.fn(),
}));

vi.mock('@/lib/scroll-to-chat-message', () => ({
  scrollToChatMessage: vi.fn(() => true),
}));

import { PinnedMessagesPanel } from './pinned-messages-panel';
import {
  listPinnedMessages,
  deletePinnedMessage,
  type PinnedMessage,
} from '@/lib/pinned-messages-api';
import { scrollToChatMessage } from '@/lib/scroll-to-chat-message';

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const pin = (over: Partial<PinnedMessage>): PinnedMessage => ({
  id: 1,
  workspace_id: 42,
  message_id: 'm1',
  role: 'assistant',
  preview: 'hello world',
  created_at: 1_700_000_000_000,
  ...over,
});

describe('PinnedMessagesPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders an empty state when there are no pins', async () => {
    vi.mocked(listPinnedMessages).mockResolvedValue([]);
    render(withQuery(<PinnedMessagesPanel workspaceId="42" />));
    await waitFor(() => expect(listPinnedMessages).toHaveBeenCalledWith('42'));
    // The preview text of any item must not be present.
    expect(screen.queryByText('hello world')).not.toBeInTheDocument();
  });

  it('renders pinned message previews', async () => {
    vi.mocked(listPinnedMessages).mockResolvedValue([
      pin({ id: 1, message_id: 'm1', preview: 'first pin' }),
      pin({ id: 2, message_id: 'm2', preview: 'second pin' }),
    ]);
    render(withQuery(<PinnedMessagesPanel workspaceId="42" />));
    expect(await screen.findByText('first pin')).toBeInTheDocument();
    expect(screen.getByText('second pin')).toBeInTheDocument();
  });

  it('locates a message when its row is clicked', async () => {
    vi.mocked(listPinnedMessages).mockResolvedValue([pin({ message_id: 'mX', preview: 'jump to me' })]);
    render(withQuery(<PinnedMessagesPanel workspaceId="42" />));
    fireEvent.click(await screen.findByText('jump to me'));
    expect(scrollToChatMessage).toHaveBeenCalledWith('mX');
  });

  it('unpins a message when its remove button is clicked', async () => {
    vi.mocked(listPinnedMessages).mockResolvedValue([pin({ id: 7, preview: 'remove me' })]);
    vi.mocked(deletePinnedMessage).mockResolvedValue();
    render(withQuery(<PinnedMessagesPanel workspaceId="42" />));
    await screen.findByText('remove me');
    // The unpin button is the only button whose accessible action is delete;
    // it sits as a sibling of the row button. Grab all buttons and click the
    // last (the X) — the row button is first.
    const buttons = screen.getAllByRole('button');
    fireEvent.click(buttons[buttons.length - 1]);
    await waitFor(() => expect(deletePinnedMessage).toHaveBeenCalledWith('42', 7));
  });
});
