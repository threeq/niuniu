import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { BatchResult } from '@/types/api';

/**
 * React Query mutation hooks for the four batch issue endpoints. Each
 * mutation invalidates both per-column `['issues']` caches and the
 * project-wide `['all-issues']` cache used by the kanban board, so the UI
 * refreshes after any bulk action.
 *
 * The server returns `{succeeded, skipped}` for every call — the caller
 * inspects `data.skipped.length` to decide whether to surface a toast about
 * the partial result.
 */
export function useBulkIssues() {
  const qc = useQueryClient();
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['issues'] });
    qc.invalidateQueries({ queryKey: ['all-issues'] });
  };

  const move = useMutation({
    mutationFn: (v: { issue_ids: number[]; column_id: number }) =>
      api.post<BatchResult>('/issues/batch/move', v),
    onSuccess: invalidate,
  });
  const priority = useMutation({
    mutationFn: (v: { issue_ids: number[]; priority: number }) =>
      api.post<BatchResult>('/issues/batch/priority', v),
    onSuccess: invalidate,
  });
  const labels = useMutation({
    mutationFn: (v: {
      issue_ids: number[];
      add_label_ids?: number[];
      remove_label_ids?: number[];
    }) => api.post<BatchResult>('/issues/batch/labels', v),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (v: { issue_ids: number[] }) =>
      api.post<BatchResult>('/issues/batch/delete', v),
    onSuccess: invalidate,
  });

  return { move, priority, labels, remove };
}
