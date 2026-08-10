import type {
  SceneDefinition,
  SceneQuickActionAsset,
  SceneAgentRefAsset,
  SceneMCPDecl,
  ScenePluginDecl,
  SceneSkillDecl,
  ScenePrompt,
  SceneRequiredCredential,
  SceneMatch,
  SceneMatchRule,
  SceneEnvPresetAsset,
  SceneAssets,
  SceneDataSourceRef,
} from '@/types/api';
import type { OwnerRef } from '@/types/org';

/** Structured row model for the quick-action editor. */
export interface QuickActionRow {
  slug: string;
  label: string;
  prompt: string;
}

/** Row model for one MCP server. `configRaw` is the inline ".mcp.json" server
 *  entry as JSON text; '' = reference a locally-installed server by name. */
export interface MCPRow {
  name: string;
  configRaw: string;
}

/** Row model for one match rule. weight/args stay raw strings while editing. */
export interface MatchRuleRow {
  signal: string;
  weight: string;
  /** JSON object for the rule's args; '' = no args. */
  argsRaw: string;
}

/** One `KEY=value` row inside an env preset. */
export interface EnvVarRow {
  key: string;
  value: string;
}

/** Row model for an env preset (`env_presets` asset). */
export interface EnvPresetRow {
  slug: string;
  name: string;
  env: EnvVarRow[];
}

/** Row model for a required data source (数据源), referenced by per-owner name. */
export interface DataSourceRow {
  name: string;
  kind: string;
  purpose: string;
  optional: boolean;
}

/**
 * Structured draft backing the "advanced definition" editor. Replaces the old
 * raw-JSON textarea: each field maps to a dedicated UI section. The only
 * free-form bit left is match-rule args (an arbitrary per-signal payload), kept
 * as a small JSON box. `project_templates` / `harness_specs` are intentionally
 * NOT part of a scene — they are managed on their own pages — so they are never
 * surfaced or emitted here.
 */
export interface AdvancedDraft {
  mcp: MCPRow[];
  plugins: ScenePluginDecl[];
  prompts: ScenePrompt[];
  /** Required data sources (数据源) — the scene's "credential" section now binds
   *  to configured data sources, mirroring a project's external sources. */
  dataSources: DataSourceRow[];
  matchBaseWeight: string;
  matchRules: MatchRuleRow[];
  envPresets: EnvPresetRow[];
  /** Unknown leftover asset keys preserved verbatim (not surfaced in the UI). */
  assetsPassthrough: Record<string, unknown>;
  /** Legacy provider-credential requirements, preserved verbatim (no longer
   *  edited in the UI — the section now configures data sources). */
  requiredCredentialsPassthrough: SceneRequiredCredential[];
  /** Vendored skill refs, preserved verbatim (not surfaced in the structured
   *  UI yet) so a fork-edit never drops a scene's projected skills. */
  skillsPassthrough: SceneSkillDecl[];
  /** Built-in MCP tool groups hidden from the agent in this scene
   *  (e.g. 'multi-agent', 'harness'). Edited via checkboxes. */
  disableToolGroups: string[];
}

export interface SceneEditorSubmit {
  slug: string;
  displayName: string;
  description: string;
  tags: string[];
  definition: SceneDefinition;
  owner?: OwnerRef;
}

export function emptyAdvancedDraft(): AdvancedDraft {
  return {
    mcp: [],
    plugins: [],
    prompts: [],
    dataSources: [],
    matchBaseWeight: '',
    matchRules: [],
    envPresets: [],
    assetsPassthrough: {},
    requiredCredentialsPassthrough: [],
    skillsPassthrough: [],
    disableToolGroups: [],
  };
}

/**
 * Split a SceneDefinition into the structured advanced draft. quick_actions and
 * agents are edited by their own structural editors; env_presets gets its own
 * structured section here. project_templates / harness_specs are not a scene
 * concern, so they are dropped. Any other unknown asset keys are preserved in
 * assetsPassthrough so editing never loses data.
 */
export function advancedDraftFromDefinition(def: SceneDefinition): AdvancedDraft {
  const assets = { ...(def.assets ?? {}) } as Record<string, unknown>;
  delete assets.quick_actions;
  delete assets.agents;
  delete assets.project_templates;
  delete assets.harness_specs;
  const envPresetsRaw = (assets.env_presets as SceneEnvPresetAsset[] | undefined) ?? [];
  delete assets.env_presets;

  return {
    mcp: (def.mcp ?? []).map((m) => ({
      name: m.name,
      configRaw: m.config && Object.keys(m.config).length > 0 ? JSON.stringify(m.config, null, 2) : '',
    })),
    plugins: (def.plugins ?? []).map((p) => ({ source: p.source, ref: p.ref ?? '', optional: !!p.optional })),
    prompts: (def.prompts ?? []).map((p) => ({ id: p.id, title: p.title, body: p.body })),
    dataSources: (def.required_data_sources ?? []).map((d) => ({
      name: d.name,
      kind: d.kind ?? '',
      purpose: d.purpose ?? '',
      optional: !!d.optional,
    })),
    requiredCredentialsPassthrough: def.required_credentials ?? [],
    skillsPassthrough: def.skills ?? [],
    matchBaseWeight: def.match?.base_weight != null ? String(def.match.base_weight) : '',
    matchRules: (def.match?.rules ?? []).map((r) => ({
      signal: r.signal,
      weight: String(r.weight),
      argsRaw: r.args && Object.keys(r.args).length > 0 ? JSON.stringify(r.args, null, 2) : '',
    })),
    envPresets: envPresetsRaw.map((p) => ({
      slug: p.slug,
      name: p.name ?? '',
      env: Object.entries(p.env ?? {}).map(([key, value]) => ({ key, value: String(value) })),
    })),
    assetsPassthrough: assets,
    disableToolGroups: def.disable_tool_groups ?? [],
  };
}

