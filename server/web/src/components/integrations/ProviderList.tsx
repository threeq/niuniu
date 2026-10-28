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
  RefreshCw,
  RotateCcw,
  FileWarning,
  BookOpen,
} from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  useExternalProviders,
  useDeleteExternalProvider,
  useUpdateExternalProvider,
  useSetProviderWriteEnabled,
  useProbeProvider,
  useReenableProvider,
  useAcknowledgeProviderDrift,
} from '@/hooks/useExternalProviders';
import { Switch } from '@/components/ui/switch';
import { ProviderDialog } from './ProviderDialog';
import { CapabilityDialog } from './CapabilityDialog';
import { HealthLight } from './HealthLight';
import {
  isSystemProvider,
  type ExternalProviderListItem,
} from '@/types/integration';
import { useAuthStore } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import type { WriteOwnerScope } from '@/lib/external-proxy-api';

export function ProviderList() {
  const { t } = useTranslation('settings');
  const userId = useAuthStore((s) => s.user?.id ?? 0);
  const myOrgs = useOrgStore((s) => s.myOrgs);
  // Write-permission owner scope. The proxy gates non-GET calls by the
  // WORKSPACE's owner, so this list manages the scope the agents actually
  // read: a sole org by default (the common team deployment), else personal.
  // The user's explicit pick wins once made.
  const [picked, setPicked] = useState<WriteOwnerScope | null>(null);
  const owner: WriteOwnerScope =
    picked ?? (myOrgs.length === 1 ? { type: 'org', id: myOrgs[0].id } : { type: 'user', id: userId });
  const { data, isLoading, isError, error } = useExternalProviders(
    owner.id > 0 ? owner : undefined,
  );
  const del = useDeleteExternalProvider();
  const update = useUpdateExternalProvider();
  const setWrite = useSetProviderWriteEnabled();
  const probe = useProbeProvider();
  const reenable = useReenableProvider();
  const ackDrift = useAcknowledgeProviderDrift();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<ExternalProviderListItem | null>(null);
  const [capFor, setCapFor] = useState<ExternalProviderListItem | null>(null);

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
      { id: p.id, enabled: next, owner: owner.id > 0 ? owner : undefined },
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

  const handleProbe = (p: ExternalProviderListItem) => {
    probe.mutate(p.id, {
      onSuccess: (o) => {
        if (o.status === 'ok' || o.status === 'drift') {
          toast.success(t('integrations.health.probeOk'));
        } else {
          toast.error(`${t('integrations.health.probeFailed')}: ${o.message}`);
        }
      },
      onError: (e) =>
        toast.error(e instanceof Error ? e.message : String(e)),
    });
  };

  const handleReenable = (p: ExternalProviderListItem) => {
    reenable.mutate(p.id, {
      onSuccess: () => toast.success(t('integrations.health.reenableSuccess')),
      onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
    });
  };

  const handleAckDrift = (p: ExternalProviderListItem) => {
    ackDrift.mutate(p.id, {
      onSuccess: () => toast.success(t('integrations.health.acknowledgeDriftSuccess')),
      onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
    });
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
        <div className="flex items-center gap-2">
          {myOrgs.length > 0 && (
            <Select
              value={owner.type === 'org' ? `org:${owner.id}` : 'user'}
              onValueChange={(v) => {
                if (v === 'user') {
                  setPicked({ type: 'user', id: userId });
                } else {
                  setPicked({ type: 'org', id: Number(v.slice('org:'.length)) });
                }
              }}
            >
              <SelectTrigger
                aria-label={t('integrations.providers.writeScopeLabel')}
                className="h-8 w-36 text-xs"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">
                  {t('integrations.providers.writeScopePersonal')}
                </SelectItem>
                {myOrgs.map((o) => (
                  <SelectItem key={o.id} value={`org:${o.id}`}>
                    {o.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Button size="sm" onClick={handleAdd}>
            <Plus className="size-4 mr-1" />
            {t('integrations.providers.addButton')}
          </Button>
        </div>
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
                  <div className="flex items-center gap-2 flex-wrap mt-1">
                    <HealthLight
                      status={p.health_status}
                      detail={p.last_probe_error || undefined}
                    />
                    {(p.consecutive_failures ?? 0) > 0 && (
                      <span className="text-[10px] text-warm-text-muted">
                        {t('integrations.health.consecutiveFailures', {
                          count: p.consecutive_failures,
                        })}
                      </span>
                    )}
                    {p.contract_drift && (
                      <Badge
                        variant="outline"
                        className="gap-1 border-warning/50 text-warning"
                        title={t('integrations.health.driftHint')}
                      >
                        <FileWarning className="size-3" aria-hidden />
                        {t('integrations.health.driftBadge')}
                      </Badge>
                    )}
                  </div>
                </div>
                <div className="flex flex-col items-end gap-1.5">
                  {p.contract_drift && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => handleAckDrift(p)}
                      disabled={ackDrift.isPending}
                      title={t('integrations.health.driftHint')}
                    >
                      {t('integrations.health.acknowledgeDrift')}
                    </Button>
                  )}
                  {!p.enabled && p.auto_disabled_at && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => handleReenable(p)}
                      disabled={reenable.isPending}
                      title={t('integrations.health.autoDisabledHint')}
                    >
                      <RotateCcw className="size-3 mr-1" aria-hidden />
                      {t('integrations.health.reenable')}
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => handleProbe(p)}
                    disabled={probe.isPending}
                    title={t('integrations.health.probeNow')}
                    aria-label={t('integrations.health.probeNow')}
                  >
                    <RefreshCw
                      className={probe.isPending ? 'size-4 animate-spin' : 'size-4'}
                      aria-hidden
                    />
                  </Button>
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
                  variant="ghost"
                  aria-label={t('integrations.capability.viewButton')}
                  title={t('integrations.capability.viewButton')}
                  onClick={() => setCapFor(p)}
                >
                  <BookOpen className="size-4" />
                </Button>
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

      <CapabilityDialog
        open={capFor !== null}
        onOpenChange={(o) => {
          if (!o) setCapFor(null);
        }}
        kind="provider"
        target={capFor?.name}
        title={capFor?.label || capFor?.name || ''}
      />
    </section>
  );
}
