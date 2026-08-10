import { useTranslation } from 'react-i18next';
import { Settings, Terminal } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import { useLocalRunner, type LocalRunnerLogEntry } from '@/stores/local-runner-store';

interface LocalRunnerLogDialogProps {
  workspaceId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Opens the config dialog (directory locked) from the log header. */
  onOpenConfig: () => void;
}

/** Semantic-token color per log level — no hardcoded green/red. */
const LEVEL_CLASS: Record<LocalRunnerLogEntry['level'], string> = {
  command: 'text-brand',
  stdout: 'text-warm-text',
  stderr: 'text-destructive',
  system: 'text-warm-text-muted',
};

function formatTime(ts: number): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(new Date(ts));
  } catch {
    return '';
  }
}

export function LocalRunnerLogDialog({
  workspaceId,
  open,
  onOpenChange,
  onOpenConfig,
}: LocalRunnerLogDialogProps) {
  const { t } = useTranslation('workspaces');
  const runner = useLocalRunner(workspaceId);
  const logs = runner.logs;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[640px] max-h-[85vh]">
        <DialogHeader>
          <div className="flex items-start justify-between gap-3 pr-6">
            <div className="space-y-1">
              <DialogTitle>{t('localRunner.log.title')}</DialogTitle>
              <DialogDescription>
                {runner.config?.localDir
                  ? t('localRunner.log.boundTo', { dir: runner.config.localDir })
                  : t('localRunner.log.description')}
              </DialogDescription>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-8 px-2 shrink-0"
              onClick={onOpenConfig}
            >
              <Settings className="h-4 w-4 mr-1" aria-hidden="true" />
              {t('localRunner.log.configButton')}
            </Button>
          </div>
        </DialogHeader>

        <ScrollArea className="h-[52vh] rounded-md border border-warm-border bg-warm-muted">
          {logs.length === 0 ? (
            <div className="flex h-[52vh] flex-col items-center justify-center gap-2 text-center px-6">
              <Terminal
                className="h-8 w-8 text-warm-text-muted"
                aria-hidden="true"
              />
              <p className="text-sm text-warm-text-muted">
                {t('localRunner.log.empty')}
              </p>
            </div>
          ) : (
            <ul className="p-3 space-y-1 font-mono text-xs leading-relaxed">
              {logs.map((entry) => (
                <li key={entry.id} className="flex gap-2">
                  <span className="shrink-0 tabular-nums text-warm-text-muted">
                    {formatTime(entry.ts)}
                  </span>
                  <span className={`whitespace-pre-wrap break-all ${LEVEL_CLASS[entry.level]}`}>
                    {entry.level === 'command' ? '$ ' : ''}
                    {entry.text}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
