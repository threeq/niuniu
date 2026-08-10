import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover';
import { assignableUsers as api } from '../../lib/api';
import type { AssignableUser, IssueAssignee } from '../../types/api';
import { avatarColorFor } from '../../lib/avatar-color';
import { useTranslation } from 'react-i18next';

type Props = {
  projectId: number;
  value: number[];
  prefilled?: IssueAssignee[];
  onChange: (ids: number[]) => void;
  disabled?: boolean;
};

export function AssigneePicker({ projectId, value, prefilled = [], onChange, disabled }: Props) {
  const { t } = useTranslation('projects');
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');

  const { data: users = [] } = useQuery<AssignableUser[]>({
    queryKey: ['assignable-users', projectId],
    queryFn: () => api.list(projectId),
    // 30s staleTime: short enough that an admin removing a member while
    // the picker is closed becomes visible on the next reopen, long enough
    // to avoid a refetch on every popover open within a single editing
    // session. A notify-WS subscription on `org_member` would be more
    // precise but is out of scope here.
    staleTime: 30 * 1000,
    enabled: open,
  });

  const selectedDisplay = useMemo(() => {
    return value.map(id => {
      const fromList = users.find(u => u.id === id);
      if (fromList) return { id, display_name: fromList.display_name };
      const fromPrefilled = prefilled.find(p => p.id === id);
      if (fromPrefilled) return { id, display_name: fromPrefilled.display_name };
      return { id, display_name: `User #${id}` };
    });
  }, [value, users, prefilled]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return users;
    return users.filter(u =>
      u.display_name.toLowerCase().includes(q) || u.username.toLowerCase().includes(q));
  }, [users, query]);

  const toggle = (id: number) => {
    onChange(value.includes(id) ? value.filter(x => x !== id) : [...value, id]);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" disabled={disabled}
          className="flex items-center gap-1 text-xs hover:bg-muted rounded px-1.5 py-1 min-h-7">
          {selectedDisplay.length === 0 ? (
            <span className="text-muted-foreground">{t('issue.properties.unassigned')}</span>
          ) : (
            <div className="flex -space-x-1">
              {selectedDisplay.slice(0, 3).map(u => (
                <div key={u.id} title={u.display_name}
                  className="h-6 w-6 rounded-full text-white text-[10px] font-medium flex items-center justify-center ring-2 ring-background"
                  style={{ backgroundColor: avatarColorFor(u.display_name) }}>
                  {u.display_name[0]?.toUpperCase()}
                </div>
              ))}
              {selectedDisplay.length > 3 && (
                <div className="h-6 w-6 rounded-full bg-muted text-[10px] flex items-center justify-center ring-2 ring-background">
                  +{selectedDisplay.length - 3}
                </div>
              )}
            </div>
          )}
        </button>
      </PopoverTrigger>
      <PopoverContent className="p-0 w-64" align="start">
        <input autoFocus value={query} onChange={e => setQuery(e.target.value)}
          placeholder={t('issue.properties.searchAssignee')}
          className="w-full px-3 py-2 border-b border-border text-sm bg-background" />
        <div className="max-h-72 overflow-auto">
          {filtered.map(u => (
            <button key={u.id} type="button" onClick={() => toggle(u.id)}
              className="w-full text-left px-3 py-1.5 flex items-center gap-2 hover:bg-muted">
              <input type="checkbox" readOnly checked={value.includes(u.id)} />
              <div className="h-5 w-5 rounded-full text-white text-[10px] flex items-center justify-center"
                style={{ backgroundColor: avatarColorFor(u.display_name) }}>
                {u.display_name[0]?.toUpperCase()}
              </div>
              <span className="text-sm">{u.display_name}</span>
              <span className="text-xs text-muted-foreground">@{u.username}</span>
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="px-3 py-4 text-xs text-muted-foreground">{t('issue.properties.noMatch')}</div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
