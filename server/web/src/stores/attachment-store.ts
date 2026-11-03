import { create } from 'zustand';
import type { ChatAttachment } from '@/types/api';

interface FileResult {
  path: string;
  name: string;
  repo: string;
  isDir: boolean;
}

interface PendingAttachment {
  name: string;
  size: number;
}

/**
 * Chat-input attachment state, SCOPED PER WORKSPACE.
 *
 * A single flat list leaked drafts across conversations: upload a document in
 * workspace A, switch to B, and A's attachment was still sitting in B's input
 * (and would be sent there). Every read/write below is keyed by workspace id,
 * so each conversation keeps its own staged files and switching back restores
 * exactly what was staged there.
 */
interface AttachmentState {
  attachmentsByWorkspace: Record<string, ChatAttachment[]>;
  pendingByWorkspace: Record<string, PendingAttachment[]>;
  uploading: boolean;
  filePickerActive: boolean;
  filePickerQuery: string;
  filePickerResults: FileResult[];
  filePickerSelectedIndex: number;
  filePickerLoading: boolean;

  addAttachment: (workspaceId: string, attachment: ChatAttachment) => void;
  removeAttachment: (workspaceId: string, path: string) => void;
  clearAttachments: (workspaceId: string) => void;
  addPendingAttachment: (workspaceId: string, name: string, size: number) => void;
  movePendingToAttachment: (workspaceId: string, name: string, attachment: ChatAttachment) => void;
  removePendingAttachment: (workspaceId: string, name: string) => void;
  setUploading: (uploading: boolean) => void;
  openFilePicker: () => void;
  closeFilePicker: () => void;
  setFilePickerQuery: (query: string) => void;
  setFilePickerResults: (results: FileResult[]) => void;
  setFilePickerSelectedIndex: (index: number) => void;
  setFilePickerLoading: (loading: boolean) => void;
  filePickerMoveUp: () => void;
  filePickerMoveDown: () => void;
}

// Stable empty arrays: selectors must not hand back a fresh [] on every call,
// or useSyncExternalStore would see a new snapshot each render.
const EMPTY_ATTACHMENTS: ChatAttachment[] = [];
const EMPTY_PENDING: PendingAttachment[] = [];

export const selectWorkspaceAttachments =
  (workspaceId: string) =>
  (s: AttachmentState): ChatAttachment[] =>
    s.attachmentsByWorkspace[workspaceId] ?? EMPTY_ATTACHMENTS;

export const selectWorkspacePendingAttachments =
  (workspaceId: string) =>
  (s: AttachmentState): PendingAttachment[] =>
    s.pendingByWorkspace[workspaceId] ?? EMPTY_PENDING;

export const useAttachmentStore = create<AttachmentState>((set) => ({
  attachmentsByWorkspace: {},
  pendingByWorkspace: {},
  uploading: false,
  filePickerActive: false,
  filePickerQuery: '',
  filePickerResults: [],
  filePickerSelectedIndex: 0,
  filePickerLoading: false,

  addAttachment: (workspaceId, attachment) =>
    set((s) => {
      const current = s.attachmentsByWorkspace[workspaceId] ?? EMPTY_ATTACHMENTS;
      if (current.some((a) => a.path === attachment.path)) return s;
      return {
        attachmentsByWorkspace: {
          ...s.attachmentsByWorkspace,
          [workspaceId]: [...current, attachment],
        },
      };
    }),
  removeAttachment: (workspaceId, path) =>
    set((s) => ({
      attachmentsByWorkspace: {
        ...s.attachmentsByWorkspace,
        [workspaceId]: (s.attachmentsByWorkspace[workspaceId] ?? EMPTY_ATTACHMENTS).filter(
          (a) => a.path !== path,
        ),
      },
      pendingByWorkspace: {
        ...s.pendingByWorkspace,
        [workspaceId]: (s.pendingByWorkspace[workspaceId] ?? EMPTY_PENDING).filter(
          (p) => p.name !== path,
        ),
      },
    })),
  clearAttachments: (workspaceId) =>
    set((s) => ({
      attachmentsByWorkspace: { ...s.attachmentsByWorkspace, [workspaceId]: [] },
      pendingByWorkspace: { ...s.pendingByWorkspace, [workspaceId]: [] },
    })),
  addPendingAttachment: (workspaceId, name, size) =>
    set((s) => {
      const current = s.pendingByWorkspace[workspaceId] ?? EMPTY_PENDING;
      return {
        pendingByWorkspace: {
          ...s.pendingByWorkspace,
          [workspaceId]: [...current, { name, size }],
        },
      };
    }),
  movePendingToAttachment: (workspaceId, name, attachment) =>
    set((s) => {
      const pending = (s.pendingByWorkspace[workspaceId] ?? EMPTY_PENDING).filter(
        (p) => p.name !== name,
      );
      const current = s.attachmentsByWorkspace[workspaceId] ?? EMPTY_ATTACHMENTS;
      const attachments = current.some((a) => a.path === attachment.path)
        ? current
        : [...current, attachment];
      return {
        pendingByWorkspace: { ...s.pendingByWorkspace, [workspaceId]: pending },
        attachmentsByWorkspace: { ...s.attachmentsByWorkspace, [workspaceId]: attachments },
      };
    }),
  removePendingAttachment: (workspaceId, name) =>
    set((s) => ({
      pendingByWorkspace: {
        ...s.pendingByWorkspace,
        [workspaceId]: (s.pendingByWorkspace[workspaceId] ?? EMPTY_PENDING).filter(
          (p) => p.name !== name,
        ),
      },
    })),
  setUploading: (uploading) => set({ uploading }),
  openFilePicker: () => set({ filePickerActive: true, filePickerQuery: '', filePickerResults: [], filePickerSelectedIndex: 0 }),
  closeFilePicker: () => set({ filePickerActive: false, filePickerQuery: '', filePickerResults: [], filePickerSelectedIndex: 0 }),
  setFilePickerQuery: (query) => set({ filePickerQuery: query, filePickerSelectedIndex: 0 }),
  setFilePickerResults: (results) => set({ filePickerResults: results }),
  setFilePickerSelectedIndex: (index) => set({ filePickerSelectedIndex: index }),
  setFilePickerLoading: (loading) => set({ filePickerLoading: loading }),
  filePickerMoveUp: () =>
    set((s) => ({
      filePickerSelectedIndex: Math.max(0, s.filePickerSelectedIndex - 1),
    })),
  filePickerMoveDown: () =>
    set((s) => ({
      filePickerSelectedIndex: Math.min(
        Math.max(0, s.filePickerResults.length - 1),
        s.filePickerSelectedIndex + 1,
      ),
    })),
}));
