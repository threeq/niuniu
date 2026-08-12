import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Plus, Trash2, ExternalLink } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Checkbox } from '@/components/ui/checkbox';
import { Badge } from '@/components/ui/badge';
import { api } from '@/lib/api';
import { listDataSources } from '@/lib/data-sources-api';
import type { KnownMCP } from '@/types/api';
import type { AdvancedDraft, DataSourceRow, EnvPresetRow, MatchRuleRow, MCPRow } from './scene-editor-helpers';

interface SceneAdvancedEditorProps {
  value: AdvancedDraft;
  onChange: (next: AdvancedDraft) => void;
  disabled?: boolean;
}

// Built-in niuniu MCP tool groups a scene can hide from the agent. The ids are a
// stable contract shared with cmd/niuniu-mcp (toolGroup* constants) and the
// scene YAML's disable_tool_groups. Unknown groups stored on a scene are
// preserved verbatim (the checkboxes only toggle the ones listed here).
const TOOL_GROUPS: { id: string; labelKey: string; hintKey: string }[] = [
  {
    id: 'multi-agent',
    labelKey: 'editor.adv_toolgroups_multi_agent',
    hintKey: 'editor.adv_toolgroups_multi_agent_hint',
  },
  { id: 'harness', labelKey: 'editor.adv_toolgroups_harness', hintKey: 'editor.adv_toolgroups_harness_hint' },
];

/** Shared section shell: bordered card with a title and an "add row" button. */
function Section({
  title,
  hint,
  onAdd,
  addLabel,
  disabled,
  children,
}: {
  title: string;
  hint?: string;
  onAdd?: () => void;
  addLabel?: string;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-2 rounded-lg border border-warm-border p-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-warm-text">{title}</h3>
        {onAdd && (
          <Button type="button" variant="outline" size="sm" onClick={onAdd} disabled={disabled}>
            <Plus className="h-4 w-4 mr-1" /> {addLabel}
          </Button>
        )}
      </div>
      {hint && <p className="text-xs text-warm-text-muted">{hint}</p>}
      {children}
    </section>
  );
}

function RemoveButton({
  onClick,
  disabled,
  label,
}: {
  onClick: () => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      onClick={onClick}
      disabled={disabled}
      className="text-destructive hover:text-destructive/80 hover:bg-destructive/10 shrink-0"
      aria-label={label}
    >
      <Trash2 className="h-4 w-4" />
    </Button>
  );
}

