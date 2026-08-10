import { useState, useEffect } from 'react';
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
import { Switch } from '@/components/ui/switch';
import { api } from '@/lib/api';

interface ClaudeSettingsDialogProps {
  workspaceId: string;
}

// Auto-compaction keys (read by server/internal/agentproxy/auto_compact.go).
// Surfaced as a Switch + threshold field so users never hand-edit env vars.
const AUTO_COMPACT_KEY = 'NIUNIU_AUTO_COMPACT';
const AUTO_COMPACT_PERCENT_KEY = 'NIUNIU_AUTO_COMPACT_PERCENT';
const AUTO_COMPACT_PERCENT_DEFAULT = '70';
// Context-window budget the threshold is measured against. Stored in env as raw
// tokens (backend reads tokens), but entered/displayed in the UI as **K tokens**
// to avoid trailing zeros (1M window → user types 1000). Default 1M (=1000 K).
// Users on a smaller window should lower this; otherwise compaction fires too
// late / too early relative to their real window.
const AUTO_COMPACT_BUDGET_KEY = 'NIUNIU_AUTO_COMPACT_BUDGET';
const AUTO_COMPACT_BUDGET_DEFAULT_RAW = 1000000; // tokens
const AUTO_COMPACT_BUDGET_DEFAULT_K = '1000'; // UI value (K tokens)
// raw tokens -> K display string; falls back to the default when unparseable.
const budgetRawToK = (raw: string | undefined): string =>
  raw && /^\d+$/.test(raw) && Number(raw) > 0
    ? String(Math.round(Number(raw) / 1000))
    : AUTO_COMPACT_BUDGET_DEFAULT_K;

// Shared agent fields (NIUNIU_AGENT_COMMAND / _ARGS / NIUNIU_MODEL /
// NIUNIU_ALLOWED_TOOLS) used to live here, but they apply to every engine, so
// they were moved to the workspace settings dialog. This dialog now holds only
// Claude-specific settings.

// CLAUDE_* env vars — passed as environment to child process
const CLAUDE_ENV_FIELDS = [
  { key: 'CLAUDE_CODE_MAX_TURNS', i18nKey: 'maxTurns' },
  { key: 'MAX_THINKING_TOKENS', i18nKey: 'thinkingTokens' },
] as const;

