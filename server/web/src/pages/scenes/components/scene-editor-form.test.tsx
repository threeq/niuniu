import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { useAuthStore } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import {
  assembleSceneDefinition,
  advancedDraftFromDefinition,
  emptyAdvancedDraft,
  type AdvancedDraft,
  type QuickActionRow,
} from './scene-editor-helpers';
import { SceneEditorForm } from './scene-editor-form';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

vi.mock('@/lib/team-api', () => ({
  agentFileApi: {
    list: vi.fn().mockResolvedValue([]),
  },
}));

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

beforeEach(() => {
  localStorage.clear();
  useAuthStore.setState({
    user: { id: 2, username: 'alice', display_name: 'Alice', role: 'member' },
    isAuthenticated: true,
  });
  useOrgStore.setState({
    myOrgs: [{ id: 9, slug: 'team', name: 'Team', description: '', role: 'admin', created_at: '', updated_at: '' }],
    loaded: true,
  });
});

describe('SceneEditorForm owner selection', () => {
  it('submits the selected org owner when creating a scene', async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();

    render(withQuery(
      <SceneEditorForm mode="create" submitting={false} error={null} onSubmit={onSubmit} />,
    ));

    await user.selectOptions(screen.getByLabelText('ownerFilter.label'), 'org:9');
    await user.type(document.querySelector('#scene-slug') as HTMLInputElement, 'team-scene');
    await user.type(document.querySelector('#scene-name') as HTMLInputElement, 'Team Scene');
    await user.click(screen.getByRole('button', { name: 'new.submit' }));

    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      owner: { type: 'org', id: 9 },
      slug: 'team-scene',
      displayName: 'Team Scene',
    }));
  });

  it('submits a changed owner when editing a scene', async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();

    render(withQuery(
      <SceneEditorForm
        mode="edit"
        initial={{
          slug: 'personal-scene',
          displayName: 'Personal Scene',
          description: '',
          tags: [],
          definition: { mcp: [], plugins: [], prompts: [], required_credentials: [], match: { rules: [] }, assets: {} },
          owner: { type: 'user', id: 2 },
        }}
        submitting={false}
        error={null}
        onSubmit={onSubmit}
      />,
    ));

    await user.selectOptions(screen.getByLabelText('ownerFilter.label'), 'org:9');
    await user.click(screen.getByRole('button', { name: 'editor.save' }));

    expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({
      owner: { type: 'org', id: 9 },
      slug: 'personal-scene',
    }));
  });
});

