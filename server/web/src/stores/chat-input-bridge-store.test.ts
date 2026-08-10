import { describe, it, expect, beforeEach } from 'vitest';
import { useChatInputBridge } from './chat-input-bridge-store';
import type { ChatAttachment } from '@/types/api';

function reset() {
  useChatInputBridge.setState({ pending: null });
}

const att: ChatAttachment = {
  path: '.attachments/annotation.png',
  type: 'upload',
  name: 'annotation.png',
  mimeType: 'image/png',
};

describe('chat-input-bridge-store', () => {
  beforeEach(reset);

  it('carries content + autoSend with no attachments by default', () => {
    useChatInputBridge.getState().request('7', 'hello', true);
    const p = useChatInputBridge.getState().pending;
    expect(p).not.toBeNull();
    expect(p!.workspaceId).toBe('7');
    expect(p!.content).toBe('hello');
    expect(p!.autoSend).toBe(true);
    expect(p!.attachments).toBeUndefined();
  });

  it('carries attachments through the bridge', () => {
    useChatInputBridge.getState().request('7', 'see this', true, [att]);
    const p = useChatInputBridge.getState().pending;
    expect(p!.attachments).toEqual([att]);
  });

  it('normalizes an empty attachment list to undefined', () => {
    useChatInputBridge.getState().request('7', 'x', false, []);
    expect(useChatInputBridge.getState().pending!.attachments).toBeUndefined();
  });

  it('consume clears only the matching nonce', () => {
    useChatInputBridge.getState().request('7', 'x', false);
    const nonce = useChatInputBridge.getState().pending!.nonce;
    useChatInputBridge.getState().consume(nonce + 1); // wrong nonce — no-op
    expect(useChatInputBridge.getState().pending).not.toBeNull();
    useChatInputBridge.getState().consume(nonce);
    expect(useChatInputBridge.getState().pending).toBeNull();
  });
});
