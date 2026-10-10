import { describe, expect, it, vi, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ChatInput } from './chat-input';
import { api } from '@/lib/api';
import i18n from '@/i18n';
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

// The drop path is plain HTML5 drag and drop — the same code runs in the
// browser and inside the desktop webview. The desktop shell used to swallow the
// events natively (Tauri's drag-drop handler); these tests pin the web side, so
// a regression there can no longer hide behind "it's probably the shell".
describe('ChatInput file drop', () => {
  afterEach(() => vi.restoreAllMocks());

  it('uploads files dropped onto the input area', async () => {
    const upload = vi.spyOn(api, 'uploadAttachment').mockResolvedValue({
      path: '/tmp/dropped.png',
      name: 'dropped.png',
      mimeType: 'image/png',
      size: 3,
      originalSize: 3,
      optimized: false,
    } as Awaited<ReturnType<typeof api.uploadAttachment>>);
    render(withQuery(
      <ChatInput workspace={baseWorkspace} onSend={vi.fn()} sessionState="idle" />
    ));

    const file = new File(['abc'], 'dropped.png', { type: 'image/png' });
    // Fired on the textarea: the drop zone is its wrapper, and the event
    // bubbles to it exactly as a real drop on the input would.
    fireEvent.drop(screen.getByRole('textbox'), { dataTransfer: { files: [file] } });

    await waitFor(() => expect(upload).toHaveBeenCalledWith('1', file));
  });

  it('shows the drop hint while a file is dragged over the input', () => {
    render(withQuery(
      <ChatInput workspace={baseWorkspace} onSend={vi.fn()} sessionState="idle" />
    ));

    fireEvent.dragOver(screen.getByRole('textbox'), {
      dataTransfer: { files: [new File(['abc'], 'dropped.png')] },
    });

    expect(screen.getByText(i18n.t('panels.chatInput.dropZone', { ns: 'workspaces' }))).toBeInTheDocument();
  });
});
