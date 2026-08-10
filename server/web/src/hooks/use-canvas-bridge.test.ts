import { describe, it, expect, beforeEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useCanvasBridge } from './use-canvas-bridge';
import { useChatInputBridge } from '@/stores/chat-input-bridge-store';
import { useAttachmentStore } from '@/stores/attachment-store';

vi.mock('@/lib/api', () => ({
  api: {
    uploadAttachment: vi.fn(async () => ({
      path: '.attachments/annotation.png',
      name: 'annotation.png',
      size: 100,
      mimeType: 'image/png',
      optimized: false,
      originalSize: 100,
    })),
    writeWorktreeFile: vi.fn(async () => ({
      path: '.worktrees/wt/canvas/annotation.excalidraw',
    })),
  },
}));

import { api } from '@/lib/api';

function makeBlob(bytes = 10): Blob {
  return new Blob([new Uint8Array(bytes)], { type: 'image/png' });
}

beforeEach(() => {
  vi.clearAllMocks();
  useChatInputBridge.setState({ pending: null });
  useAttachmentStore.setState({ attachments: [] });
});

describe('useCanvasBridge', () => {
  it('persists source to worktree, uploads image, and auto-sends with the attachment on the bridge', async () => {
    const { result } = renderHook(() => useCanvasBridge('7'));

    await act(async () => {
      await result.current.send({
        blob: makeBlob(),
        filename: 'annotation.png',
        prompt: 'do the thing',
        autoSend: true,
        source: { worktree: 'wt', path: 'canvas/annotation.excalidraw', content: '{}' },
      });
    });

    expect(api.writeWorktreeFile).toHaveBeenCalledWith('7', 'wt', 'canvas/annotation.excalidraw', '{}');
    expect(api.uploadAttachment).toHaveBeenCalledTimes(1);

    const pending = useChatInputBridge.getState().pending;
    expect(pending?.autoSend).toBe(true);
    expect(pending?.attachments?.[0]?.path).toBe('.attachments/annotation.png');
    // autoSend rides the attachment via the bridge, not the attachment store.
    expect(useAttachmentStore.getState().attachments).toHaveLength(0);
  });

  it('stages the attachment in the store for review when autoSend is false', async () => {
    const { result } = renderHook(() => useCanvasBridge('7'));

    await act(async () => {
      await result.current.send({
        blob: makeBlob(),
        filename: 'annotation.png',
        prompt: 'review me',
        autoSend: false,
      });
    });

    const pending = useChatInputBridge.getState().pending;
    expect(pending?.autoSend).toBe(false);
    expect(pending?.attachments).toBeUndefined();
    expect(useAttachmentStore.getState().attachments[0]?.path).toBe('.attachments/annotation.png');
  });

  it('rejects an oversized image without touching the network', async () => {
    const { result } = renderHook(() => useCanvasBridge('7'));

    let res: unknown;
    await act(async () => {
      res = await result.current.send({
        blob: makeBlob(10 * 1024 * 1024 + 1),
        filename: 'big.png',
        prompt: 'x',
      });
    });

    expect(res).toBeNull();
    expect(api.uploadAttachment).not.toHaveBeenCalled();
    expect(api.writeWorktreeFile).not.toHaveBeenCalled();
  });
});