describe('assembleSceneDefinition', () => {
  const draft: AdvancedDraft = {
    mcp: [{ name: 'fetch', configRaw: '' }],
    plugins: [{ source: 'exa@claude-plugins-official', ref: '', optional: true }],
    prompts: [{ id: 'p1', title: 'T', body: 'B' }],
    dataSources: [{ name: 'warehouse', kind: 'postgres', purpose: 'analytics', optional: false }],
    knowledgeBases: [{ name: 'remote-kb', purpose: 'search', optional: false }],
    matchBaseWeight: '',
    matchRules: [{ signal: 's', weight: '5', argsRaw: '' }],
    envPresets: [{ slug: 'e1', name: 'Preset 1', env: [{ key: 'A', value: '1' }] }],
    providers: ['DeepSeek'],
    assetsPassthrough: {},
    requiredCredentialsPassthrough: [{ alias: 'x', provider: 'slack' }],
    skillsPassthrough: [{ name: 'fireworks-tech-graph' }],
    disableToolGroups: [],
  };

  it('assembles structured fields and merges quick_actions / agent references into assets', () => {
    const qas: QuickActionRow[] = [{ slug: 'summarize', label: '汇总', prompt: 'do it' }];

    const def = assembleSceneDefinition(draft, qas, ['researcher', 'reviewer']);

    expect(def.mcp).toEqual([{ name: 'fetch' }]);
    expect(def.plugins[0].source).toBe('exa@claude-plugins-official');
    expect(def.plugins[0].optional).toBe(true);
    expect(def.prompts[0].id).toBe('p1');
    // Data sources assembled from the structured rows.
    expect(def.required_data_sources).toEqual([
      { name: 'warehouse', kind: 'postgres', purpose: 'analytics' },
    ]);
    // Legacy provider credentials preserved verbatim (no longer edited).
    expect(def.required_credentials).toEqual([{ alias: 'x', provider: 'slack' }]);
    expect(def.match.rules?.[0].signal).toBe('s');
    expect(def.match.rules?.[0].weight).toBe(5);
    // env_presets assembled from structured rows.
    expect(def.assets.env_presets).toEqual([{ slug: 'e1', name: 'Preset 1', env: { A: '1' } }]);
    // Structured rows + agent references merged in.
    expect(def.assets.quick_actions).toEqual([{ slug: 'summarize', label: '汇总', prompt: 'do it' }]);
    expect(def.assets.agents).toEqual([{ name: 'researcher' }, { name: 'reviewer' }]);
  });

  it('never emits project_templates / harness_specs, even if they leak via passthrough', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      assetsPassthrough: {
        project_templates: [{ slug: 'pt', payload: {} }],
        harness_specs: [{ slug: 'hs', payload: {} }],
        quick_actions: [{ slug: 'stale', label: 'stale', prompt: 'stale' }],
        agents: [{ name: 'stale-agent' }],
      },
    };
    const def = assembleSceneDefinition(d, [{ slug: 'fresh', label: 'L', prompt: 'P' }], []);
    expect(def.assets.project_templates).toBeUndefined();
    expect(def.assets.harness_specs).toBeUndefined();
    // Structured editors stay authoritative for quick_actions/agents.
    expect(def.assets.quick_actions).toEqual([{ slug: 'fresh', label: 'L', prompt: 'P' }]);
    expect(def.assets.agents).toEqual([]);
  });

  it('drops blank rows and dedups/trims agent references', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      mcp: [{ name: '', configRaw: '' }, { name: ' fetch ', configRaw: '' }],
      plugins: [{ source: '', ref: '', optional: false }],
      dataSources: [{ name: '', kind: '', purpose: '', optional: false }],
      envPresets: [{ slug: '', name: '', env: [{ key: 'X', value: '1' }] }],
    };
    const qas: QuickActionRow[] = [
      { slug: '', label: 'no slug', prompt: 'x' },
      { slug: 'keep', label: 'keep', prompt: 'y' },
    ];
    const def = assembleSceneDefinition(d, qas, ['a1', ' a1 ', '', '  ', 'a2']);
    expect(def.mcp).toEqual([{ name: 'fetch' }]);
    expect(def.plugins).toEqual([]);
    expect(def.required_data_sources).toEqual([]); // blank-name data source dropped
    expect(def.assets.env_presets).toBeUndefined(); // blank-slug preset dropped
    expect(def.assets.quick_actions).toEqual([{ slug: 'keep', label: 'keep', prompt: 'y' }]);
    expect(def.assets.agents).toEqual([{ name: 'a1' }, { name: 'a2' }]);
  });

  it('drops blank env-var keys within a preset', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      envPresets: [
        { slug: 'e1', name: '', env: [{ key: ' ', value: 'skip' }, { key: 'KEEP', value: 'v' }] },
      ],
    };
    const def = assembleSceneDefinition(d, [], []);
    expect(def.assets.env_presets).toEqual([{ slug: 'e1', env: { KEEP: 'v' } }]);
  });

  it('parses an inline MCP JSON config and references installed MCPs by name only', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      mcp: [
        { name: 'fetch', configRaw: '' },
        { name: 'my-server', configRaw: '{ "command": "npx", "args": ["-y", "x"], "env": { "K": "v" } }' },
      ],
    };
    const def = assembleSceneDefinition(d, [], []);
    expect(def.mcp[0]).toEqual({ name: 'fetch' }); // name-only reference
    expect(def.mcp[1]).toEqual({
      name: 'my-server',
      config: { command: 'npx', args: ['-y', 'x'], env: { K: 'v' } },
    });
  });

  it('throws when an inline MCP config is not valid JSON', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      mcp: [{ name: 'bad', configRaw: '{ not json' }],
    };
    expect(() => assembleSceneDefinition(d, [], [])).toThrow();
  });

  it('parses match base_weight and per-rule args JSON', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      matchBaseWeight: '3',
      matchRules: [{ signal: 'path_glob', weight: '5', argsRaw: '{ "pattern": "**/*.go" }' }],
    };
    const def = assembleSceneDefinition(d, [], []);
    expect(def.match.base_weight).toBe(3);
    expect(def.match.rules?.[0]).toEqual({ signal: 'path_glob', weight: 5, args: { pattern: '**/*.go' } });
  });

  it('throws when a match-rule args is not valid JSON', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      matchRules: [{ signal: 's', weight: '1', argsRaw: '{ not json' }],
    };
    expect(() => assembleSceneDefinition(d, [], [])).toThrow();
  });

  it('throws when a match-rule weight is not a number', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      matchRules: [{ signal: 's', weight: 'abc', argsRaw: '' }],
    };
    expect(() => assembleSceneDefinition(d, [], [])).toThrow();
  });

  it('emits disable_tool_groups (deduped, trimmed, blanks dropped) when set', () => {
    const d: AdvancedDraft = {
      ...emptyAdvancedDraft(),
      disableToolGroups: ['multi-agent', ' harness ', '', 'multi-agent'],
    };
    const def = assembleSceneDefinition(d, [], []);
    expect(def.disable_tool_groups).toEqual(['multi-agent', 'harness']);
  });

  it('omits disable_tool_groups entirely when none are hidden', () => {
    const def = assembleSceneDefinition(emptyAdvancedDraft(), [], []);
    expect(def.disable_tool_groups).toBeUndefined();
  });
});

