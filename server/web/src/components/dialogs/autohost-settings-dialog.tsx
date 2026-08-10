import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RotateCcw, Info } from 'lucide-react';
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
import { api } from '@/lib/api';

const AUTOHOST_BUDGET_KEY = 'NIUNIU_AUTOHOST_BUDGET';
const AUTOHOST_ERROR_BUDGET_KEY = 'NIUNIU_AUTOHOST_ERROR_BUDGET';
const AUTOHOST_CONTINUE_PROMPT_KEY = 'NIUNIU_AUTOHOST_CONTINUE_PROMPT';
const AUTOHOST_RECOVER_PROMPT_KEY = 'NIUNIU_AUTOHOST_RECOVER_PROMPT';
const AUTOHOST_GOAL_CONDITION_KEY = 'NIUNIU_AUTOHOST_GOAL_CONDITION';
const AUTOHOST_BUDGET_DEFAULT = '12';
const AUTOHOST_ERROR_BUDGET_DEFAULT = '3';
const AUTOHOST_GOAL_CONDITION_MAX = 4000;

// Default prompts must stay in sync with
// server/internal/agentproxy/autohost.go (autohostContinuePrompt /
// autohostRecoverPrompt).
const AUTOHOST_CONTINUE_PROMPT_DEFAULT =
  `继续推进当前任务的剩余步骤。遇到选择请按你的最佳判断决定，不要停下来询问我。\n\n如果当前任务已经全部完成，请在回复末尾单独输出一行：[AUTOHOST_DONE]`;
const AUTOHOST_RECOVER_PROMPT_DEFAULT =
  `上一轮以错误结束。请先诊断失败原因（看日志/报错信息），然后换一种方式继续推进。不要重复完全相同的失败操作。\n\n如果错误确实无法恢复（例如外部依赖不可用、需要人工介入的凭证问题），请在回复末尾单独输出一行：[AUTOHOST_DONE]`;

interface AutohostSettingsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workspaceId: string;
}

