import { useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '@/lib/api';
import { useAttachmentStore } from '@/stores/attachment-store';
import { toast } from 'sonner';

const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10MB

export function useFileUpload(workspaceId: string) {
  const { t } = useTranslation('common');
  const store = useAttachmentStore();
  const fileInputRef = useRef<HTMLInputElement>(null);

  const uploadFile = useCallback(async (file: File) => {
    if (file.size > MAX_FILE_SIZE) {
      toast.error(t('errors.fileUploadTooLarge', { name: file.name, limit: '10MB' }));
      return;
    }
    store.setUploading(true);
    try {
      const result = await api.uploadAttachment(workspaceId, file);
      store.addAttachment({
        path: result.path,
        type: 'upload',
        name: result.name,
        mimeType: result.mimeType,
        size: result.size,
        originalSize: result.originalSize,
        optimized: result.optimized,
      });
    } catch {
      toast.error(t('errors.fileUploadFailed', { name: file.name }));
    } finally {
      store.setUploading(false);
    }
  }, [workspaceId, store, t]);

  const uploadFiles = useCallback(async (files: FileList | File[]) => {
    await Promise.allSettled(Array.from(files).map((f) => uploadFile(f)));
  }, [uploadFile]);

  const openFilePicker = useCallback(() => {
    fileInputRef.current?.click();
  }, []);

  const handleFileInputChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      uploadFiles(e.target.files);
      e.target.value = '';
    }
  }, [uploadFiles]);

  const handlePaste = useCallback((e: ClipboardEvent) => {
    const items = e.clipboardData?.items;
    if (!items) return;
    for (const item of Array.from(items)) {
      if (item.type.startsWith('image/')) {
        e.preventDefault();
        const file = item.getAsFile();
        if (file) {
          // Normalize non-standard MIME subtypes used by some Linux/macOS tools
          // e.g. image/x-png → png, image/x-bmp → bmp
          let ext = item.type.split('/')[1] || 'png';
          if (ext.startsWith('x-')) ext = ext.slice(2);
          const namedFile = new File([file], `paste-${Date.now()}.${ext}`, { type: item.type });
          uploadFile(namedFile);
        }
        return;
      }
    }
  }, [uploadFile]);

  return { fileInputRef, uploadFiles, openFilePicker, handleFileInputChange, handlePaste };
}
