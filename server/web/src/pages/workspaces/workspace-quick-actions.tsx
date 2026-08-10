import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { MonitorSmartphone, Copy, Check, TerminalSquare, FolderOpen } from 'lucide-react';
import type { Workspace } from '@/types/api';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { openInVSCode } from '@/lib/vscode';
import { openInExplorer } from '@/lib/shell';
import { isPersonalEdition } from '@/lib/edition';
import { Button } from '@/components/ui/button';

interface QuickActionsProps {
  workspace: Workspace;
}

export function WorkspaceQuickActions({ workspace }: QuickActionsProps) {
  const { t } = useTranslation('workspaces');
  const { togglePanel } = useWorkspacePanelStore();
  const [copied, setCopied] = useState(false);
  // Personal edition shows "Open File Explorer" instead of "Open VS Code":
  // the workspace path lives on the user's own machine so revealing it in
  // Explorer/Finder is more useful than launching the VS Code URL handler.
  const [personal, setPersonal] = useState(false);

  useEffect(() => {
    isPersonalEdition().then(setPersonal);
  }, []);

  const handleOpenVSCode = () => {
    openInVSCode(workspace.path).catch(() => {})
  };

  const handleOpenExplorer = () => {
    openInExplorer(workspace.path).catch(() => {})
  };

  const handleCopyPath = async () => {
    await navigator.clipboard.writeText(workspace.path);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleToggleTerminal = () => {
    togglePanel('terminal');
  };

  const iconBtn = 'h-8 w-8 rounded-md text-warm-text-muted hover:text-warm-text';

  return (
    <div className="w-10 border-l border-warm-border bg-warm-muted flex flex-col items-center py-2 gap-1 shrink-0">
      {personal ? (
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('sideToolbar.openInExplorer')}
          title={t('sideToolbar.openInExplorer')}
          onClick={handleOpenExplorer}
          className={iconBtn}
        >
          <FolderOpen className="w-4 h-4" aria-hidden="true" />
        </Button>
      ) : (
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('sideToolbar.openInVSCode')}
          title={t('sideToolbar.openInVSCode')}
          onClick={handleOpenVSCode}
          className={iconBtn}
        >
          <MonitorSmartphone className="w-4 h-4" aria-hidden="true" />
        </Button>
      )}

      <Button
        variant="ghost"
        size="icon"
        aria-label={t('sideToolbar.copyPath')}
        title={t('sideToolbar.copyPath')}
        onClick={handleCopyPath}
        className={iconBtn}
      >
        {copied ? (
          <Check className="w-4 h-4 text-success" aria-hidden="true" />
        ) : (
          <Copy className="w-4 h-4" aria-hidden="true" />
        )}
      </Button>

      <Button
        variant="ghost"
        size="icon"
        aria-label={t('sideToolbar.terminal')}
        title={t('sideToolbar.terminal')}
        onClick={handleToggleTerminal}
        className={iconBtn}
      >
        <TerminalSquare className="w-4 h-4" aria-hidden="true" />
      </Button>
    </div>
  );
}
