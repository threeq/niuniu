import { useMemo, useState, useCallback } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover';
import { labels as api } from '../../lib/api';
import type { Label } from '../../types/api';
import { contrastTextColor, randomPresetColor } from '../../lib/label-color';
import { isIMEComposing } from '@/lib/ime';
import { useTranslation } from 'react-i18next';

type Props = {
  projectId: number;
  value: number[];
  onChange: (ids: number[]) => void;
  disabled?: boolean;
};

export function LabelPicker({ projectId, value, onChange, disabled }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');

  const { data: labelList = [] } = useQuery<Label[]>({
    queryKey: ['labels', projectId],
    queryFn: () => api.list(projectId),
  });

  const selected = useMemo(
    () => value.map(id => labelList.find(l => l.id === id)).filter(Boolean) as Label[],
    [value, labelList]
  );

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return labelList;
    return labelList.filter(l => l.name.toLowerCase().includes(q));
  }, [labelList, query]);

  const exact = labelList.find(l => l.name.toLowerCase() === query.trim().toLowerCase());

  const createMut = useMutation({
    mutationFn: () => api.create(projectId, { name: query.trim(), color: randomPresetColor() }),
    onSuccess: (res) => {
      if ('code' in res && res.code === 'label_name_taken') {
        onChange([...new Set([...value, res.existing.id])]);
        toast(t('issue.properties.labelExists', { name: res.existing.name }));
      } else if ('data' in res) {
        onChange([...new Set([...value, res.data.id])]);
      }
      qc.invalidateQueries({ queryKey: ['labels', projectId] });
      setQuery('');
    },
    // labels.create wraps apiFetch with `suppressError: true` so the
    // `label_name_taken` 409 surfaces as a structured `{code, existing}`
    // payload (handled in onSuccess above) instead of a generic API Error
    // toast. That suppression also hides 5xx / 403 / network failures, so
    // we re-introduce a feedback toast here on the rejection path.
    onError: (err: unknown) => {
      toast.error(t('common:status.failed'));
      console.error('create label failed', err);
    },
  });

  const onKey = useCallback((e: React.KeyboardEvent) => {
    if (isIMEComposing(e)) return;
    if (e.key === 'Enter' && query.trim() && !exact && !createMut.isPending) {
      e.preventDefault();
      createMut.mutate();
    }
  }, [query, exact, createMut]);

  const toggle = (id: number) => {
    onChange(value.includes(id) ? value.filter(x => x !== id) : [...value, id]);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" disabled={disabled}
          className="flex items-center gap-1 text-xs hover:bg-muted rounded px-1.5 py-1 min-h-7 flex-wrap">
          {selected.length === 0 ? (
            <span className="text-muted-foreground">+ {t('issue.properties.labels')}</span>
          ) : (
            <>
              {selected.slice(0, 3).map(l => (
                <span key={l.id} className="rounded px-1.5 py-0.5 text-[11px] leading-tight"
                  style={{ backgroundColor: l.color, color: contrastTextColor(l.color) }}>{l.name}</span>
              ))}
              {selected.length > 3 && (
                <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[11px]">+{selected.length - 3}</span>
              )}
            </>
          )}
        </button>
      </PopoverTrigger>
      <PopoverContent className="p-0 w-72" align="start">
        <input autoFocus
          value={query}
          onChange={e => setQuery(e.target.value)}
          onKeyDown={onKey}
          placeholder={t('settings.labels.placeholder.name')}
          className="w-full px-3 py-2 border-b border-border text-sm bg-background" />
        <div className="max-h-64 overflow-auto">
          {filtered.map(l => (
            <button key={l.id} type="button" onClick={() => toggle(l.id)}
              className="w-full text-left px-3 py-1.5 flex items-center gap-2 hover:bg-muted">
              <input type="checkbox" readOnly checked={value.includes(l.id)} />
              <span className="rounded px-1.5 py-0.5 text-[11px]"
                style={{ backgroundColor: l.color, color: contrastTextColor(l.color) }}>{l.name}</span>
              {l.description && <span className="text-xs text-muted-foreground truncate">{l.description}</span>}
            </button>
          ))}
          {!exact && query.trim() !== '' && (
            <button type="button"
              onClick={() => createMut.mutate()}
              disabled={createMut.isPending}
              className="w-full text-left px-3 py-2 text-sm hover:bg-muted border-t border-border disabled:opacity-50">
              {t('issue.properties.createLabel', { name: query.trim() })}
            </button>
          )}
          {filtered.length === 0 && exact === undefined && query.trim() === '' && (
            <div className="px-3 py-4 text-xs text-muted-foreground">{t('settings.labels.empty')}</div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
