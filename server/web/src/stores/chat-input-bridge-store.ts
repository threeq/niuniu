import { create } from 'zustand';
import type { ChatAttachment } from '@/types/api';

export interface ChatInputBridgeRequest {
  workspaceId: string;
  content: string;
  autoSend: boolean;
  // Attachments to send alongside the content. Only consumed on the autoSend
  // path (they ride directly into handleSend); for the non-autoSend path the
  // producer is expected to have added them to the attachment store already so
  // they render as removable tags for the user to review before sending.
  attachments?: ChatAttachment[];
  nonce: number;
}

interface ChatInputBridgeState {
  pending: ChatInputBridgeRequest | null;
  request: (
    workspaceId: string,
    content: string,
    autoSend?: boolean,
    attachments?: ChatAttachment[],
  ) => void;
  consume: (nonce: number) => void;
}

export const useChatInputBridge = create<ChatInputBridgeState>((set) => ({
  pending: null,
  request: (workspaceId, content, autoSend = false, attachments) =>
    set({
      pending: {
        workspaceId,
        content,
        autoSend,
        attachments: attachments && attachments.length > 0 ? attachments : undefined,
        nonce: Date.now() + Math.random(),
      },
    }),
  consume: (nonce) =>
    set((state) =>
      state.pending && state.pending.nonce === nonce
        ? { pending: null }
        : state,
    ),
}));
