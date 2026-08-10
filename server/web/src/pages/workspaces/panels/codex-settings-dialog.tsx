import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
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
import { api } from '@/lib/api';
import type { Workspace, CodexSandboxMode, CodexApprovalPolicy } from '@/types/api';

interface CodexSettingsDialogProps {
  workspace: Workspace;
}

const SANDBOX_MODES: CodexSandboxMode[] = ['read-only', 'workspace-write', 'danger-full-access'];
const APPROVAL_POLICIES: CodexApprovalPolicy[] = ['untrusted', 'on-failure', 'on-request', 'never'];

// Codex-specific settings: sandbox mode + approval policy (workspace columns,
// persisted via PUT /workspaces/:id/codex-sandbox). These have no Claude/Qwen
// equivalent, so they live in the Codex agent dialog.
export function CodexSettingsDialog({ workspace }: CodexSettingsDialogProps) {
  const { t } = useTranslation('workspaces');
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [sandboxMode, setSandboxMode] = useState<CodexSandboxMode>('danger-full-access');
  const [approvalPolicy, setApprovalPolicy] = useState<CodexApprovalPolicy>('never');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setSandboxMode(workspace.codex_sandbox_mode ?? 'danger-full-access');
    setApprovalPolicy(workspace.codex_approval_policy ?? 'never');
  }, [open, workspace.codex_sandbox_mode, workspace.codex_approval_policy]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await api.put(`/workspaces/${workspace.id}/codex-sandbox`, {
        sandbox_mode: sandboxMode,
        approval_policy: approvalPolicy,
      });
      queryClient.invalidateQueries({ queryKey: ['workspace', workspace.id] });
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
      setOpen(false);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('panels.codexSettings.triggerTitle')}
        >
          <Bot className="h-3.5 w-3.5" />
          <span>{t('panels.codexSettings.trigger')}</span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('panels.codexSettings.title')}</DialogTitle>
          <DialogDescription>{t('panels.codexSettings.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">
              {t('panels.codexSettings.sandboxLabel')}
            </label>
            <select
              value={sandboxMode}
              onChange={(e) => setSandboxMode(e.target.value as CodexSandboxMode)}
              className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-info"
            >
              {SANDBOX_MODES.map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">
              {t('panels.codexSettings.approvalLabel')}
            </label>
            <select
              value={approvalPolicy}
              onChange={(e) => setApprovalPolicy(e.target.value as CodexApprovalPolicy)}
              className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-info"
            >
              {APPROVAL_POLICIES.map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </select>
          </div>
        </div>

        <DialogFooter>
          <button
            onClick={() => setOpen(false)}
            className="rounded-md px-3 py-1.5 text-sm text-foreground hover:bg-accent"
          >
            {t('common:actions.cancel')}
          </button>
          <button
            onClick={handleSave}
            disabled={saving}
            className="rounded-md bg-info px-3 py-1.5 text-sm text-white hover:bg-info/90 disabled:opacity-50"
          >
            {saving ? t('panels.codexSettings.saving') : t('common:actions.save')}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
