import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Plug, AlertTriangle, Loader2, RefreshCw, Power } from 'lucide-react';
import { toast } from 'sonner';
import { api, mcpApi } from '@/lib/api';
import type { WorkspaceMCPState } from '@/types/api';
import { Checkbox } from '@/components/ui/checkbox';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Label } from '@/components/ui/label';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';

// Per-workspace MCP configuration panel. Mounted inside the 工作空间场景
// (WorkspaceScenesPanel) dialog — the effective/projection list lives there as
// the read-only summary, so this panel only owns the editable parts: per-
// workspace overrides + the strict-isolation toggle.
// Lets the user view/toggle which detected MCP servers are enabled for this
// workspace, surface unavailable servers (selected but no longer installed
// under the current Claude account), and trigger a re-detection.
//
// Spec: docs/superpowers/specs/2026-05-17-per-workspace-mcp-config-design.md §7
// Backend endpoints in server/internal/api/workspace_mcp.go.
//
// niuniu is always implicitly enabled — we render it as a disabled-checked
// row with a "必选" hint to communicate that it can't be turned off.

interface WorkspaceMCPPanelProps {
  workspaceId: number;
}

export function WorkspaceMCPPanel({ workspaceId }: WorkspaceMCPPanelProps) {
  const { t } = useTranslation('workspaces');
  const [state, setState] = useState<WorkspaceMCPState | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [initialSelected, setInitialSelected] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [redetecting, setRedetecting] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [savedAt, setSavedAt] = useState<number | null>(null);
  const [strict, setStrict] = useState(false);

  const loadState = useCallback(async () => {
    setLoading(true);
    try {
      const s = await mcpApi.get(workspaceId);
      setState(s);
      const initial = s.servers ?? [];
      setSelected(initial);
      setInitialSelected(initial);
      setStrict(s.strict ?? false);
    } finally {
      setLoading(false);
    }
  }, [workspaceId]);

  useEffect(() => {
    void loadState();
  }, [loadState]);

  const dirty =
    selected.length !== initialSelected.length ||
    selected.some((n) => !initialSelected.includes(n)) ||
    initialSelected.some((n) => !selected.includes(n));

  const toggle = (name: string) => {
    setSelected((prev) =>
      prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]
    );
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await mcpApi.put(workspaceId, selected);
      await loadState();
      setSavedAt(Date.now());
    } finally {
      setSaving(false);
    }
  };

  const handleRedetect = async () => {
    setRedetecting(true);
    try {
      const result = await mcpApi.redetect(workspaceId);
      // Merge `recommended` into the local selection without dropping the
      // user's existing choices — re-detect should expand, not clobber.
      setSelected((prev) => {
        const set = new Set(prev);
        for (const name of result.recommended ?? []) set.add(name);
        return Array.from(set);
      });
      await loadState();
    } finally {
      setRedetecting(false);
    }
  };

  const handleRestartAgent = async () => {
    setRestarting(true);
    try {
      // No standalone "restart" endpoint exists; stopping the session ends the
      // current agent process so the next user message respawns with the new
      // .mcp.json. This is the closest available action that maps to the
      // user's intent ("apply the changes I just saved").
      await api.stopAgent(String(workspaceId));
    } catch {
      // Errors surface via the global toast in apiFetch.
    } finally {
      setRestarting(false);
    }
  };

  if (loading && !state) {
    return (
      <div className="flex items-center gap-2 text-xs text-muted-foreground py-2">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        {t('mcp.loading')}
      </div>
    );
  }

  if (!state) {
    return null;
  }

  const unavailable = state.unavailable ?? [];
  const conflicts = state.plugin_conflicts ?? [];
  const available = state.available ?? [];

  return (
    <div className="space-y-4" data-testid="workspace-mcp-panel">
      {/* Panel heading */}
      <Label className="text-sm font-semibold flex items-center gap-1.5">
        <Plug className="h-3.5 w-3.5 text-muted-foreground" />
        {t('mcp.section_title')}
      </Label>

      {/* ── Section 2: 本工作区覆盖 (Workspace overrides) ── */}
      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
            {t('mcp.overrides')}
          </p>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={handleRedetect}
            disabled={redetecting || saving}
            className="text-xs"
          >
            {redetecting ? (
              <Loader2 className="h-3 w-3 animate-spin mr-1" />
            ) : (
              <RefreshCw className="h-3 w-3 mr-1" />
            )}
            {t('mcp.redetect')}
          </Button>
        </div>

        {unavailable.length > 0 && (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertDescription className="text-xs">
              {t('mcp.unavailable_warning', { count: unavailable.length })}
              <span className="ml-1 font-mono text-[11px]">
                {unavailable.join(', ')}
              </span>
            </AlertDescription>
          </Alert>
        )}

        {conflicts.length > 0 && (
          <div
            role="alert"
            className="flex items-center gap-2 w-full rounded-lg border bg-background px-3 py-2 text-foreground"
          >
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
            <span className="text-xs leading-snug">
              {t('mcp.plugin_conflict.global_load')}
            </span>
          </div>
        )}

        <div className="border rounded-md divide-y">
          {/* niuniu is always implicitly enabled — disabled-checked row */}
          <label
            className="flex items-center gap-2 p-2 cursor-not-allowed opacity-90"
            data-testid="mcp-row-niuniu"
          >
            <Checkbox checked disabled />
            <span className="flex-1 text-sm">niuniu</span>
            <span className="shrink-0 rounded-sm bg-brand/10 px-1.5 py-0.5 text-[10px] font-medium text-brand">
              {t('mcp.niuniu_required')}
            </span>
          </label>

          {available.length === 0 ? (
            <p className="text-xs text-muted-foreground italic p-2">
              {t('mcp.no_servers_available')}
            </p>
          ) : (
            available.map((m) => {
              const checked = selected.includes(m.name);
              const isPlugin = m.source === 'plugin';
              return (
                <label
                  key={m.name}
                  className="flex items-center gap-2 p-2 cursor-pointer hover:bg-accent/40"
                  data-testid={`mcp-row-${m.name}`}
                >
                  <Checkbox
                    checked={checked}
                    onCheckedChange={() => toggle(m.name)}
                    disabled={saving}
                  />
                  <span className="flex-1 min-w-0 text-sm truncate" title={m.name}>
                    {m.name}
                  </span>
                  {isPlugin && (
                    <span
                      className="shrink-0 rounded-sm bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground"
                      title={m.plugin_name}
                    >
                      plugin
                    </span>
                  )}
                </label>
              );
            })
          )}
        </div>

        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="sm"
            onClick={handleSave}
            disabled={!dirty || saving}
            data-testid="mcp-save"
          >
            {saving && <Loader2 className="h-3 w-3 animate-spin mr-1" />}
            {t('mcp.save')}
          </Button>
          {savedAt !== null && (
            <span className="text-xs text-muted-foreground flex items-center gap-2">
              {t('mcp.restart_required')}
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={handleRestartAgent}
                disabled={restarting}
              >
                {restarting ? (
                  <Loader2 className="h-3 w-3 animate-spin mr-1" />
                ) : (
                  <Power className="h-3 w-3 mr-1" />
                )}
                {t('mcp.restart_agent')}
              </Button>
            </span>
          )}
        </div>
      </div>

      {/* ── Section 3: strict 开关 ── */}
      <div className="flex items-center justify-between rounded border border-warm-border px-3 py-2">
        <div className="flex flex-col">
          <Label htmlFor="ws-strict-mcp" className="text-sm">{t('mcp.strict_label')}</Label>
          <span className="text-xs text-muted-foreground">{t('mcp.strict_hint')}</span>
        </div>
        <Switch
          id="ws-strict-mcp"
          checked={strict}
          onCheckedChange={async (next) => {
            const prev = strict;
            setStrict(next);
            try {
              await mcpApi.setStrict(workspaceId, next);
            } catch {
              setStrict(prev);
              toast.error(t('mcp.strict_failed'));
            }
          }}
        />
      </div>
    </div>
  );
}
