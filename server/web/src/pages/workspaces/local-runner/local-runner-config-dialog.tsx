import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RotateCcw, Lock, Info, FolderOpen } from 'lucide-react';
import {
  desktopDirPickAvailable,
  requestDesktopDirPick,
  DESKTOP_DIR_PICKED_EVENT,
} from '@/lib/desktop-runner-context';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';
import {
  useLocalRunnerStore,
  useLocalRunner,
  DEFAULT_ALLOWED_COMMANDS,
  type LocalRunnerConfig,
} from '@/stores/local-runner-store';

interface LocalRunnerConfigDialogProps {
  workspaceId: string;
  /** Human label forwarded to the desktop manager UI. */
  workspaceName?: string;
  /** When true the working-directory field is read-only (re-configure flow). */
  dirLocked: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/** Split a newline/comma separated whitelist textarea into a clean list. */
function parseCommands(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((c) => c.trim())
    .filter(Boolean);
}

export function LocalRunnerConfigDialog({
  workspaceId,
  workspaceName,
  dirLocked,
  open,
  onOpenChange,
}: LocalRunnerConfigDialogProps) {
  const { t } = useTranslation('workspaces');
  const runner = useLocalRunner(workspaceId);
  const saveConfig = useLocalRunnerStore((s) => s.saveConfig);
  const defaultPrompt = t('localRunner.config.defaultPrompt');

  const [localDir, setLocalDir] = useState('');
  const [promptSnippet, setPromptSnippet] = useState('');
  const [allowedRaw, setAllowedRaw] = useState(DEFAULT_ALLOWED_COMMANDS.join('\n'));
  const [alwaysAllowPersist, setAlwaysAllowPersist] = useState(false);
  const [saving, setSaving] = useState(false);
  const [canPickDir, setCanPickDir] = useState(false);

  // Native directory picker (desktop only): the "browse" button asks Go to open
  // a folder dialog; the chosen path arrives as a window CustomEvent. Only wired
  // while the dialog is open and the directory is editable.
  useEffect(() => {
    if (!open || dirLocked) return;
    setCanPickDir(desktopDirPickAvailable());
    const onPicked = (e: Event) => {
      const path = (e as CustomEvent<{ path?: string }>).detail?.path;
      if (typeof path === 'string' && path) setLocalDir(path);
    };
    window.addEventListener(DESKTOP_DIR_PICKED_EVENT, onPicked);
    return () => window.removeEventListener(DESKTOP_DIR_PICKED_EVENT, onPicked);
  }, [open, dirLocked]);

  // Hydrate the form from the current config every time the dialog opens so a
  // stale draft never leaks between opens.
  useEffect(() => {
    if (!open) return;
    const cfg = runner.config;
    setLocalDir(cfg?.localDir ?? '');
    setPromptSnippet(cfg?.promptSnippet || defaultPrompt);
    setAllowedRaw(
      (cfg?.allowedCommands ?? DEFAULT_ALLOWED_COMMANDS).join('\n'),
    );
    setAlwaysAllowPersist(cfg?.alwaysAllowPersist ?? false);
    // Depend on `open` only: opening is the intended re-sync point.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const dirValid = localDir.trim().length > 0;

  const handleSave = async () => {
    if (!dirValid || saving) return;
    setSaving(true);
    const config: LocalRunnerConfig = {
      localDir: localDir.trim(),
      promptSnippet: promptSnippet.trim() || defaultPrompt,
      allowedCommands: parseCommands(allowedRaw),
      alwaysAllowPersist,
    };
    try {
      await saveConfig(workspaceId, config, workspaceName);
      onOpenChange(false);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px] max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('localRunner.config.title')}</DialogTitle>
          <DialogDescription>{t('localRunner.config.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-6 py-2">
          {/* Working directory */}
          <div className="space-y-1.5">
            <Label htmlFor="local-runner-dir" className="flex items-center gap-1.5">
              {t('localRunner.config.dirLabel')}
              {dirLocked && (
                <Lock className="h-3 w-3 text-warm-text-muted" aria-hidden="true" />
              )}
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="local-runner-dir"
                value={localDir}
                onChange={(e) => setLocalDir(e.target.value)}
                placeholder={t('localRunner.config.dirPlaceholder')}
                disabled={dirLocked || saving}
                autoComplete="off"
                spellCheck={false}
              />
              {!dirLocked && canPickDir && (
                <Button
                  type="button"
                  variant="secondary"
                  className="shrink-0"
                  onClick={() => requestDesktopDirPick()}
                  disabled={saving}
                >
                  <FolderOpen className="h-4 w-4 mr-1.5" aria-hidden="true" />
                  {t('localRunner.config.browse')}
                </Button>
              )}
            </div>
            <p className="text-xs text-warm-text-muted">
              {dirLocked
                ? t('localRunner.config.dirLockedHint')
                : t('localRunner.config.dirHint')}
            </p>
          </div>

          {/* Prompt snippet */}
          <div className="space-y-1.5">
            <div className="flex items-end justify-between gap-2">
              <Label htmlFor="local-runner-prompt">
                {t('localRunner.config.promptLabel')}
              </Label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setPromptSnippet(defaultPrompt)}
                disabled={saving}
              >
                <RotateCcw className="h-3 w-3 mr-1" aria-hidden="true" />
                {t('localRunner.config.useDefault')}
              </Button>
            </div>
            <Textarea
              id="local-runner-prompt"
              rows={4}
              value={promptSnippet}
              onChange={(e) => setPromptSnippet(e.target.value)}
              placeholder={defaultPrompt}
              disabled={saving}
              className="text-xs leading-relaxed"
            />
            <p className="text-xs text-warm-text-muted">
              {t('localRunner.config.promptHint')}
            </p>
          </div>

          {/* Allowed-command whitelist */}
          <div className="space-y-1.5">
            <Label htmlFor="local-runner-allowed">
              {t('localRunner.config.allowedLabel')}
            </Label>
            <Textarea
              id="local-runner-allowed"
              rows={4}
              value={allowedRaw}
              onChange={(e) => setAllowedRaw(e.target.value)}
              placeholder={DEFAULT_ALLOWED_COMMANDS.join('\n')}
              disabled={saving}
              className="font-mono text-xs leading-relaxed"
            />
            <p className="text-xs text-warm-text-muted">
              {t('localRunner.config.allowedHint')}
            </p>
          </div>

          {/* Persist "always allow" */}
          <div className="flex items-start justify-between gap-3">
            <div className="space-y-0.5">
              <Label htmlFor="local-runner-persist">
                {t('localRunner.config.persistLabel')}
              </Label>
              <p className="text-xs text-warm-text-muted">
                {t('localRunner.config.persistHint')}
              </p>
            </div>
            <Switch
              id="local-runner-persist"
              checked={alwaysAllowPersist}
              onCheckedChange={setAlwaysAllowPersist}
              disabled={saving}
            />
          </div>

          <div className="flex items-start gap-1.5 text-xs text-warm-text-muted bg-warm-muted rounded px-2 py-1.5">
            <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" aria-hidden="true" />
            <span>{t('localRunner.config.stubHint')}</span>
          </div>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="secondary"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t('common:actions.cancel')}
          </Button>
          <Button type="button" onClick={handleSave} disabled={!dirValid || saving}>
            {saving ? t('common:actions.saving') : t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
