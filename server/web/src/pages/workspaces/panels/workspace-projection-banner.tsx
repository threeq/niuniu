import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import {
  KeyRound,
  PackageX,
  PackagePlus,
  RefreshCw,
  Copy,
  RotateCw,
  Download,
  EyeOff,
  Undo2,
  TerminalSquare,
  Minimize2,
} from 'lucide-react';
import { workspaceSceneApi } from '@/lib/api';
import type { ApplyResult, InstallFailure } from '@/types/api';
import { Alert, AlertTitle, AlertDescription } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';

interface WorkspaceProjectionBannerProps {
  workspaceId: number;
  /**
   * Optional injected projection. When provided, the banner renders from
   * this object instead of fetching its own. Used by tests and by callers
   * that already hold the projection (avoids a duplicate request).
   */
  projection?: ApplyResult | null;
}

function pluginInstallCommand(source: string, ref?: string): string {
  return ref
    ? `claude plugin install ${source} --ref ${ref}`
    : `claude plugin install ${source}`;
}

async function copyToClipboard(text: string): Promise<void> {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  throw new Error('clipboard unavailable');
}

// Collapse state is persisted per-workspace so the banner stays minimized across
// re-renders / re-visits until the user expands it again.
function collapseKey(workspaceId: number): string {
  return `ws-projection-banner-collapsed:${workspaceId}`;
}
function readCollapsed(workspaceId: number): boolean {
  try {
    return localStorage.getItem(collapseKey(workspaceId)) === '1';
  } catch {
    return false;
  }
}
function writeCollapsed(workspaceId: number, value: boolean): void {
  try {
    if (value) localStorage.setItem(collapseKey(workspaceId), '1');
    else localStorage.removeItem(collapseKey(workspaceId));
  } catch {
    /* localStorage unavailable — non-fatal, collapse just won't persist */
  }
}

