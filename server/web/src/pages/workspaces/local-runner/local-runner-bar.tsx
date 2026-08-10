import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { HardDriveDownload, Loader2, ScrollText, Settings, AlertTriangle } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { useLocalRunnerStore, useLocalRunner } from '@/stores/local-runner-store';
import { LocalRunnerConfigDialog } from './local-runner-config-dialog';
import { LocalRunnerLogDialog } from './local-runner-log-dialog';
import { useLocalRunnerLogs } from './use-local-runner-logs';

interface LocalRunnerBarProps {
  workspaceId: string;
  /** Human label forwarded to the desktop manager UI (falls back to the id). */
  workspaceName?: string;
}

/**
 * Bottom-toolbar content for the per-workspace local executor (#526·子A).
 *
 * Renders one of the state-machine faces (unbound / connecting / active /
 * error) and owns the config + log dialogs. Only mounted by the workspace page
 * when the desktop-remote signal says the entry is available.
 */
export function LocalRunnerBar({ workspaceId, workspaceName }: LocalRunnerBarProps) {
  const { t } = useTranslation('workspaces');
  const ensureLoaded = useLocalRunnerStore((s) => s.ensureLoaded);
  const runner = useLocalRunner(workspaceId);

  const [configOpen, setConfigOpen] = useState(false);
  const [configDirLocked, setConfigDirLocked] = useState(false);
  const [logOpen, setLogOpen] = useState(false);

  useEffect(() => {
    ensureLoaded(workspaceId, workspaceName);
  }, [ensureLoaded, workspaceId, workspaceName]);

  // Single owner of the log-stream subscription: live whenever the runner is
  // bound. Unsubscribes on unmount / unbind / workspace change (see hook).
  useLocalRunnerLogs(workspaceId, runner.status !== 'unbound');

  const openConfig = (dirLocked: boolean) => {
    setConfigDirLocked(dirLocked);
    setConfigOpen(true);
  };

  return (
    <>
      <div className="flex h-full items-center gap-2 px-3">
        {runner.status === 'unbound' && (
          <Button
            type="button"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={() => openConfig(false)}
          >
            <HardDriveDownload className="h-3.5 w-3.5 mr-1" aria-hidden="true" />
            {t('localRunner.bar.launch')}
          </Button>
        )}

        {runner.status === 'connecting' && (
          <span className="flex items-center gap-1.5 text-xs text-warm-text-muted">
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            {t('localRunner.bar.connecting')}
          </span>
        )}

        {runner.status === 'active' && (
          <>
            <span className="flex items-center gap-1.5 text-xs text-warm-text-muted">
              <span
                className="h-1.5 w-1.5 rounded-full bg-success"
                aria-hidden="true"
              />
              {t('localRunner.bar.active')}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => setLogOpen(true)}
            >
              <ScrollText className="h-3.5 w-3.5 mr-1" aria-hidden="true" />
              {t('localRunner.bar.viewLog')}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-6 w-6"
              aria-label={t('localRunner.bar.configure')}
              title={t('localRunner.bar.configure')}
              onClick={() => openConfig(true)}
            >
              <Settings className="h-3.5 w-3.5" aria-hidden="true" />
            </Button>
          </>
        )}

        {runner.status === 'error' && (
          <>
            <span className="flex items-center gap-1.5 text-xs text-destructive">
              <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
              {runner.error || t('localRunner.bar.error')}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => openConfig(false)}
            >
              {t('localRunner.bar.retry')}
            </Button>
          </>
        )}
      </div>

      <LocalRunnerConfigDialog
        workspaceId={workspaceId}
        workspaceName={workspaceName}
        dirLocked={configDirLocked}
        open={configOpen}
        onOpenChange={setConfigOpen}
      />

      <LocalRunnerLogDialog
        workspaceId={workspaceId}
        open={logOpen}
        onOpenChange={setLogOpen}
        onOpenConfig={() => {
          setLogOpen(false);
          openConfig(true);
        }}
      />
    </>
  );
}