/** Parse a JSON object string; throw a labelled error if it isn't a plain object. */
function parseJsonObject(text: string, label: string): Record<string, unknown> {
  const parsed = JSON.parse(text) as unknown;
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object`);
  }
  return parsed as Record<string, unknown>;
}

/**
 * Assemble a SceneDefinition from the structured advanced draft plus the
 * quick-action rows and selected agent names. The structured editors are
 * authoritative for quick_actions/agents: any stray copies in the leftover-assets
 * JSON are stripped so they cannot shadow the form. Throws (with a human-readable
 * message) if the leftover-assets JSON, a match-rule's args JSON, or a numeric
 * field is invalid.
 */
export function assembleSceneDefinition(
  draft: AdvancedDraft,
  quickActions: QuickActionRow[],
  agentNames: string[],
): SceneDefinition {
  // Unknown leftover asset keys are preserved verbatim; the structured editors
  // are authoritative for everything they own, and project_templates /
  // harness_specs are never a scene concern.
  const advancedAssets = { ...draft.assetsPassthrough };
  delete advancedAssets.quick_actions;
  delete advancedAssets.agents;
  delete advancedAssets.env_presets;
  delete advancedAssets.project_templates;
  delete advancedAssets.harness_specs;

  const mcp: SceneMCPDecl[] = draft.mcp
    .filter((m) => m.name.trim())
    .map((m) => {
      const name = m.name.trim();
      const decl: SceneMCPDecl = { name };
      const cfgText = m.configRaw.trim();
      if (cfgText) decl.config = parseJsonObject(cfgText, `MCP "${name}" config`);
      return decl;
    });

  const plugins: ScenePluginDecl[] = draft.plugins
    .filter((p) => p.source.trim())
    .map((p) => {
      const decl: ScenePluginDecl = { source: p.source.trim() };
      if (p.ref?.trim()) decl.ref = p.ref.trim();
      if (p.optional) decl.optional = true;
      return decl;
    });

  const prompts: ScenePrompt[] = draft.prompts
    .filter((p) => p.id.trim())
    .map((p) => ({ id: p.id.trim(), title: p.title.trim(), body: p.body }));

  const required_data_sources: SceneDataSourceRef[] = draft.dataSources
    .filter((d) => d.name.trim())
    .map((d) => {
      const ref: SceneDataSourceRef = { name: d.name.trim() };
      if (d.kind.trim()) ref.kind = d.kind.trim();
      if (d.purpose.trim()) ref.purpose = d.purpose.trim();
      if (d.optional) ref.optional = true;
      return ref;
    });

  const match: SceneMatch = {
    rules: draft.matchRules
      .filter((r) => r.signal.trim())
      .map((r) => {
        const weight = Number(r.weight);
        if (r.weight.trim() === '' || Number.isNaN(weight)) {
          throw new Error(`match rule "${r.signal.trim()}" weight must be a number`);
        }
        const rule: SceneMatchRule = { signal: r.signal.trim(), weight };
        const argsText = r.argsRaw.trim();
        if (argsText) rule.args = parseJsonObject(argsText, `match rule "${r.signal.trim()}" args`);
        return rule;
      }),
  };
  const baseWeightText = draft.matchBaseWeight.trim();
  if (baseWeightText) {
    const baseWeight = Number(baseWeightText);
    if (Number.isNaN(baseWeight)) throw new Error('match base weight must be a number');
    match.base_weight = baseWeight;
  }

  const envPresets: SceneEnvPresetAsset[] = draft.envPresets
    .filter((p) => p.slug.trim())
    .map((p) => {
      const env: Record<string, string> = {};
      for (const row of p.env) {
        const key = row.key.trim();
        if (key) env[key] = row.value;
      }
      const preset: SceneEnvPresetAsset = { slug: p.slug.trim(), env };
      if (p.name.trim()) preset.name = p.name.trim();
      return preset;
    });

  const quick_actions: SceneQuickActionAsset[] = quickActions
    .filter((q) => q.slug.trim())
    .map((q) => ({ slug: q.slug.trim(), label: q.label.trim(), prompt: q.prompt }));
  // Dedup + drop blanks; references are by the agent's unique name.
  const agents: SceneAgentRefAsset[] = Array.from(
    new Set(agentNames.map((n) => n.trim()).filter(Boolean)),
  ).map((name) => ({ name }));

  const assets: SceneAssets = { ...advancedAssets, quick_actions, agents };
  if (envPresets.length > 0) assets.env_presets = envPresets;

  // Hidden built-in tool groups: dedup + drop blanks. Omit the key entirely when
  // none are hidden so the definition stays clean.
  const disableToolGroups = Array.from(
    new Set(draft.disableToolGroups.map((g) => g.trim()).filter(Boolean)),
  );

  return {
    mcp,
    plugins,
    ...(draft.skillsPassthrough.length > 0 ? { skills: draft.skillsPassthrough } : {}),
    prompts,
    // Legacy provider-credential requirements are preserved verbatim; the UI now
    // configures data sources instead.
    required_credentials: draft.requiredCredentialsPassthrough,
    required_data_sources,
    ...(disableToolGroups.length > 0 ? { disable_tool_groups: disableToolGroups } : {}),
    match,
    assets,
  };
}