export function ClaudeSettingsDialog({ workspaceId }: ClaudeSettingsDialogProps) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [autoCompact, setAutoCompact] = useState(true);
  const [compactPercent, setCompactPercent] = useState(AUTO_COMPACT_PERCENT_DEFAULT);
  const [compactBudget, setCompactBudget] = useState(AUTO_COMPACT_BUDGET_DEFAULT_K);
  const [saving, setSaving] = useState(false);

  const allKnownKeys = new Set([
    ...CLAUDE_ENV_FIELDS.map((f) => f.key),
    'NIUNIU_PERMISSION_MODE', // managed by PermissionSelector
    AUTO_COMPACT_KEY, // managed by the auto-compaction Switch below
    AUTO_COMPACT_PERCENT_KEY,
    AUTO_COMPACT_BUDGET_KEY,
  ]);

  useEffect(() => {
    if (!open) return;
    api
      .get<{ env: Record<string, string> }>(`/workspaces/${workspaceId}/env`)
      .then((res) => {
        const known: Record<string, string> = {};
        for (const [key, value] of Object.entries(res.env)) {
          if (allKnownKeys.has(key)) {
            known[key] = value;
          }
        }
        setFields(known);
        // Absent key => default ON (matches the backend default).
        setAutoCompact(res.env[AUTO_COMPACT_KEY] !== '0');
        setCompactPercent(res.env[AUTO_COMPACT_PERCENT_KEY] || AUTO_COMPACT_PERCENT_DEFAULT);
        setCompactBudget(budgetRawToK(res.env[AUTO_COMPACT_BUDGET_KEY]));
      })
      .catch(() => {
        setFields({});
        setAutoCompact(true);
        setCompactPercent(AUTO_COMPACT_PERCENT_DEFAULT);
        setCompactBudget(AUTO_COMPACT_BUDGET_DEFAULT_K);
      });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, workspaceId]);

  const handleSave = async () => {
    setSaving(true);
    try {
      // Get all existing env vars so we don't lose unrelated ones
      const existing = await api
        .get<{ env: Record<string, string> }>(`/workspaces/${workspaceId}/env`)
        .then((r) => r.env)
        .catch(() => ({}) as Record<string, string>);

      const envMap: Record<string, string> = { ...existing };

      // Set all known fields (remove empty)
      for (const { key } of CLAUDE_ENV_FIELDS) {
        const val = fields[key]?.trim();
        if (val) {
          envMap[key] = val;
        } else {
          delete envMap[key];
        }
      }

      // Auto-compaction: store the switch as "1"/"0". The threshold is only
      // persisted when it differs from the default and is a valid 1–99 percent;
      // otherwise drop the key so the backend default (70) applies.
      envMap[AUTO_COMPACT_KEY] = autoCompact ? '1' : '0';
      const p = compactPercent.trim();
      if (/^\d+$/.test(p) && Number(p) > 0 && Number(p) < 100 && p !== AUTO_COMPACT_PERCENT_DEFAULT) {
        envMap[AUTO_COMPACT_PERCENT_KEY] = p;
      } else {
        delete envMap[AUTO_COMPACT_PERCENT_KEY];
      }
      // Budget: entered in K tokens, stored as raw tokens. Persist only a
      // positive integer that differs from the default (1M); otherwise drop the
      // key so the backend default applies.
      const kb = compactBudget.trim();
      const rawB = /^\d+$/.test(kb) ? Number(kb) * 1000 : 0;
      if (rawB > 0 && rawB !== AUTO_COMPACT_BUDGET_DEFAULT_RAW) {
        envMap[AUTO_COMPACT_BUDGET_KEY] = String(rawB);
      } else {
        delete envMap[AUTO_COMPACT_BUDGET_KEY];
      }

      await api.put(`/workspaces/${workspaceId}/env`, { env: envMap });
      setOpen(false);
    } finally {
      setSaving(false);
    }
  };

  const updateField = (key: string, value: string) => {
    setFields((prev) => ({ ...prev, [key]: value }));
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('panels.claudeSettings.triggerTitle')}
        >
          <Bot className="h-3.5 w-3.5" />
          <span>{t('panels.claudeSettings.trigger')}</span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-md max-h-[80vh]">
        <DialogHeader>
          <DialogTitle>{t('panels.claudeSettings.title')}</DialogTitle>
          <DialogDescription>
            {t('panels.claudeSettings.description')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Section: Context management (auto-compaction) */}
          <div>
            <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">
              {t('panels.claudeSettings.autoCompact.section')}
            </h4>
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <label className="block text-sm font-medium text-foreground">
                  {t('panels.claudeSettings.autoCompact.label')}
                </label>
                <p className="text-xs text-muted-foreground mt-0.5">
                  {t('panels.claudeSettings.autoCompact.desc')}
                </p>
              </div>
              <Switch checked={autoCompact} onCheckedChange={setAutoCompact} />
            </div>
            {autoCompact && (
              <div className="flex items-center justify-between gap-3 mt-3">
                <div className="min-w-0">
                  <label className="block text-sm font-medium text-foreground">
                    {t('panels.claudeSettings.autoCompact.percentLabel')}
                  </label>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {t('panels.claudeSettings.autoCompact.percentDesc')}
                  </p>
                </div>
                <input
                  type="number"
                  min={1}
                  max={99}
                  value={compactPercent}
                  onChange={(e) => setCompactPercent(e.target.value)}
                  className="w-16 rounded-md border border-border px-2 py-1.5 text-sm text-right focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent bg-background"
                />
              </div>
            )}
            {autoCompact && (
              <div className="flex items-center justify-between gap-3 mt-3">
                <div className="min-w-0">
                  <label className="block text-sm font-medium text-foreground">
                    {t('panels.claudeSettings.autoCompact.budgetLabel')}
                  </label>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {t('panels.claudeSettings.autoCompact.budgetDesc')}
                  </p>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  <input
                    type="number"
                    min={1}
                    step={100}
                    value={compactBudget}
                    onChange={(e) => setCompactBudget(e.target.value)}
                    className="w-20 rounded-md border border-border px-2 py-1.5 text-sm text-right focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent bg-background"
                  />
                  <span className="text-xs text-muted-foreground">K</span>
                </div>
              </div>
            )}
          </div>

          <hr className="border-border" />

          {/* Section: Claude env vars */}
          <div>
            <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">
              {t('panels.claudeSettings.envVars')}
            </h4>
            <p className="text-[10px] text-muted-foreground mb-3">
              {t('panels.claudeSettings.envVarsDesc')}
            </p>
            {CLAUDE_ENV_FIELDS.map(({ key, i18nKey }) => (
              <div key={key} className="mb-3">
                <label className="block text-sm font-medium text-foreground mb-1">{t(`panels.claudeSettings.fields.${i18nKey}.label`)}</label>
                <input
                  type="text"
                  value={fields[key] ?? ''}
                  onChange={(e) => updateField(key, e.target.value)}
                  placeholder={t(`panels.claudeSettings.fields.${i18nKey}.placeholder`)}
                  className="w-full rounded-md border border-border px-3 py-1.5 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent bg-background"
                />
                <p className="text-xs text-muted-foreground mt-0.5">{key}</p>
              </div>
            ))}
          </div>
        </div>

        {/* Footer */}
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
            {saving ? t('panels.claudeSettings.saving') : t('common:actions.save')}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
