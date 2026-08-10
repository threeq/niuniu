import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from '@tanstack/react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { LayoutDashboard, Plus, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog';
import {
  listDashboards,
  createDashboard,
  deleteDashboard,
} from '@/lib/dashboards-api';
import { confirm } from '@/lib/confirm';

export function DashboardsPage() {
  const { t } = useTranslation('dashboards');
  const queryClient = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ['dashboards'],
    queryFn: listDashboards,
  });
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');

  const create = useMutation({
    mutationFn: () => createDashboard(name.trim() || t('defaultName')),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['dashboards'] });
      setCreateOpen(false);
      setName('');
    },
    onError: (e) =>
      toast.error(e instanceof Error ? e.message : t('panelLoadFailed')),
  });

  const remove = useMutation({
    mutationFn: (id: number) => deleteDashboard(id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['dashboards'] }),
  });

  const handleDelete = async (id: number, dashName: string) => {
    if (!(await confirm(t('deleteConfirm', { name: dashName })))) return;
    remove.mutate(id);
  };

  return (
    <div className="flex h-full flex-col gap-4 overflow-y-auto p-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-foreground">
          {t('pageTitle')}
        </h1>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus className="size-4 mr-1" />
          {t('create')}
        </Button>
      </div>

      {isLoading && (
        <div className="text-sm text-muted-foreground">{t('loading')}</div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <div className="text-sm text-muted-foreground">{t('empty')}</div>
      )}

      {!isLoading && data && data.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((d) => (
            <div
              key={d.id}
              className="group relative rounded-md border border-border bg-card p-4 transition-colors hover:border-ring"
            >
              <Link
                to="/dashboards/$id"
                params={{ id: String(d.id) }}
                className="flex items-center gap-3"
              >
                <LayoutDashboard
                  className="size-5 text-muted-foreground shrink-0"
                  aria-hidden
                />
                <span className="text-sm font-medium text-foreground truncate">
                  {d.name}
                </span>
              </Link>
              <Button
                size="sm"
                variant="ghost"
                aria-label={t('delete')}
                className="absolute right-2 top-2 opacity-0 transition-opacity group-hover:opacity-100"
                disabled={remove.isPending}
                onClick={() => handleDelete(d.id, d.name)}
              >
                <Trash2 className="size-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('createTitle')}</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="dash-name">{t('nameLabel')}</Label>
            <Input
              id="dash-name"
              value={name}
              placeholder={t('namePlaceholder')}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setCreateOpen(false)}
              disabled={create.isPending}
            >
              {t('cancel')}
            </Button>
            <Button onClick={() => create.mutate()} disabled={create.isPending}>
              {create.isPending ? t('saving') : t('save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
