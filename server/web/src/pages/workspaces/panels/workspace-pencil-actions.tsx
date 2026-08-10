import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { PenTool, ExternalLink, Code2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { useChatInputBridge } from '@/stores/chat-input-bridge-store';
import {
  isDesktopShell,
  requestOpenInPencil,
  OPEN_PENCIL_RESULT_EVENT,
  type OpenPencilResult,
} from '@/lib/desktop-runner-context';

interface WorkspacePencilActionsProps {
  workspaceId: number;
}

/**
 * pencil-design scene action card: launch the OpenPencil desktop app ("在
 * OpenPencil 中打开") and one-click-send the "按设计生成代码" prompt to the agent
 * (reusing the chat-input bridge). Rendered by the scenes panel only when a
 * pencil-design layer is attached. Design-system compliant: shadcn Button,
 * warm-* tokens, lucide icons, t() strings, dark-mode-safe.
 */
export function WorkspacePencilActions({ workspaceId }: WorkspacePencilActionsProps) {
  const { t } = useTranslation('scenes');
  const requestChatInput = useChatInputBridge((s) => s.request);

  // Surface the desktop launch outcome (dispatched by Go) as a toast.
  useEffect(() => {
    function onResult(e: Event) {
      const detail = (e as CustomEvent<OpenPencilResult>).detail;
      if (!detail) return;
      if (detail.ok) {
        toast.success(t('pencil.launch_ok'));
      } else if (detail.reason === 'not_installed') {
        toast.error(t('pencil.launch_missing'));
      } else {
        toast.error(t('pencil.launch_failed'));
      }
    }
    window.addEventListener(OPEN_PENCIL_RESULT_EVENT, onResult);
    return () => window.removeEventListener(OPEN_PENCIL_RESULT_EVENT, onResult);
  }, [t]);

  function handleOpen() {
    if (!isDesktopShell()) {
      toast.info(t('pencil.desktop_only'));
      return;
    }
    if (requestOpenInPencil()) {
      toast.loading(t('pencil.launching'), { duration: 1500 });
    }
  }

  function handleGenerate() {
    requestChatInput(String(workspaceId), t('pencil.generate_prompt'), true);
    toast.success(t('pencil.generate_sent'));
  }

  return (
    <div className="border border-warm-border rounded-md p-3 bg-warm-surface space-y-2">
      <div className="flex items-center gap-2">
        <PenTool className="w-4 h-4 text-warm-text-muted shrink-0" aria-hidden />
        <h4 className="text-sm font-medium text-warm-text">{t('pencil.title')}</h4>
      </div>
      <p className="text-xs text-warm-text-muted">{t('pencil.hint')}</p>
      <div className="flex flex-wrap gap-2 pt-1">
        <Button type="button" size="sm" variant="secondary" onClick={handleOpen}>
          <ExternalLink className="w-4 h-4 mr-1" aria-hidden />
          {t('pencil.open')}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={handleGenerate}>
          <Code2 className="w-4 h-4 mr-1" aria-hidden />
          {t('pencil.generate')}
        </Button>
      </div>
    </div>
  );
}