export function WorkspaceProjectionBanner({
  workspaceId,
  projection,
}: WorkspaceProjectionBannerProps) {
  const { t } = useTranslation('scenes');
  const qc = useQueryClient();
  const [collapsed, setCollapsed] = useState(() => readCollapsed(workspaceId));
  const collapse = () => {
    setCollapsed(true);
    writeCollapsed(workspaceId, true);
  };
  const expand = () => {
    setCollapsed(false);
    writeCollapsed(workspaceId, false);
  };

  const { data: fetched } = useQuery({
    queryKey: ['workspace-projection', workspaceId],
    queryFn: () => workspaceSceneApi.getProjection(workspaceId),
    enabled: projection === undefined,
  });

  const result = projection ?? fetched ?? null;

  const recomputeMut = useMutation({
    mutationFn: () => workspaceSceneApi.recompute(workspaceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspace-projection', workspaceId] });
    },
  });

  // Per-plugin install mutation. sources=[]  → install all currently pending.
  const installMut = useMutation({
    mutationFn: (sources: string[]) =>
      workspaceSceneApi.installPlugins(workspaceId, sources),
    onSuccess: (res, sources) => {
      qc.invalidateQueries({ queryKey: ['workspace-projection', workspaceId] });
      const installedCount = res.results.filter((r) => r.status === 'installed').length;
      const failedCount = res.results.filter((r) => r.status === 'failed').length;
      if (failedCount > 0) {
        toast.error(
          t('banner.install_partial', {
            installed: installedCount,
            failed: failedCount,
          }),
        );
      } else {
        toast.success(
          sources.length > 0
            ? t('banner.install_success_one')
            : t('banner.install_success_all', { count: installedCount }),
        );
      }
    },
    onError: () => {
      toast.error(t('banner.install_error'));
    },
  });

  // Dismiss / restore a plugin so the banner stops surfacing it. `dismissed`
  // true → hide; false → bring it back into the install list.
  const dismissMut = useMutation({
    mutationFn: ({ source, dismissed }: { source: string; dismissed: boolean }) =>
      workspaceSceneApi.dismissPlugin(workspaceId, source, dismissed),
    onSuccess: (_res, { dismissed }) => {
      qc.invalidateQueries({ queryKey: ['workspace-projection', workspaceId] });
      toast.success(dismissed ? t('banner.dismissed_one') : t('banner.restored_one'));
    },
    onError: () => {
      toast.error(t('banner.install_error'));
    },
  });

  if (!result) return null;
  const missing = result.missing_credentials ?? [];
  const allRows = result.install_failures ?? [];
  // Split by status. The column historically held only failures, but after
  // the 2026-05-20 "no auto-install" change it also carries pending and
  // skipped entries — banner needs to render each differently.
  const pending = allRows.filter((r) => r.status === 'pending');
  const failures = allRows.filter((r) => r.status === 'failed');
  // 'unsupported' rows surface for codex workspaces when a scene declares
  // claude-marketplace plugins. Codex CLI 0.x has no `codex plugin install`,
  // so we render an explanatory note instead of an install/retry CTA — the
  // user is expected to add those plugins as MCP servers in the scene.
  const unsupported = allRows.filter((r) => r.status === 'unsupported');
  const dismissedPlugins = result.dismissed_plugins ?? [];
  const missingRuntimes = result.missing_runtimes ?? [];
  const restartRequired = !!result.restart_required;
  if (
    !restartRequired &&
    missing.length === 0 &&
    missingRuntimes.length === 0 &&
    pending.length === 0 &&
    failures.length === 0 &&
    unsupported.length === 0 &&
    dismissedPlugins.length === 0
  ) {
    return null;
  }

  // Collapsed: a small square floating at the top-left (overlays the chat, takes
  // no banner height) that re-expands on click.
  if (collapsed) {
    return (
      <div className="relative h-0">
        <button
          type="button"
          onClick={expand}
          aria-label={t('banner.expand')}
          title={t('banner.expand')}
          className="absolute left-0 top-0 z-20 flex size-7 items-center justify-center rounded-md border border-warm-border bg-warm-muted text-warm-text-muted shadow-sm hover:text-warm-text"
        >
          <PackagePlus className="size-4" aria-hidden />
        </button>
      </div>
    );
  }

  const renderRow = (f: InstallFailure, action: 'install' | 'retry') => {
    const sources = [f.source];
    return (
      <li key={`${f.source}-${f.ref ?? ''}`} className="space-y-1">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="font-mono">{f.source}</span>
          {f.ref && <span className="text-warm-text-muted">@{f.ref}</span>}
        </div>
        {f.stderr && (
          <pre className="text-[11px] font-mono whitespace-pre-wrap bg-warm-muted text-warm-text px-2 py-1 rounded max-h-24 overflow-y-auto">
            {f.stderr.slice(0, 800)}
          </pre>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="ghost"
            className="h-6 px-2"
            onClick={() => installMut.mutate(sources)}
            disabled={installMut.isPending}
          >
            {action === 'install' ? (
              <Download className="w-3 h-3 mr-1" aria-hidden />
            ) : (
              <RotateCw className="w-3 h-3 mr-1" aria-hidden />
            )}
            {action === 'install' ? t('banner.install_one') : t('banner.retry_install')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-6 px-2"
            onClick={() =>
              copyToClipboard(pluginInstallCommand(f.source, f.ref))
                .then(() => toast.success(t('banner.copied')))
                .catch(() => undefined)
            }
          >
            <Copy className="w-3 h-3 mr-1" aria-hidden />
            {t('banner.copy_command')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-6 px-2"
            onClick={() =>
              dismissMut.mutate({ source: f.source, dismissed: true })
            }
            disabled={dismissMut.isPending}
          >
            <EyeOff className="w-3 h-3 mr-1" aria-hidden />
            {t('banner.ignore')}
          </Button>
        </div>
      </li>
    );
  };

  return (
    <div className="flex items-start gap-1">
      {/* Hide control at the far-left: collapse the banner into a small square. */}
      <button
        type="button"
        onClick={collapse}
        aria-label={t('banner.collapse')}
        title={t('banner.collapse')}
        className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md text-warm-text-muted hover:bg-warm-muted hover:text-warm-text"
      >
        <Minimize2 className="size-3.5" aria-hidden />
      </button>
      <div className="min-w-0 flex-1 space-y-2">
      {restartRequired && (
        <Alert variant="warning" data-testid="banner-restart-required">
          <RefreshCw className="w-4 h-4" aria-hidden />
          <AlertTitle>{t('banner.restart_required_title')}</AlertTitle>
          <AlertDescription>{t('banner.restart_required')}</AlertDescription>
        </Alert>
      )}

      {missingRuntimes.length > 0 && (
        <Alert variant="warning" data-testid="banner-missing-runtimes">
          <TerminalSquare className="w-4 h-4" aria-hidden />
          <AlertTitle>{t('banner.missing_runtimes_title')}</AlertTitle>
          <AlertDescription>
            <p className="mb-1">
              {t('banner.missing_runtimes', {
                tools: missingRuntimes.join(', '),
              })}
            </p>
            <Button asChild size="sm" variant="ghost" className="h-6 px-2">
              <Link to="/settings">{t('banner.go_install_runtime')}</Link>
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {missing.length > 0 && (
        <Alert variant="destructive" data-testid="banner-missing-credentials">
          <KeyRound className="w-4 h-4" aria-hidden />
          <AlertTitle>{t('banner.missing_credentials_title')}</AlertTitle>
          <AlertDescription>
            <p className="mb-1">{t('banner.missing_credentials')}</p>
            <ul className="space-y-1">
              {missing.map((c) => (
                <li
                  key={c.alias}
                  className="flex flex-wrap items-center gap-2 text-xs"
                >
                  <span className="font-mono">{c.alias}</span>
                  <span className="text-warm-text-muted">({c.provider})</span>
                  {c.purpose && (
                    <span className="text-warm-text-muted">— {c.purpose}</span>
                  )}
                  <Button
                    asChild
                    size="sm"
                    variant="ghost"
                    className="h-6 px-2"
                  >
                    <Link
                      to="/settings/integrations"
                      search={{ provider: c.provider, alias: c.alias }}
                    >
                      {t('banner.go_bind')}
                    </Link>
                  </Button>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {pending.length > 0 && (
        <Alert variant="default" data-testid="banner-pending-plugins">
          <PackagePlus className="w-4 h-4" aria-hidden />
          <AlertTitle>{t('banner.pending_plugins_title')}</AlertTitle>
          <AlertDescription>
            <p className="mb-1">{t('banner.pending_plugins')}</p>
            <div className="mb-2">
              <Button
                size="sm"
                variant="default"
                onClick={() => installMut.mutate([])}
                disabled={installMut.isPending}
              >
                <Download className="w-3 h-3 mr-1" aria-hidden />
                {t('banner.install_all', { count: pending.length })}
              </Button>
            </div>
            <ul className="space-y-2">
              {pending.map((p) => renderRow(p, 'install'))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {failures.length > 0 && (
        <Alert variant="destructive" data-testid="banner-install-failures">
          <PackageX className="w-4 h-4" aria-hidden />
          <AlertTitle>{t('banner.install_failures_title')}</AlertTitle>
          <AlertDescription>
            <p className="mb-1">{t('banner.install_failures')}</p>
            <ul className="space-y-2">
              {failures.map((f) => renderRow(f, 'retry'))}
            </ul>
            <div className="mt-2 flex">
              <Button
                size="sm"
                variant="ghost"
                className="h-6 px-2"
                onClick={() => recomputeMut.mutate()}
                disabled={recomputeMut.isPending}
              >
                <RefreshCw className="w-3 h-3 mr-1" aria-hidden />
                {t('banner.recompute')}
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      )}

      {unsupported.length > 0 && (
        <Alert data-testid="banner-install-unsupported">
          <PackageX className="w-4 h-4" aria-hidden />
          <AlertTitle>{t('banner.install_unsupported_title')}</AlertTitle>
          <AlertDescription>
            <p className="mb-2">{t('banner.install_unsupported')}</p>
            <ul className="space-y-1 text-xs">
              {unsupported.map((u) => (
                <li key={`${u.source}-${u.ref ?? ''}`} className="font-mono">
                  {u.source}{u.ref ? `@${u.ref}` : ''}
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {dismissedPlugins.length > 0 && (
        <Alert variant="default" data-testid="banner-dismissed-plugins">
          <EyeOff className="w-4 h-4" aria-hidden />
          <AlertTitle>
            {t('banner.dismissed_title', { count: dismissedPlugins.length })}
          </AlertTitle>
          <AlertDescription>
            <ul className="space-y-1">
              {dismissedPlugins.map((source) => (
                <li
                  key={source}
                  className="flex flex-wrap items-center gap-2 text-xs"
                >
                  <span className="font-mono">{source}</span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 px-2"
                    onClick={() =>
                      dismissMut.mutate({ source, dismissed: false })
                    }
                    disabled={dismissMut.isPending}
                  >
                    <Undo2 className="w-3 h-3 mr-1" aria-hidden />
                    {t('banner.restore')}
                  </Button>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      </div>
    </div>
  );
}