export function AutohostSettingsDialog({ open, onOpenChange, workspaceId }: AutohostSettingsDialogProps) {
  const { t } = useTranslation('workspaces');
  const [budget, setBudget] = useState(AUTOHOST_BUDGET_DEFAULT);
  const [errorBudget, setErrorBudget] = useState(AUTOHOST_ERROR_BUDGET_DEFAULT);
  const [continuePrompt, setContinuePrompt] = useState('');
  const [recoverPrompt, setRecoverPrompt] = useState('');
  const [fallbackCondition, setFallbackCondition] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);

  // Reload from server every time the dialog opens — keep stale state out of
  // the form. The mode selector lives in PermissionSelector and writes
  // independently, so we don't touch NIUNIU_PERMISSION_MODE here.
  useEffect(() => {
    if (!open) return;
    api
      .get<{ env: Record<string, string> }>(`/workspaces/${workspaceId}/env`)
      .then((res) => {
        setBudget(res.env[AUTOHOST_BUDGET_KEY] || AUTOHOST_BUDGET_DEFAULT);
        setErrorBudget(res.env[AUTOHOST_ERROR_BUDGET_KEY] || AUTOHOST_ERROR_BUDGET_DEFAULT);
        setContinuePrompt(res.env[AUTOHOST_CONTINUE_PROMPT_KEY] ?? '');
        setRecoverPrompt(res.env[AUTOHOST_RECOVER_PROMPT_KEY] ?? '');
        setFallbackCondition(res.env[AUTOHOST_GOAL_CONDITION_KEY] ?? '');
      })
      .catch(() => {});
  }, [open, workspaceId]);

  const normalizeInt = (raw: string, fallback: string) => {
    const trimmed = raw.trim();
    return /^\d+$/.test(trimmed) ? trimmed : fallback;
  };

  const handleSave = async () => {
    setIsSubmitting(true);
    try {
      // Read latest env so we don't clobber unrelated keys, then patch.
      const existing = await api
        .get<{ env: Record<string, string> }>(`/workspaces/${workspaceId}/env`)
        .then((r) => r.env)
        .catch(() => ({}) as Record<string, string>);
      existing[AUTOHOST_BUDGET_KEY] = normalizeInt(budget, AUTOHOST_BUDGET_DEFAULT);
      existing[AUTOHOST_ERROR_BUDGET_KEY] = normalizeInt(errorBudget, AUTOHOST_ERROR_BUDGET_DEFAULT);
      existing[AUTOHOST_CONTINUE_PROMPT_KEY] = continuePrompt.trim();
      existing[AUTOHOST_RECOVER_PROMPT_KEY] = recoverPrompt.trim();
      existing[AUTOHOST_GOAL_CONDITION_KEY] = fallbackCondition.trim();
      await api.put(`/workspaces/${workspaceId}/env`, { env: existing });
      onOpenChange(false);
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px] max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>{t('panels.permissions.autohost.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('panels.permissions.autohost.dialogDescription')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-6 py-2">
          {/* Budgets section */}
          <section className="space-y-3">
            <h3 className="text-sm font-medium text-foreground">
              {t('panels.permissions.autohost.budgetSection')}
            </h3>

            <div className="grid grid-cols-[1fr_auto] gap-3 items-center">
              <div>
                <label htmlFor="autohost-continue-budget" className="text-sm font-medium text-foreground">
                  {t('panels.permissions.autohost.continueBudget')}
                </label>
                <p className="text-xs text-muted-foreground mt-0.5">
                  {t('panels.permissions.autohost.continueBudgetDesc')}
                </p>
              </div>
              <Input
                id="autohost-continue-budget"
                type="number"
                min={0}
                value={budget}
                onChange={(e) => setBudget(e.target.value)}
                className="w-20 text-right"
                disabled={isSubmitting}
              />
            </div>

            <div className="grid grid-cols-[1fr_auto] gap-3 items-center">
              <div>
                <label htmlFor="autohost-error-budget" className="text-sm font-medium text-foreground">
                  {t('panels.permissions.autohost.errorBudget')}
                </label>
                <p className="text-xs text-muted-foreground mt-0.5">
                  {t('panels.permissions.autohost.errorBudgetDesc')}
                </p>
              </div>
              <Input
                id="autohost-error-budget"
                type="number"
                min={0}
                value={errorBudget}
                onChange={(e) => setErrorBudget(e.target.value)}
                className="w-20 text-right"
                disabled={isSubmitting}
              />
            </div>
          </section>

          {/* Prompts section */}
          <section className="space-y-3">
            <h3 className="text-sm font-medium text-foreground">
              {t('panels.permissions.autohost.promptSection')}
            </h3>

            <div className="space-y-2">
              <div className="flex items-end justify-between gap-2">
                <div>
                  <label htmlFor="autohost-continue-prompt" className="text-sm font-medium text-foreground">
                    {t('panels.permissions.autohost.continuePrompt')}
                  </label>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {t('panels.permissions.autohost.continuePromptDesc')}
                  </p>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  onClick={() => setContinuePrompt(AUTOHOST_CONTINUE_PROMPT_DEFAULT)}
                  disabled={isSubmitting}
                >
                  <RotateCcw className="h-3 w-3 mr-1" />
                  {t('panels.permissions.autohost.useDefault')}
                </Button>
              </div>
              <Textarea
                id="autohost-continue-prompt"
                rows={5}
                value={continuePrompt}
                onChange={(e) => setContinuePrompt(e.target.value)}
                placeholder={AUTOHOST_CONTINUE_PROMPT_DEFAULT}
                disabled={isSubmitting}
                className="font-mono text-xs leading-relaxed"
              />
            </div>

            <div className="space-y-2">
              <div className="flex items-end justify-between gap-2">
                <div>
                  <label htmlFor="autohost-recover-prompt" className="text-sm font-medium text-foreground">
                    {t('panels.permissions.autohost.recoverPrompt')}
                  </label>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {t('panels.permissions.autohost.recoverPromptDesc')}
                  </p>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  onClick={() => setRecoverPrompt(AUTOHOST_RECOVER_PROMPT_DEFAULT)}
                  disabled={isSubmitting}
                >
                  <RotateCcw className="h-3 w-3 mr-1" />
                  {t('panels.permissions.autohost.useDefault')}
                </Button>
              </div>
              <Textarea
                id="autohost-recover-prompt"
                rows={5}
                value={recoverPrompt}
                onChange={(e) => setRecoverPrompt(e.target.value)}
                placeholder={AUTOHOST_RECOVER_PROMPT_DEFAULT}
                disabled={isSubmitting}
                className="font-mono text-xs leading-relaxed"
              />
            </div>

            <div className="flex items-start gap-1.5 text-xs text-muted-foreground bg-muted/40 rounded px-2 py-1.5">
              <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
              <span>{t('panels.permissions.autohost.promptHint')}</span>
            </div>
          </section>

          {/* Fallback completion-criterion section — used by the LLM judge only
              when the bound issue has no per-issue goal_condition. Per-issue
              criteria (set in the issue detail page) take precedence. */}
          <section className="space-y-3">
            <h3 className="text-sm font-medium text-foreground">
              {t('autohost.fallbackCondition.title')}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t('autohost.fallbackCondition.hint')}
            </p>
            <Textarea
              id="autohost-fallback-condition"
              rows={3}
              value={fallbackCondition}
              onChange={(e) => setFallbackCondition(e.target.value)}
              placeholder={t('autohost.fallbackCondition.placeholder')}
              maxLength={AUTOHOST_GOAL_CONDITION_MAX}
              disabled={isSubmitting}
              className="font-mono text-xs leading-relaxed"
            />
            <div className="text-xs text-muted-foreground">
              {t('autohost.fallbackCondition.charCount', {
                n: fallbackCondition.length,
                max: AUTOHOST_GOAL_CONDITION_MAX,
              })}
            </div>
          </section>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={isSubmitting}
          >
            {t('common:actions.cancel')}
          </Button>
          <Button type="button" onClick={handleSave} disabled={isSubmitting}>
            {isSubmitting ? t('common:actions.saving') : t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
