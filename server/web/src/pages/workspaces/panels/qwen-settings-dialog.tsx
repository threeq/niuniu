import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Bot } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogFooter,
  DialogTitle,
  DialogTrigger,
  DialogDescription,
} from '@/components/ui/dialog';

// Qwen Code currently has no per-workspace settings unique to it: the model and
// CLI command live in the shared workspace settings, and approval mode maps from
// the shared permission selector. This dialog keeps the agent button present and
// honest, pointing users at where Qwen's settings actually live. New
// Qwen-specific knobs would be added here as they appear.
export function QwenSettingsDialog() {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('panels.qwenSettings.triggerTitle')}
        >
          <Bot className="h-3.5 w-3.5" />
          <span>{t('panels.qwenSettings.trigger')}</span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('panels.qwenSettings.title')}</DialogTitle>
          <DialogDescription>{t('panels.qwenSettings.description')}</DialogDescription>
        </DialogHeader>
        <p className="text-sm text-muted-foreground">{t('panels.qwenSettings.sharedNote')}</p>
        <DialogFooter>
          <button
            onClick={() => setOpen(false)}
            className="rounded-md px-3 py-1.5 text-sm text-foreground hover:bg-accent"
          >
            {t('common:actions.close')}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
