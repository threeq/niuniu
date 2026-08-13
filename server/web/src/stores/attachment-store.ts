import { create } from 'zustand';
import type { ChatAttachment } from '@/types/api';

interface FileResult {
  path: string;
  name: string;
  repo: string;
  isDir: boolean;
}

interface AttachmentState {
  attachments: ChatAttachment[];
  /** Files currently uploading — show a spinner tag. */
  pendingAttachments: { name: string; size: number }[];
  uploading: boolean;
  filePickerActive: boolean;
  filePickerQuery: string;
  filePickerResults: FileResult[];
  filePickerSelectedIndex: number;
  filePickerLoading: boolean;

  addAttachment: (attachment: ChatAttachment) => void;
  removeAttachment: (path: string) => void;
  clearAttachments: () => void;
  addPendingAttachment: (name: string, size: number) => void;
  movePendingToAttachment: (name: string, attachment: ChatAttachment) => void;
  removePendingAttachment: (name: string) => void;
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

export const useAttachmentStore = create<AttachmentState>((set) => ({
  attachments: [],
  pendingAttachments: [],
  uploading: false,
  filePickerActive: false,
  filePickerQuery: '',
  filePickerResults: [],
  filePickerSelectedIndex: 0,
  filePickerLoading: false,

  addAttachment: (attachment) =>
    set((s) => {
      if (s.attachments.some((a) => a.path === attachment.path)) return s;
      return { attachments: [...s.attachments, attachment] };
    }),
  removeAttachment: (path) =>
    set((s) => ({
      attachments: s.attachments.filter((a) => a.path !== path),
      pendingAttachments: s.pendingAttachments.filter((p) => p.name !== path),
    })),
  clearAttachments: () => set({ attachments: [], pendingAttachments: [] }),
  addPendingAttachment: (name, size) =>
    set((s) => ({
      pendingAttachments: [...s.pendingAttachments, { name, size }],
    })),
  movePendingToAttachment: (name, attachment) =>
    set((s) => ({
      pendingAttachments: s.pendingAttachments.filter((p) => p.name !== name),
      attachments: s.attachments.some((a) => a.path === attachment.path)
        ? s.attachments
        : [...s.attachments, attachment],
    })),
  removePendingAttachment: (name) =>
    set((s) => ({
      pendingAttachments: s.pendingAttachments.filter((p) => p.name !== name),
    })),
  setUploading: (uploading) => set({ uploading }),
  openFilePicker: () => set({ filePickerActive: true, filePickerQuery: '', filePickerResults: [], filePickerSelectedIndex: 0 }),
  closeFilePicker: () => set({ filePickerActive: false, filePickerQuery: '', filePickerResults: [], filePickerSelectedIndex: 0 }),
  setFilePickerQuery: (query) => set({ filePickerQuery: query, filePickerSelectedIndex: 0 }),
  setFilePickerResults: (results) => set({ filePickerResults: results }),
  setFilePickerSelectedIndex: (index) => set({ filePickerSelectedIndex: index }),
  setFilePickerLoading: (loading) => set({ filePickerLoading: loading }),
  filePickerMoveUp: () => set((s) => ({ filePickerSelectedIndex: Math.max(0, s.filePickerSelectedIndex - 1) })),
  filePickerMoveDown: () => set((s) => ({ filePickerSelectedIndex: Math.min(s.filePickerResults.length - 1, s.filePickerSelectedIndex + 1) })),
}));