export function SceneAdvancedEditor({ value, onChange, disabled }: SceneAdvancedEditorProps) {
  const { t } = useTranslation('scenes');
  const patch = (p: Partial<AdvancedDraft>) => onChange({ ...value, ...p });

  // Locally-installed MCP servers (from the owner's active Claude account, with a
  // fall back to the first account). Used to populate the picker; the scene still
  // only stores the MCP name, so a free-typed name for a not-yet-installed server
  // is also accepted.
  const { data: mcpAvailable = [] } = useQuery({
    queryKey: ['scene-mcp-available'],
    queryFn: async (): Promise<KnownMCP[]> => {
      // Per-account MCP discovery was removed with the multi-account system;
      // the picker now relies on free-typed names (the scene only stores the
      // MCP name anyway, so a not-yet-installed server is accepted).
      return [];
    },
  });
  const mcpByName = new Map(mcpAvailable.map((m) => [m.name, m]));

  // The owner's configured data sources (数据源, managed under Settings /
  // Integrations). The scene's "credential" section binds to these by name,
  // mirroring how a project's external sources bind to a configured credential.
  const { data: dataSources = [] } = useQuery({
    queryKey: ['data-sources'],
    queryFn: () => listDataSources(),
  });
  const dsByName = new Map(dataSources.map((d) => [d.name, d]));

  // -- providers (subscription platforms mounted by name, expanded per agent) --
  const { data: providers = [] } = useQuery({
    queryKey: ['env-providers'],
    queryFn: () => api.listEnvProviders(),
  });
  const toggleProvider = (name: string) => {
    const has = value.providers.includes(name);
    patch({
      providers: has ? value.providers.filter((n) => n !== name) : [...value.providers, name],
    });
  };

  // -- mcp ------------------------------------------------------------------
  const addMcp = () => patch({ mcp: [...value.mcp, { name: '', configRaw: '' }] });
  const setMcp = (i: number, p: Partial<MCPRow>) =>
    patch({ mcp: value.mcp.map((m, idx) => (idx === i ? { ...m, ...p } : m)) });
  const removeMcp = (i: number) => patch({ mcp: value.mcp.filter((_, idx) => idx !== i) });

  // -- plugins --------------------------------------------------------------
  const addPlugin = () => patch({ plugins: [...value.plugins, { source: '', ref: '', optional: false }] });
  const setPlugin = (i: number, p: Partial<(typeof value.plugins)[number]>) =>
    patch({ plugins: value.plugins.map((row, idx) => (idx === i ? { ...row, ...p } : row)) });
  const removePlugin = (i: number) => patch({ plugins: value.plugins.filter((_, idx) => idx !== i) });

  // -- prompts --------------------------------------------------------------
  const addPrompt = () => patch({ prompts: [...value.prompts, { id: '', title: '', body: '' }] });
  const setPrompt = (i: number, p: Partial<(typeof value.prompts)[number]>) =>
    patch({ prompts: value.prompts.map((row, idx) => (idx === i ? { ...row, ...p } : row)) });
  const removePrompt = (i: number) => patch({ prompts: value.prompts.filter((_, idx) => idx !== i) });

  // -- required data sources ------------------------------------------------
  const addDS = () =>
    patch({ dataSources: [...value.dataSources, { name: '', kind: '', purpose: '', optional: false }] });
  const setDS = (i: number, p: Partial<DataSourceRow>) =>
    patch({ dataSources: value.dataSources.map((row, idx) => (idx === i ? { ...row, ...p } : row)) });
  const removeDS = (i: number) =>
    patch({ dataSources: value.dataSources.filter((_, idx) => idx !== i) });

  // -- disabled tool groups -------------------------------------------------
  const toggleToolGroup = (id: string, on: boolean) =>
    patch({
      disableToolGroups: on
        ? Array.from(new Set([...value.disableToolGroups, id]))
        : value.disableToolGroups.filter((g) => g !== id),
    });

  // -- match rules ----------------------------------------------------------
  const addRule = () =>
    patch({ matchRules: [...value.matchRules, { signal: '', weight: '', argsRaw: '' }] });
  const setRule = (i: number, p: Partial<MatchRuleRow>) =>
    patch({ matchRules: value.matchRules.map((row, idx) => (idx === i ? { ...row, ...p } : row)) });
  const removeRule = (i: number) => patch({ matchRules: value.matchRules.filter((_, idx) => idx !== i) });

  // -- env presets ----------------------------------------------------------
  const addPreset = () => patch({ envPresets: [...value.envPresets, { slug: '', name: '', env: [] }] });
  const setPreset = (i: number, p: Partial<EnvPresetRow>) =>
    patch({ envPresets: value.envPresets.map((row, idx) => (idx === i ? { ...row, ...p } : row)) });
  const removePreset = (i: number) => patch({ envPresets: value.envPresets.filter((_, idx) => idx !== i) });
  const addVar = (pi: number) =>
    setPreset(pi, { env: [...value.envPresets[pi].env, { key: '', value: '' }] });
  const setVar = (pi: number, vi: number, p: Partial<{ key: string; value: string }>) =>
    setPreset(pi, { env: value.envPresets[pi].env.map((v, idx) => (idx === vi ? { ...v, ...p } : v)) });
  const removeVar = (pi: number, vi: number) =>
    setPreset(pi, { env: value.envPresets[pi].env.filter((_, idx) => idx !== vi) });

  return (
    <div className="space-y-4">
      {/* MCP servers */}
      <Section
        title={t('editor.adv_mcp_title')}
        hint={t('editor.adv_mcp_hint')}
        onAdd={addMcp}
        addLabel={t('editor.adv_mcp_add')}
        disabled={disabled}
      >
        <datalist id="scene-mcp-available">
          {mcpAvailable.map((m) => (
            <option key={m.name} value={m.name}>
              {m.source === 'plugin' ? t('editor.adv_mcp_src_plugin') : t('editor.adv_mcp_src_global')}
            </option>
          ))}
        </datalist>
        {value.mcp.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.adv_mcp_empty')}</p>
        ) : (
          <div className="space-y-2">
            {value.mcp.map((row, i) => {
              const name = row.name.trim();
              const known = mcpByName.get(name);
              const hasConfig = row.configRaw.trim().length > 0;
              // Show the inline-config box for a custom name (not installed) or
              // whenever a config has already been pasted.
              const showConfig = (name !== '' && !known) || hasConfig;
              return (
                <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                  <div className="flex items-center gap-2">
                    <Input
                      value={row.name}
                      onChange={(e) => setMcp(i, { name: e.target.value })}
                      placeholder={t('editor.adv_mcp_name')}
                      disabled={disabled}
                      list="scene-mcp-available"
                      className="font-mono text-xs"
                    />
                    {name &&
                      (hasConfig ? (
                        <Badge variant="secondary" className="shrink-0 text-xs">
                          {t('editor.adv_mcp_custom')}
                        </Badge>
                      ) : known ? (
                        <Badge variant="secondary" className="shrink-0 text-xs">
                          {known.source === 'plugin' ? t('editor.adv_mcp_src_plugin') : t('editor.adv_mcp_src_global')}
                        </Badge>
                      ) : (
                        <Badge variant="outline" className="shrink-0 text-xs text-warm-text-muted">
                          {t('editor.adv_mcp_new')}
                        </Badge>
                      ))}
                    <RemoveButton onClick={() => removeMcp(i)} disabled={disabled} label={t('editor.remove')} />
                  </div>
                  {showConfig && (
                    <Textarea
                      value={row.configRaw}
                      onChange={(e) => setMcp(i, { configRaw: e.target.value })}
                      placeholder={t('editor.adv_mcp_config_ph')}
                      disabled={disabled}
                      rows={5}
                      className="font-mono text-xs"
                    />
                  )}
                </div>
              );
            })}
          </div>
        )}
        <p className="text-xs text-warm-text-muted">{t('editor.adv_mcp_new_hint')}</p>
      </Section>

      {/* Plugins */}
      <Section
        title={t('editor.adv_plugins_title')}
        hint={t('editor.adv_plugins_hint')}
        onAdd={addPlugin}
        addLabel={t('editor.adv_plugins_add')}
        disabled={disabled}
      >
        {value.plugins.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.adv_plugins_empty')}</p>
        ) : (
          <div className="space-y-3">
            {value.plugins.map((row, i) => (
              <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                <div className="flex gap-2">
                  <Input
                    value={row.source}
                    onChange={(e) => setPlugin(i, { source: e.target.value })}
                    placeholder={t('editor.adv_plugins_source')}
                    disabled={disabled}
                    className="font-mono text-xs"
                  />
                  <RemoveButton onClick={() => removePlugin(i)} disabled={disabled} label={t('editor.remove')} />
                </div>
                <div className="flex items-center gap-3">
                  <Input
                    value={row.ref ?? ''}
                    onChange={(e) => setPlugin(i, { ref: e.target.value })}
                    placeholder={t('editor.adv_plugins_ref')}
                    disabled={disabled}
                    className="font-mono text-xs"
                  />
                  <label className="flex items-center gap-2 text-xs text-warm-text shrink-0 cursor-pointer">
                    <Checkbox
                      checked={!!row.optional}
                      onCheckedChange={(c) => setPlugin(i, { optional: c === true })}
                      disabled={disabled}
                    />
                    {t('editor.adv_optional')}
                  </label>
                </div>
              </div>
            ))}
          </div>
        )}
      </Section>

      {/* Hidden built-in tool groups */}
      <Section title={t('editor.adv_toolgroups_title')} hint={t('editor.adv_toolgroups_hint')}>
        <div className="space-y-2">
          {TOOL_GROUPS.map((g) => (
            <label key={g.id} className="flex items-start gap-2 text-xs text-warm-text cursor-pointer">
              <Checkbox
                checked={value.disableToolGroups.includes(g.id)}
                onCheckedChange={(c) => toggleToolGroup(g.id, c === true)}
                disabled={disabled}
                className="mt-0.5"
              />
              <span>
                <span className="font-medium">{t(g.labelKey)}</span>
                <span className="block text-warm-text-muted">{t(g.hintKey)}</span>
              </span>
            </label>
          ))}
        </div>
      </Section>

      {/* CLAUDE.md prompt fragments */}
      <Section
        title={t('editor.adv_prompts_title')}
        hint={t('editor.adv_prompts_hint')}
        onAdd={addPrompt}
        addLabel={t('editor.adv_prompts_add')}
        disabled={disabled}
      >
        {value.prompts.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.adv_prompts_empty')}</p>
        ) : (
          <div className="space-y-3">
            {value.prompts.map((row, i) => (
              <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                <div className="flex gap-2">
                  <Input
                    value={row.id}
                    onChange={(e) => setPrompt(i, { id: e.target.value })}
                    placeholder={t('editor.adv_prompts_id')}
                    disabled={disabled}
                    className="font-mono text-xs"
                  />
                  <Input
                    value={row.title}
                    onChange={(e) => setPrompt(i, { title: e.target.value })}
                    placeholder={t('editor.adv_prompts_pt_title')}
                    disabled={disabled}
                  />
                  <RemoveButton onClick={() => removePrompt(i)} disabled={disabled} label={t('editor.remove')} />
                </div>
                <Textarea
                  value={row.body}
                  onChange={(e) => setPrompt(i, { body: e.target.value })}
                  placeholder={t('editor.adv_prompts_body')}
                  disabled={disabled}
                  rows={3}
                  className="text-xs"
                />
              </div>
            ))}
          </div>
        )}
      </Section>

      {/* Required data sources (数据源) — bind to the owner's configured data
          sources, mirroring a project's external sources. */}
      <Section
        title={t('editor.adv_ds_title')}
        hint={t('editor.adv_ds_hint')}
        onAdd={addDS}
        addLabel={t('editor.adv_ds_add')}
        disabled={disabled}
      >
        {value.dataSources.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.adv_ds_empty')}</p>
        ) : (
          <div className="space-y-3">
            {value.dataSources.map((row, i) => {
              const found = row.name ? dsByName.get(row.name) : undefined;
              // Preserve a stored name that the owner no longer has configured.
              const unknownName = row.name && !found;
              return (
                <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                  <div className="flex gap-2">
                    <select
                      value={row.name}
                      onChange={(e) => {
                        const next = dsByName.get(e.target.value);
                        setDS(i, { name: e.target.value, kind: next?.kind ?? row.kind });
                      }}
                      disabled={disabled}
                      className="h-9 flex-1 rounded-md border border-input bg-background px-3 py-1 text-sm disabled:opacity-50"
                    >
                      <option value="" disabled>
                        {t('editor.adv_ds_select')}
                      </option>
                      {dataSources.map((d) => (
                        <option key={d.id} value={d.name}>
                          {d.name} ({d.kind})
                        </option>
                      ))}
                      {unknownName && <option value={row.name}>{row.name}</option>}
                    </select>
                    {found ? (
                      <Badge variant="secondary" className="shrink-0 text-xs">
                        {found.kind}
                      </Badge>
                    ) : null}
                    <RemoveButton onClick={() => removeDS(i)} disabled={disabled} label={t('editor.remove')} />
                  </div>
                  <div className="flex items-center gap-3">
                    <Input
                      value={row.purpose}
                      onChange={(e) => setDS(i, { purpose: e.target.value })}
                      placeholder={t('editor.adv_ds_purpose')}
                      disabled={disabled}
                    />
                    <label className="flex items-center gap-2 text-xs text-warm-text shrink-0 cursor-pointer">
                      <Checkbox
                        checked={row.optional}
                        onCheckedChange={(c) => setDS(i, { optional: c === true })}
                        disabled={disabled}
                      />
                      {t('editor.adv_optional')}
                    </label>
                  </div>
                  {unknownName && (
                    <p className="text-xs text-warning">
                      {t('editor.adv_ds_missing', { name: row.name })}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
        )}
        <Button asChild variant="outline" size="sm">
          <Link to="/settings/integrations">
            <ExternalLink className="h-4 w-4 mr-1" aria-hidden /> {t('editor.adv_ds_manage')}
          </Link>
        </Button>
      </Section>

      {/* Match rules */}
      <Section
        title={t('editor.adv_match_title')}
        hint={t('editor.adv_match_hint')}
        onAdd={addRule}
        addLabel={t('editor.adv_match_add_rule')}
        disabled={disabled}
      >
        <div className="flex items-center gap-2">
          <label htmlFor="scene-match-base-weight" className="text-xs text-warm-text-muted">
            {t('editor.adv_match_base_weight')}
          </label>
          <Input
            id="scene-match-base-weight"
            type="number"
            value={value.matchBaseWeight}
            onChange={(e) => patch({ matchBaseWeight: e.target.value })}
            disabled={disabled}
            className="w-28 text-xs"
          />
        </div>
        {value.matchRules.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.adv_match_empty')}</p>
        ) : (
          <div className="space-y-3">
            {value.matchRules.map((row, i) => (
              <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                <div className="flex gap-2">
                  <Input
                    value={row.signal}
                    onChange={(e) => setRule(i, { signal: e.target.value })}
                    placeholder={t('editor.adv_match_signal')}
                    disabled={disabled}
                    className="font-mono text-xs"
                  />
                  <Input
                    type="number"
                    value={row.weight}
                    onChange={(e) => setRule(i, { weight: e.target.value })}
                    placeholder={t('editor.adv_match_weight')}
                    disabled={disabled}
                    className="w-28 text-xs"
                  />
                  <RemoveButton onClick={() => removeRule(i)} disabled={disabled} label={t('editor.remove')} />
                </div>
                <Textarea
                  value={row.argsRaw}
                  onChange={(e) => setRule(i, { argsRaw: e.target.value })}
                  placeholder={t('editor.adv_match_args')}
                  disabled={disabled}
                  rows={2}
                  className="font-mono text-xs"
                />
              </div>
            ))}
          </div>
        )}
      </Section>

      {/* Env presets */}
      <Section
        title={t('editor.adv_env_title')}
        hint={t('editor.adv_env_hint')}
        onAdd={addPreset}
        addLabel={t('editor.adv_env_add')}
        disabled={disabled}
      >
        {/* Subscription-platform providers mounted by name (expanded per agent at spawn) */}
        <div className="space-y-2">
          <p className="text-xs text-warm-text-muted">{t('editor.adv_env_providers_hint')}</p>
          {providers.length === 0 ? (
            <p className="text-xs text-warm-text-muted">{t('editor.adv_env_providers_empty')}</p>
          ) : (
            <div className="space-y-1.5">
              {providers.map((p) => (
                <label key={p.id} className="flex items-center gap-2 text-sm text-warm-text">
                  <Checkbox
                    checked={value.providers.includes(p.name)}
                    onCheckedChange={() => toggleProvider(p.name)}
                    disabled={disabled}
                  />
                  <span>{p.name}</span>
                  {p.protocol && <Badge variant="outline" className="text-xs">{p.protocol}</Badge>}
                </label>
              ))}
            </div>
          )}
        </div>

        {value.envPresets.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.adv_env_empty')}</p>
        ) : (
          <div className="space-y-3">
            {value.envPresets.map((preset, pi) => (
              <div key={pi} className="rounded-md border border-warm-border p-3 space-y-2">
                <div className="flex gap-2">
                  <Input
                    value={preset.slug}
                    onChange={(e) => setPreset(pi, { slug: e.target.value })}
                    placeholder={t('editor.adv_env_slug')}
                    disabled={disabled}
                    className="font-mono text-xs"
                  />
                  <Input
                    value={preset.name}
                    onChange={(e) => setPreset(pi, { name: e.target.value })}
                    placeholder={t('editor.adv_env_name')}
                    disabled={disabled}
                  />
                  <RemoveButton onClick={() => removePreset(pi)} disabled={disabled} label={t('editor.remove')} />
                </div>
                {preset.env.length === 0 ? (
                  <p className="text-xs text-warm-text-muted">{t('editor.adv_env_vars_empty')}</p>
                ) : (
                  <div className="space-y-2">
                    {preset.env.map((v, vi) => (
                      <div key={vi} className="flex gap-2">
                        <Input
                          value={v.key}
                          onChange={(e) => setVar(pi, vi, { key: e.target.value })}
                          placeholder={t('editor.adv_env_key')}
                          disabled={disabled}
                          className="font-mono text-xs"
                        />
                        <Input
                          value={v.value}
                          onChange={(e) => setVar(pi, vi, { value: e.target.value })}
                          placeholder={t('editor.adv_env_value')}
                          disabled={disabled}
                          className="font-mono text-xs"
                        />
                        <RemoveButton onClick={() => removeVar(pi, vi)} disabled={disabled} label={t('editor.remove')} />
                      </div>
                    ))}
                  </div>
                )}
                <Button type="button" variant="outline" size="sm" onClick={() => addVar(pi)} disabled={disabled}>
                  <Plus className="h-4 w-4 mr-1" /> {t('editor.adv_env_add_var')}
                </Button>
              </div>
            ))}
          </div>
        )}
      </Section>
    </div>
  );
}
