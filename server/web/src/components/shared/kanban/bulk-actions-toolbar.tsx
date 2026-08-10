import { useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { ArrowRightLeft, Flag, Tag, Trash2, X, Check, Plus, Minus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover';
import {
  AlertDialog,
  AlertDialogTrigger,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogFooter,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogCancel,
  AlertDialogAction,
} from '@/components/ui/alert-dialog';
import { useBulkIssues } from '@/lib/hooks/use-bulk-issues';
import { cn } from '@/lib/utils';
import type { Column, Issue, Label, BatchResult } from '@/types/api';

interface BulkActionsToolbarProps {
  selectedIds: number[];
  selectedIssues: Issue[];
  columns: Column[];
  labels: Label[];
  onClear: () => void;
  onDone: () => void;
}

// Priority options, high -> low, with the design-system priority dot tokens.
// 0=low is neutral gray (never green) per docs/design-system.md §2.4.
const PRIORITY_OPTIONS: { value: number; i18nKey: string; dot: string }[] = [
  { value: 3, i18nKey: 'kanban.priority.critical', dot: 'bg-prio-critical' },
  { value: 2, i18nKey: 'kanban.priority.high', dot: 'bg-prio-high' },
  { value: 1, i18nKey: 'kanban.priority.medium', dot: 'bg-prio-medium' },
  { value: 0, i18nKey: 'kanban.priority.low', dot: 'bg-prio-low' },
];

export function BulkActionsToolbar({
  selectedIds,
  selectedIssues,
  columns,
  labels,
  onClear,
  onDone,
}: BulkActionsToolbarProps) {
  const { t } = useTranslation('projects');
  const bulk = useBulkIssues();
  const [labelDeltas, setLabelDeltas] = useState<Record<number, 'add' | 'remove'>>({});

  const count = selectedIds.length;
  const busy =
    bulk.move.isPending ||
    bulk.priority.isPending ||
    bulk.labels.isPending ||
    bulk.remove.isPending;

  // Surface a toast summarizing the BatchResult, then clear the selection.
  const reportResult = useCallback(
    (data: BatchResult) => {
      const skip = data.skipped?.length ?? 0;
      const ok = data.succeeded?.length ?? 0;
      if (skip > 0) {
        toast.warning(t('kanban.bulk.skipped', { ok, skip }));
      } else {
        toast.success(t('kanban.bulk.done'));
      }
      onDone();
    },
    [t, onDone]
  );

  const handleMove = useCallback(
    (columnId: number) => {
      bulk.move.mutate(
        { issue_ids: selectedIds, column_id: columnId },
        { onSuccess: reportResult }
      );
    },
    [bulk.move, selectedIds, reportResult]
  );

  const handlePriority = useCallback(
    (priority: number) => {
      bulk.priority.mutate(
        { issue_ids: selectedIds, priority },
        { onSuccess: reportResult }
      );
    },
    [bulk.priority, selectedIds, reportResult]
  );

  const cycleLabel = useCallback((labelId: number) => {
    setLabelDeltas((prev) => {
      const next = { ...prev };
      const cur = next[labelId];
      if (cur === undefined) next[labelId] = 'add';
      else if (cur === 'add') next[labelId] = 'remove';
      else delete next[labelId];
      return next;
    });
  }, []);

  const applyLabels = useCallback(() => {
    const add_label_ids = Object.entries(labelDeltas)
      .filter(([, v]) => v === 'add')
      .map(([k]) => Number(k));
    const remove_label_ids = Object.entries(labelDeltas)
      .filter(([, v]) => v === 'remove')
      .map(([k]) => Number(k));
    if (add_label_ids.length === 0 && remove_label_ids.length === 0) return;
    bulk.labels.mutate(
      { issue_ids: selectedIds, add_label_ids, remove_label_ids },
      {
        onSuccess: (data) => {
          setLabelDeltas({});
          reportResult(data);
        },
      }
    );
  }, [labelDeltas, bulk.labels, selectedIds, reportResult]);

  const handleDelete = useCallback(() => {
    bulk.remove.mutate({ issue_ids: selectedIds }, { onSuccess: reportResult });
  }, [bulk.remove, selectedIds, reportResult]);

  return (
    <div className="flex items-center gap-2 px-4 py-2 border-t border-warm-border bg-warm-surface">
      <span className="text-sm font-medium text-warm-text tabular-nums">
        {t('kanban.bulk.selectedCount', { count })}
      </span>

      <div className="flex items-center gap-2 ml-auto">
        {/* Move to column */}
        <Popover>
          <PopoverTrigger asChild>
            <Button variant="outline" size="sm" disabled={busy} className="gap-1.5">
              <ArrowRightLeft className="w-4 h-4" aria-hidden="true" />
              {t('kanban.bulk.move')}
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" className="w-52 p-1">
            <div className="max-h-64 overflow-auto">
              {columns.map((col) => (
                <button
                  key={col.id}
                  type="button"
                  onClick={() => handleMove(col.id)}
                  className="w-full px-2 py-1.5 text-sm text-left rounded-sm truncate
                             hover:bg-accent hover:text-accent-foreground transition-colors
                             focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {col.name}
                </button>
              ))}
            </div>
          </PopoverContent>
        </Popover>

        {/* Change priority */}
        <Popover>
          <PopoverTrigger asChild>
            <Button variant="outline" size="sm" disabled={busy} className="gap-1.5">
              <Flag className="w-4 h-4" aria-hidden="true" />
              {t('kanban.bulk.changePriority')}
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" className="w-44 p-1">
            {PRIORITY_OPTIONS.map((opt) => (
              <button
                key={opt.value}
                type="button"
                onClick={() => handlePriority(opt.value)}
                className="w-full flex items-center gap-2 px-2 py-1.5 text-sm text-left rounded-sm
                           hover:bg-accent hover:text-accent-foreground transition-colors
                           focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className={cn('w-2.5 h-2.5 rounded-full shrink-0', opt.dot)} aria-hidden="true" />
                {t(opt.i18nKey)}
              </button>
            ))}
          </PopoverContent>
        </Popover>

        {/* Add / remove labels */}
        {labels.length > 0 && (
          <Popover>
            <PopoverTrigger asChild>
              <Button variant="outline" size="sm" disabled={busy} className="gap-1.5">
                <Tag className="w-4 h-4" aria-hidden="true" />
                {t('kanban.bulk.labels')}
              </Button>
            </PopoverTrigger>
            <PopoverContent align="end" className="w-64 p-1">
              <div className="max-h-64 overflow-auto">
                {labels.map((label) => {
                  const state = labelDeltas[label.id];
                  return (
                    <div
                      key={label.id}
                      className="flex items-center gap-2 px-2 py-1.5 rounded-sm hover:bg-accent/60"
                    >
                      <span
                        className="w-2.5 h-2.5 rounded-full shrink-0 border border-warm-border"
                        style={{ backgroundColor: label.color }}
                        aria-hidden="true"
                      />
                      <span className="flex-1 truncate text-sm">{label.name}</span>
                      <button
                        type="button"
                        onClick={() => cycleLabel(label.id)}
                        aria-label={state === 'add' ? t('kanban.bulk.addLabel') : t('kanban.bulk.removeLabel')}
                        className={cn(
                          'flex items-center justify-center w-6 h-6 rounded-sm shrink-0 transition-colors',
                          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                          state === 'add' && 'bg-success/15 text-success',
                          state === 'remove' && 'bg-destructive/15 text-destructive',
                          !state && 'text-warm-text-muted hover:bg-warm-muted'
                        )}
                      >
                        {state === 'add' ? (
                          <Plus className="w-4 h-4" aria-hidden="true" />
                        ) : state === 'remove' ? (
                          <Minus className="w-4 h-4" aria-hidden="true" />
                        ) : (
                          <Check className="w-4 h-4 opacity-40" aria-hidden="true" />
                        )}
                      </button>
                    </div>
                  );
                })}
              </div>
              <div className="p-1 pt-2">
                <Button
                  size="sm"
                  className="w-full"
                  disabled={Object.keys(labelDeltas).length === 0 || busy}
                  onClick={applyLabels}
                >
                  {t('kanban.bulk.applyLabels')}
                </Button>
              </div>
            </PopoverContent>
          </Popover>
        )}

        {/* Delete (destructive — second confirmation) */}
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="destructive" size="sm" disabled={busy} className="gap-1.5">
              <Trash2 className="w-4 h-4" aria-hidden="true" />
              {t('kanban.bulk.delete')}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t('kanban.bulk.deleteConfirmTitle', { count })}</AlertDialogTitle>
              <AlertDialogDescription>{t('kanban.bulk.deleteConfirmDesc')}</AlertDialogDescription>
            </AlertDialogHeader>
            <ul className="text-sm text-warm-text-muted list-disc pl-5 space-y-0.5 max-h-32 overflow-auto">
              {selectedIssues.slice(0, 5).map((iss) => (
                <li key={iss.id} className="truncate">
                  #{iss.id} {iss.title}
                </li>
              ))}
              {selectedIssues.length > 5 && <li>+{selectedIssues.length - 5}</li>}
            </ul>
            <AlertDialogFooter>
              <AlertDialogCancel>{t('kanban.bulk.cancel')}</AlertDialogCancel>
              <AlertDialogAction
                onClick={handleDelete}
                className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              >
                {t('kanban.bulk.delete')}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        {/* Clear selection */}
        <Button variant="ghost" size="sm" onClick={onClear} className="gap-1.5">
          <X className="w-4 h-4" aria-hidden="true" />
          {t('kanban.bulk.clear')}
        </Button>
      </div>
    </div>
  );
}
