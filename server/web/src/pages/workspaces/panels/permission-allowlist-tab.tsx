import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import type { PermissionAllowlistEntry } from '@/types/permission';
import { listPermissionAllowlist, deletePermissionAllowlistEntry } from '@/lib/api';

export function PermissionAllowlistTab({ workspaceId }: { workspaceId: number }) {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<PermissionAllowlistEntry[]>([]);
  const [filter, setFilter] = useState('');

  const reload = async () => {
    setEntries(await listPermissionAllowlist(workspaceId));
  };

  useEffect(() => {
    reload().catch((e) => console.error('list allowlist failed', e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId]);

  const remove = async (id: number) => {
    try {
      await deletePermissionAllowlistEntry(id);
      await reload();
    } catch (e) {
      console.error('delete allowlist failed', e);
    }
  };

  const visible = entries.filter((e) => {
    const q = filter.toLowerCase();
    return !q || e.tool_name.toLowerCase().includes(q) || e.matcher_value.toLowerCase().includes(q);
  });

  return (
    <div className="space-y-2">
      <div className="text-xs text-muted-foreground">
        {t('workspaces:permission.allowlist.header')}
      </div>
      <Input
        placeholder={t('workspaces:permission.allowlist.search')}
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
      />
      <ul className="divide-y rounded border">
        {visible.length === 0 ? (
          <li className="p-3 text-center text-xs text-muted-foreground">
            {t('workspaces:permission.allowlist.empty')}
          </li>
        ) : (
          visible.map((entry) => (
            <li key={entry.id} className="flex items-center gap-2 p-2 text-sm">
              <span className="font-mono">{entry.tool_name}</span>
              <span className="rounded bg-muted px-1 text-xs">{entry.matcher_kind}</span>
              <span className="text-xs">
                {entry.matcher_value || t('workspaces:permission.allowlist.any')}
              </span>
              <Button
                className="ml-auto"
                size="icon"
                variant="ghost"
                aria-label={t('workspaces:permission.allowlist.delete')}
                onClick={() => remove(entry.id)}
              >
                <Trash2 className="h-3 w-3" />
              </Button>
            </li>
          ))
        )}
      </ul>
    </div>
  );
}