describe('advancedDraftFromDefinition / round-trip', () => {
  it('splits a definition into the structured draft, dropping non-scene assets', () => {
    const draft = advancedDraftFromDefinition({
      mcp: [{ name: 'fetch' }, { name: 'my-server', config: { command: 'npx', args: ['x'] } }],
      plugins: [{ source: 'exa', ref: 'v1', optional: true }],
      prompts: [{ id: 'p1', title: 'T', body: 'B' }],
      required_credentials: [{ alias: 'x', provider: 'slack', purpose: 'send', optional: false }],
      required_data_sources: [{ name: 'warehouse', kind: 'postgres', purpose: 'analytics' }],
      match: { base_weight: 2, rules: [{ signal: 's', weight: 5, args: { k: 'v' } }] },
      assets: {
        quick_actions: [{ slug: 'q', label: 'L', prompt: 'P' }],
        agents: [{ name: 'a' }],
        env_presets: [{ slug: 'e1', name: 'E1', env: { A: '1' } }],
        project_templates: [{ slug: 'pt', payload: {} }],
        harness_specs: [{ slug: 'hs', payload: {} }],
      },
    });

    expect(draft.mcp[0]).toEqual({ name: 'fetch', configRaw: '' });
    expect(draft.mcp[1].name).toBe('my-server');
    expect(JSON.parse(draft.mcp[1].configRaw)).toEqual({ command: 'npx', args: ['x'] });
    expect(draft.plugins).toEqual([{ source: 'exa', ref: 'v1', optional: true }]);
    // Data sources split into structured rows; legacy creds kept as passthrough.
    expect(draft.dataSources).toEqual([
      { name: 'warehouse', kind: 'postgres', purpose: 'analytics', optional: false },
    ]);
    expect(draft.requiredCredentialsPassthrough).toEqual([
      { alias: 'x', provider: 'slack', purpose: 'send', optional: false },
    ]);
    expect(draft.matchBaseWeight).toBe('2');
    expect(draft.matchRules[0].weight).toBe('5');
    expect(JSON.parse(draft.matchRules[0].argsRaw)).toEqual({ k: 'v' });
    expect(draft.envPresets).toEqual([{ slug: 'e1', name: 'E1', env: [{ key: 'A', value: '1' }] }]);
    // project_templates / harness_specs are not a scene concern — dropped entirely.
    expect(draft.assetsPassthrough.project_templates).toBeUndefined();
    expect(draft.assetsPassthrough.harness_specs).toBeUndefined();
    expect(draft.assetsPassthrough.env_presets).toBeUndefined();
  });

  it('round-trips cleanly: definition → draft → definition drops the non-scene assets', () => {
    const def0 = {
      mcp: [],
      plugins: [],
      prompts: [],
      required_credentials: [],
      match: { rules: [] },
      assets: {
        quick_actions: [{ slug: 'q', label: 'L', prompt: 'P' }],
        agents: [],
        env_presets: [{ slug: 'e1', env: { A: '1' } }],
        project_templates: [{ slug: 'pt', payload: {} }],
        harness_specs: [{ slug: 'hs', payload: {} }],
      },
    };
    const draft = advancedDraftFromDefinition(def0);
    const def1 = assembleSceneDefinition(draft, [{ slug: 'q', label: 'L', prompt: 'P' }], []);
    expect(def1.assets.env_presets).toEqual([{ slug: 'e1', env: { A: '1' } }]);
    expect(def1.assets.project_templates).toBeUndefined();
    expect(def1.assets.harness_specs).toBeUndefined();
  });

  it('round-trip preserves disable_tool_groups (incl. unknown groups) — no silent drop', () => {
    const def0 = {
      mcp: [],
      plugins: [],
      prompts: [],
      required_credentials: [],
      match: { rules: [] },
      assets: {},
      disable_tool_groups: ['multi-agent', 'harness', 'future-group'],
    };
    const draft = advancedDraftFromDefinition(def0);
    expect(draft.disableToolGroups).toEqual(['multi-agent', 'harness', 'future-group']);
    const def1 = assembleSceneDefinition(draft, [], []);
    expect(def1.disable_tool_groups).toEqual(['multi-agent', 'harness', 'future-group']);
  });
});
