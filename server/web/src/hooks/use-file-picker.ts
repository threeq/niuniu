import { useEffect, useRef } from 'react';
import { api } from '@/lib/api';
import { useAttachmentStore } from '@/stores/attachment-store';

export function useFilePicker(workspaceId: string) {
  const store = useAttachmentStore();
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  useEffect(() => {
    if (!store.filePickerActive) return;
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(async () => {
      store.setFilePickerLoading(true);
      try {
        const res = await api.searchWorkspaceFiles(workspaceId, store.filePickerQuery);
        store.setFilePickerResults(res.files ?? []);
      } catch {
        store.setFilePickerResults([]);
      } finally {
        store.setFilePickerLoading(false);
      }
    }, 150);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
  // eslint-disable-next-line react-hooks/exhaustive-deps -- store.setFilePickerLoading and store.setFilePickerResults are stable Zustand action refs; including store would cause unnecessary re-runs
  }, [store.filePickerActive, store.filePickerQuery, workspaceId]);

  return store;
}
