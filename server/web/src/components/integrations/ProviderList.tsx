// ProviderList — Settings -> Integrations -> External API Providers.
//
// Renders the providers configured against the new external-proxy
// architecture (backend: server/internal/server/handler_external_proxy.go).
// Each row shows the name + label + base URL + enabled state, with
// inline edit / delete actions and a top-right "Add" button.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Plus,
  Pencil,
  Trash2,
  Plug,
  AlertTriangle,
  Lock,
  ShieldAlert,
} from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
  useExternalProviders,
  useDeleteExternalProvider,
  useUpdateExternalProvider,
  useSetProviderWriteEnabled,
} from '@/hooks/useExternalProviders';
import { Switch } from '@/components/ui/switch';
import { ProviderDialog } from './ProviderDialog';
import {
  isSystemProvider,
  type ExternalProviderListItem,
} from '@/types/integration';

export function ProviderList() {
  const { t } = useTranslation('settings');
  const { data, isLoading, isError, error } = useExternalProviders();
  const del = useDeleteExternalProvider();
  const update = useUpdateExternalProvider();
  const setWrite = useSetProviderWriteEnabled();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<ExternalProviderListItem | null>(null);

  const handleAdd = () => {
    setEditing(null);
    setDialogOpen(true);
  };

  const handleEdit = (p: ExternalProviderListItem) => {
    setEditing(p);
    setDialogOpen(true);
  };

  const handleDelete = async (p: ExternalProviderListItem) => {
    const ok = await confirm(
      t('integrations.providers.deleteConfirm', { name: p.name }),
    );
    if (!ok) return;
    del.mutate(p.id, {
      onSuccess: () => toast.success(t('integrations.providers.dialog.deleteSuccess')),
      onError: (e) =>
        toast.error(
          t('integrations.providers.dialog.deleteFailed', {
            error: e instanceof Error ? e.message : String(e),
          }),
        ),
    });
  };

  const handleToggleEnabled = (p: ExternalProviderListItem, next: boolean) => {
    update.mutate(
      { id: p.id, body: { enabled: next } },
      {
        onError: (e) =>
          toast.error(
            t('integrations.providers.dialog.saveFailed', {
              error: e instanceof Error ? e.message : String(e),
            }),
          ),
      },
    );
  };

  const handleToggleWrite = (p: ExternalProviderListItem, next: boolean) => {
    setWrite.mutate(
      { id: p.id, enabled: next },
      {
        onSuccess: () =>
          toast.success(
            next
              ? t('integrations.providers.writeEnabledOn', { name: p.label || p.name })
              : t('integrations.providers.writeEnabledOff', { name: p.label || p.name }),
          ),
        onError: (e) =>
          toast.error(
            t('integrations.providers.dialog.saveFailed', {
              error: e instanceof Error ? e.message : String(e),
            }),
          ),
      },
    );
  };

  return (
    <section className="flex flex-col gap-3 rounded border border-warm-border p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">
            {t('integrations.providers.sectionTitle')}
          </h2>
          <p className="text-xs text-warm-text-muted mt-1">
            {t('integrations.providers.sectionDescription')}
          </p>
        </div>
        <Button size="sm" onClick={handleAdd}>
          <Plus className="size-4 mr-1" />
          {t('integrations.providers.addButton')}
        </Button>
      </div>

      <div
        role="note"
        className="flex items-start gap-2 rounded border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warm-text"
      >
        <ShieldAlert
          aria-hidden
          className="size-4 text-warning shrink-0 mt-0.5"
        />
        <span>{t('integrations.providers.writeWarning')}</span>
      </div>

      {isLoading && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('integrations.providers.loading')}
        </div>
      )}

      {isError && (
        <div className="flex items-start gap-2 rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-warm-text">
          <AlertTriangle
            aria-hidden
            className="size-4 text-destructive shrink-0 mt-0.5"
          />
          <span>{error instanceof Error ? error.message : String(error)}</span>
        </div>
      )}

      {!isLoading && !isError && data && data.length === 0 && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('integrations.providers.empty')}
        </div>
      )}

      {!isLoading && data && data.length > 0 && (
        <div className="flex flex-col divide-y divide-warm-border/60">
          {data.map((p) => {
            const system = isSystemProvider(p);
            return (
              <div key={p.id} className="flex items-center gap-3 py-3">
                <Plug className="size-5 text-warm-text" aria-hidden />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-sm font-medium truncate">
                      {p.label || p.name}
                    </span>
                    <code className="text-[10px] text-warm-text-muted px-1.5 py-0.5 rounded bg-warm-surface">
                      {p.name}
                    </code>
                    {system && (
                      <Badge
                        variant="outline"
                        className="gap-1"
                        title={t('integrations.providers.systemHint')}
                      >
                        <Lock className="size-3" aria-hidden />
                        {t('integrations.providers.systemBadge')}
                      </Badge>
                    )}
                  </div>
                  <div className="text-xs text-warm-text-muted mt-0.5 truncate">
                    {p.api_base_url}
                  </div>
                </div>
                <div className="flex flex-col items-end gap-1.5">
                  <label className="flex items-center gap-1.5 text-[10px] text-warm-text-muted cursor-pointer">
                    <span>{t('integrations.providers.enabledShortLabel')}</span>
                    <Switch
                      aria-label={t('integrations.providers.enabledLabel')}
                      checked={p.enabled}
                      onCheckedChange={(v) => handleToggleEnabled(p, v)}
                      disabled={update.isPending}
                    />
                  </label>
                  <label
                    className="flex items-center gap-1.5 text-[10px] text-warm-text-muted cursor-pointer"
                    title={t('integrations.providers.writeEnabledHint')}
                  >
                    <ShieldAlert
                      className={
                        p.write_enabled
                          ? 'size-3 text-warning'
                          : 'size-3 text-warm-text-muted'
                      }
                      aria-hidden
                    />
                    <span>{t('integrations.providers.writeEnabledLabel')}</span>
                    <Switch
                      aria-label={t('integrations.providers.writeEnabledLabel')}
                      checked={p.write_enabled}
                      onCheckedChange={(v) => handleToggleWrite(p, v)}
                      disabled={setWrite.isPending || !p.enabled}
                    />
                  </label>
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={t('integrations.providers.editButton')}
                  onClick={() => handleEdit(p)}
                  disabled={system}
                  title={
                    system ? t('integrations.providers.systemHint') : undefined
                  }
                >
                  <Pencil className="size-4" />
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={t('integrations.providers.deleteButton')}
                  disabled={system || del.isPending}
                  onClick={() => handleDelete(p)}
                  title={
                    system ? t('integrations.providers.systemHint') : undefined
                  }
                >
                  <Trash2 className="size-4" />
                </Button>
              </div>
            );
          })}
        </div>
      )}

      <ProviderDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        editing={editing}
      />
    </section>
  );
}
