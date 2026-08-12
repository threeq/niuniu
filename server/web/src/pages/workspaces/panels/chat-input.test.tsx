import { describe, expect, it, vi, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ChatInput } from './chat-input';
import type { Workspace } from '@/types/api';

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const baseWorkspace = {
  id: 1,
  name: 'w',
  path: '/',
  agent_status: 'idle',
} as unknown as Workspace;

describe('ChatInput IME composition guard', () => {
  afterEach(() => vi.restoreAllMocks());

  it('does not send on Ctrl+Enter while IME composition is active', () => {
    const onSend = vi.fn();
    render(withQuery(
      <ChatInput workspace={baseWorkspace} onSend={onSend} sessionState="idle" />
    ));
    const ta = screen.getByRole('textbox');
    fireEvent.change(ta, { target: { value: '你好' } });
    fireEvent.keyDown(ta, { key: 'Enter', ctrlKey: true, isComposing: true });
    expect(onSend).not.toHaveBeenCalled();
  });

  it('sends on Ctrl+Enter when not composing', () => {
    const onSend = vi.fn();
    render(withQuery(
      <ChatInput workspace={baseWorkspace} onSend={onSend} sessionState="idle" />
    ));
    const ta = screen.getByRole('textbox');
    fireEvent.change(ta, { target: { value: 'hello' } });
    fireEvent.keyDown(ta, { key: 'Enter', ctrlKey: true });
    expect(onSend).toHaveBeenCalledWith('hello', expect.any(Array));
  });
});
