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
  uploading: boolean;
  filePickerActive: boolean;
  filePickerQuery: string;
  filePickerResults: FileResult[];
  filePickerSelectedIndex: number;
  filePickerLoading: boolean;

  addAttachment: (attachment: ChatAttachment) => void;
  removeAttachment: (path: string) => void;
  clearAttachments: () => void;
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
    set((s) => ({ attachments: s.attachments.filter((a) => a.path !== path) })),
  clearAttachments: () => set({ attachments: [] }),
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
