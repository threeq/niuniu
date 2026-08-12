import { useState, useEffect } from 'react';
import { Settings2, Plus, Trash2, GitBranch, FolderGit2, Loader2, Copy, Archive, ShieldCheck } from 'lucide-react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
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
import { useWorkspaces } from '@/lib/hooks/use-workspaces';
import type { Workspace, WorkspaceRepoDetail, WorktreeChangeStatus } from '@/types/api';
import { DeleteWorkspaceDialog } from '@/components/dialogs/delete-workspace-dialog';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { ScheduleManager } from './schedule-manager';
import { AutohostSettingsDialog } from '@/components/dialogs/autohost-settings-dialog';

// Env keys owned exclusively by the AutohostSettingsDialog. Filtered out of
// the raw env editor (load) and merged from the latest server state on save,
// so the two dialogs can't clobber each other under the full-replace PUT.
const AUTOHOST_MANAGED_KEYS = new Set([
  'NIUNIU_PERMISSION_MODE',
  'NIUNIU_AUTOHOST_BUDGET',
  'NIUNIU_AUTOHOST_ERROR_BUDGET',
  'NIUNIU_AUTOHOST_CONTINUE_PROMPT',
  'NIUNIU_AUTOHOST_RECOVER_PROMPT',
  'NIUNIU_AUTOHOST_GOAL_CONDITION',
]);

// Agent settings shared by every engine (command / args / model / allowed-tools).
// Moved here from the per-agent settings dialog: they apply regardless of which
// CLI the workspace runs. Surfaced as labeled fields, filtered out of the raw
// env editor below, and merged back on save (same anti-clobber pattern as the
// autohost keys). Labels reuse the panels.claudeSettings.fields.* strings.
const SHARED_AGENT_FIELDS = [
  { key: 'NIUNIU_AGENT_COMMAND', i18nKey: 'agentCommand' },
  { key: 'NIUNIU_AGENT_ARGS', i18nKey: 'agentArgs' },
  { key: 'NIUNIU_MODEL', i18nKey: 'model' },
  { key: 'NIUNIU_ALLOWED_TOOLS', i18nKey: 'allowedTools' },
] as const;
const SHARED_AGENT_KEYS = new Set<string>(SHARED_AGENT_FIELDS.map((f) => f.key));

// Per-engine example/default hints for the shared agent fields, so the
// placeholders read true for the workspace's actual engine (a qwen workspace
// shouldn't suggest "default: claude" or claude-only model names). The visible
// "default:" / "e.g." prefix is i18n'd; only the technical example token varies.
const AGENT_HINTS: Record<string, Record<string, string>> = {
  claude: {
    NIUNIU_AGENT_COMMAND: 'claude',
    NIUNIU_AGENT_ARGS: '--allowedTools Edit,Write,Bash',
    NIUNIU_MODEL: 'sonnet, opus, claude-sonnet-4-6',
    NIUNIU_ALLOWED_TOOLS: 'Bash(git:*) Edit Read',
  },
  codex: {
    NIUNIU_AGENT_COMMAND: 'codex',
    NIUNIU_AGENT_ARGS: '--full-auto',
    NIUNIU_MODEL: 'gpt-5-codex, o4-mini',
    NIUNIU_ALLOWED_TOOLS: 'Bash(git:*) Edit Read',
  },
  qwen: {
    NIUNIU_AGENT_COMMAND: 'qwen',
    NIUNIU_AGENT_ARGS: '--yolo',
    NIUNIU_MODEL: 'qwen3-coder-plus, deepseek-v3',
    NIUNIU_ALLOWED_TOOLS: 'Bash(git:*) Edit Read',
  },
  omp: {
    NIUNIU_AGENT_COMMAND: 'omp',
    NIUNIU_AGENT_ARGS: '--mode rpc',
    NIUNIU_MODEL: 'glm-4.6, deepseek-v3, qwen3, kimi-k2',
    NIUNIU_ALLOWED_TOOLS: 'Bash Edit Read',
  },
  goose: {
    NIUNIU_AGENT_COMMAND: 'goose',
    NIUNIU_AGENT_ARGS: 'acp',
    NIUNIU_MODEL: 'openrouter:<model>, ollama:<model>, anthropic:<model>',
    NIUNIU_ALLOWED_TOOLS: 'Bash Edit Read',
  },
};

interface WorkspaceSettingsDialogProps {
  workspace: Workspace;
}

export function WorkspaceSettingsDialog({ workspace }: WorkspaceSettingsDialogProps) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(workspace.name);
  const [envVars, setEnvVars] = useState<{ key: string; value: string }[]>([]);
  const [agentFields, setAgentFields] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [pendingChanges, setPendingChanges] = useState<WorktreeChangeStatus[] | null>(null);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const { deleteWorkspace, forceDeleteWorkspace, isDeleting, refetch, archiveWorkspace, isArchiving } = useWorkspaces();

  // Autohost settings sub-dialog. Editing autohost env keys lives in its own
  // dialog component because the form is too tall to inline here. The parent's
  // envVars state is intentionally NOT synced with the sub-dialog: each writes
  // to /workspaces/:id/env independently; the autohost sub-dialog patches only
  // its own keys, so it can't clobber the parent's edits in flight.
  const [autohostDialogOpen, setAutohostDialogOpen] = useState(false);

  // Add worktree state
  const [showAddWorktree, setShowAddWorktree] = useState(false);
  const [selectedRepoId, setSelectedRepoId] = useState<string>('');
  const [selectedBranch, setSelectedBranch] = useState('');
  const [branches, setBranches] = useState<string[]>([]);
  const [loadingBranches, setLoadingBranches] = useState(false);
  const [addingWorktree, setAddingWorktree] = useState(false);
  const [showPresetDropdown, setShowPresetDropdown] = useState(false);

  // Fetch workspace repositories (worktrees)
  const { data: wsRepos = [], refetch: refetchRepos } = useQuery({
    queryKey: ['workspace-repositories', workspace.id],
    queryFn: () => api.get<WorkspaceRepoDetail[]>(`/workspaces/${workspace.id}/repositories`),
    enabled: open,
  });

  // Fetch all repositories for the "add worktree" dropdown
  const { data: allRepos = [] } = useQuery({
    queryKey: ['repositories'],
    queryFn: () => api.listRepositories(),
    enabled: open && showAddWorktree,
  });

  // Filter out repos that already have a worktree in this workspace
  const availableRepos = allRepos.filter(
    (repo) => !wsRepos.some((wr) => String(wr.repository_id) === String(repo.id))
  );

  // Fetch env presets for import (pre-fetch when dialog opens)
  const { data: presets = [], isLoading: isLoadingPresets } = useQuery({
    queryKey: ['env-presets'],
    queryFn: () => api.listEnvPresets(),
    enabled: open,
  });

  // Subscription-platform providers for direct workspace binding (issue #653).
  const { data: providers = [] } = useQuery({
    queryKey: ['env-providers'],
    queryFn: () => api.listEnvProviders(),
    enabled: open,
  });
  const [boundProviderId, setBoundProviderId] = useState<number | null>(null);
  const [savingProvider, setSavingProvider] = useState(false);
  useEffect(() => {
    if (open) setBoundProviderId(workspace.env_provider_id ?? null);
  }, [open, workspace.env_provider_id]);
  const changeProvider = async (id: number | null) => {
    setBoundProviderId(id);
    setSavingProvider(true);
    try {
      await api.setWorkspaceEnvProvider(workspace.id, id);
      queryClient.invalidateQueries({ queryKey: ['workspace', workspace.id] });
    } finally {
      setSavingProvider(false);
    }
  };

  useEffect(() => {
    if (!open) return;
    setName(workspace.name);
    setShowAddWorktree(false);
    setShowPresetDropdown(false);
    setSelectedRepoId('');
    setSelectedBranch('');
    setBranches([]);
    api
      .get<{ env: Record<string, string> }>(`/workspaces/${workspace.id}/env`)
      .then((res) => {
        // Hide autohost-managed keys — they have their own dialog. Showing them
        // here would let users edit the same row from two places and the
        // full-replace PUT would race.
        const entries = Object.entries(res.env)
          .filter(([key]) => !AUTOHOST_MANAGED_KEYS.has(key) && !SHARED_AGENT_KEYS.has(key))
          .map(([key, value]) => ({ key, value }));
        setEnvVars(entries.length > 0 ? entries : []);
        const af: Record<string, string> = {};
        for (const { key } of SHARED_AGENT_FIELDS) {
          if (res.env[key]) af[key] = res.env[key];
        }
        setAgentFields(af);
      })
      .catch(() => {
        setEnvVars([]);
        setAgentFields({});
      });
  }, [open, workspace.id, workspace.name]);

  // Fetch branches when repo changes
  useEffect(() => {
    if (!selectedRepoId) {
      setBranches([]);
      setSelectedBranch('');
      return;
    }
    setLoadingBranches(true);
    api.getRepositoryBranches(selectedRepoId).then((b) => {
      setBranches(b);
      const repo = allRepos.find((r) => String(r.id) === selectedRepoId);
      setSelectedBranch(repo?.default_branch || b[0] || 'main');
      setLoadingBranches(false);
    }).catch(() => {
      setBranches([]);
      setLoadingBranches(false);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedRepoId]);

  const handleSave = async () => {
    setSaving(true);
    try {
      if (name !== workspace.name) {
        await api.put(`/workspaces/${workspace.id}/name`, { name });
      }
      const envMap: Record<string, string> = {};
      for (const { key, value } of envVars) {
        if (key.trim()) {
          envMap[key.trim()] = value;
        }
      }
      // Shared agent fields (labeled section): non-empty set, empty removed.
      for (const { key } of SHARED_AGENT_FIELDS) {
        const v = agentFields[key]?.trim();
        if (v) {
          envMap[key] = v;
        } else {
          delete envMap[key];
        }
      }
      // Preserve autohost-managed keys by re-reading the latest server state
      // and merging them in. Without this merge, opening this dialog after
      // the autohost sub-dialog had saved would silently overwrite those
      // values on next parent save (PUT is full-replace, see service.SetWorkspaceEnvVars).
      try {
        const latest = await api.get<{ env: Record<string, string> }>(`/workspaces/${workspace.id}/env`);
        for (const key of AUTOHOST_MANAGED_KEYS) {
          if (latest.env[key] !== undefined) {
            envMap[key] = latest.env[key];
          }
        }
      } catch {
        // If the GET fails, fall through with whatever envMap we have — the
        // PUT will go ahead with non-autohost keys only, which is the same
        // failure mode as before this merge was added.
      }
      await api.put(`/workspaces/${workspace.id}/env`, { env: envMap });
      queryClient.invalidateQueries({ queryKey: ['workspace', workspace.id] });
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
      setOpen(false);
    } finally {
      setSaving(false);
    }
  };

  const handleAddWorktree = async () => {
    if (!selectedRepoId || !selectedBranch) return;
    setAddingWorktree(true);
    try {
      await api.addWorkspaceRepository(workspace.id, {
        repository_id: selectedRepoId,
        branch: selectedBranch,
      });
      await refetchRepos();
      queryClient.invalidateQueries({ queryKey: ['workspace-tree-groups', workspace.id] });
      setShowAddWorktree(false);
      setSelectedRepoId('');
      setSelectedBranch('');
      setBranches([]);
    } catch (error) {
      console.error('Failed to add worktree:', error);
    } finally {
      setAddingWorktree(false);
    }
  };

  const addEnvVar = () => {
    setEnvVars((prev) => [...prev, { key: '', value: '' }]);
  };

  const removeEnvVar = (index: number) => {
    setEnvVars((prev) => prev.filter((_, i) => i !== index));
  };

  const updateEnvVar = (index: number, field: 'key' | 'value', val: string) => {
    setEnvVars((prev) =>
      prev.map((item, i) => (i === index ? { ...item, [field]: val } : item)),
    );
  };

  const importPreset = (env: Record<string, string>) => {
    const existingKeys = new Set(envVars.map(v => v.key));
    const newVars = Object.entries(env)
      .filter(([key]) => !existingKeys.has(key))
      .map(([key, value]) => ({ key, value }));
    setEnvVars(prev => [...prev, ...newVars]);
    setShowPresetDropdown(false);
  };

  // Close preset dropdown on click-outside
  useEffect(() => {
    if (!showPresetDropdown) return;
    const handler = (e: MouseEvent) => {
      if (!(e.target as Element).closest('.preset-dropdown-area')) {
        setShowPresetDropdown(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [showPresetDropdown]);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('panels.workspaceSettings.triggerTitle')}
        >
          <Settings2 className="h-3.5 w-3.5" />
          <span>{t('panels.workspaceSettings.trigger')}</span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-lg max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>{t('panels.workspaceSettings.title')}</DialogTitle>
          <DialogDescription>{t('panels.workspaceSettings.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-5">
          {/* Workspace name */}
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">{t('panels.workspaceSettings.name')}</label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full rounded-md border border-border px-3 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-info focus:border-transparent"
            />
          </div>

          {/* Repositories & Worktrees */}
          <div>
            <div className="flex items-center justify-between mb-1">
              <label className="block text-sm font-medium text-foreground">{t('panels.workspaceSettings.boundRepos')}</label>
              <button
                onClick={() => setShowAddWorktree(!showAddWorktree)}
                className="flex items-center gap-0.5 text-xs text-blue-500 hover:text-blue-700"
              >
                <Plus className="h-3 w-3" />
                {t('panels.workspaceSettings.addRepo')}
              </button>
            </div>
            <p className="text-xs text-muted-foreground mb-2">
              {t('panels.workspaceSettings.boundReposDesc')}
            </p>

            {/* Existing worktrees list */}
            {wsRepos.length === 0 ? (
              <p className="text-xs text-muted-foreground italic">{t('panels.workspaceSettings.noBoundRepos')}</p>
            ) : (
              <div className="rounded-md border border-border divide-y divide-border">
                {wsRepos.map((wr) => (
                  <div key={wr.id} className="px-3 py-2">
                    <div className="flex items-center gap-2">
                      <FolderGit2 className="h-3.5 w-3.5 text-muted-foreground/70 flex-shrink-0" />
                      <span className="text-sm font-medium text-foreground truncate">
                        {wr.repository?.name || `Repo #${wr.repository_id}`}
                      </span>
                      <div className="flex items-center gap-1 ml-auto flex-shrink-0">
                        <GitBranch className="h-3 w-3 text-muted-foreground/70" />
                        <span className="text-xs text-muted-foreground font-mono">{wr.branch}</span>
                      </div>
                    </div>
                    <p className="text-xs text-muted-foreground/70 mt-0.5 pl-5.5 truncate font-mono">
                      {wr.worktree_path
                        .replace(/\\/g, '/')
                        .replace(/^.*\/workspaces\//, '')}
                    </p>
                  </div>
                ))}
              </div>
            )}

            {/* Add worktree form */}
            {showAddWorktree && (
              <div className="mt-2 rounded-md border border-info/30 bg-info/5 p-3 space-y-2">
                <div>
                  <label className="block text-xs font-medium text-muted-foreground mb-1">{t('panels.workspaceSettings.selectRepo')}</label>
                  <select
                    value={selectedRepoId}
                    onChange={(e) => setSelectedRepoId(e.target.value)}
                    disabled={addingWorktree}
                    className="w-full h-8 rounded border border-border bg-background px-2 text-sm focus:outline-none focus:ring-1 focus:ring-info"
                  >
                    <option value="">
                      {availableRepos.length === 0 ? t('panels.workspaceSettings.allReposAdded') : t('panels.workspaceSettings.selectRepoPlaceholder')}
                    </option>
                    {availableRepos.map((repo) => (
                      <option key={repo.id} value={String(repo.id)}>
                        {repo.name}
                      </option>
                    ))}
                  </select>
                </div>

                {selectedRepoId && (
                  <div>
                    <label className="block text-xs font-medium text-muted-foreground mb-1">{t('panels.workspaceSettings.selectBranch')}</label>
                    {loadingBranches ? (
                      <div className="flex items-center gap-1 text-xs text-muted-foreground/70 py-1">
                        <Loader2 className="h-3 w-3 animate-spin" />
                        {t('panels.workspaceSettings.loadingBranches')}
                      </div>
                    ) : (
                      <select
                        value={selectedBranch}
                        onChange={(e) => setSelectedBranch(e.target.value)}
                        disabled={addingWorktree}
                        className="w-full h-8 rounded border border-border bg-background px-2 text-sm focus:outline-none focus:ring-1 focus:ring-info"
                      >
                        {branches.map((b) => (
                          <option key={b} value={b}>{b}</option>
                        ))}
                      </select>
                    )}
                  </div>
                )}

                <div className="flex justify-end gap-2 pt-1">
                  <button
                    onClick={() => {
                      setShowAddWorktree(false);
                      setSelectedRepoId('');
                      setSelectedBranch('');
                    }}
                    className="rounded px-2.5 py-1 text-xs text-muted-foreground hover:bg-accent"
                  >
                    {t('common:actions.cancel')}
                  </button>
                  <button
                    onClick={handleAddWorktree}
                    disabled={!selectedRepoId || !selectedBranch || addingWorktree}
                    className="rounded bg-info px-2.5 py-1 text-xs text-white hover:bg-info/90 disabled:opacity-50 flex items-center gap-1"
                  >
                    {addingWorktree && <Loader2 className="h-3 w-3 animate-spin" />}
                    {addingWorktree ? t('panels.workspaceSettings.addingWorktree') : t('panels.workspaceSettings.addWorktree')}
                  </button>
                </div>
              </div>
            )}
          </div>

          {/* Shared agent settings (apply to every engine) */}
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">
              {t('panels.workspaceSettings.agentSection')}
            </label>
            <p className="text-xs text-muted-foreground mb-2">
              {t('panels.workspaceSettings.agentSectionDesc')}
            </p>
            <div className="space-y-2">
              {SHARED_AGENT_FIELDS.map(({ key, i18nKey }) => {
                const hint = (AGENT_HINTS[workspace.cli_type ?? 'claude'] ?? AGENT_HINTS.claude)[key];
                const placeholder = key === 'NIUNIU_AGENT_COMMAND'
                  ? t('panels.workspaceSettings.agentDefaultHint', { value: hint })
                  : t('panels.workspaceSettings.agentEgHint', { value: hint });
                return (
                  <div key={key}>
                    <label className="block text-xs font-medium text-muted-foreground mb-1">
                      {t(`panels.claudeSettings.fields.${i18nKey}.label`)}
                    </label>
                    <input
                      type="text"
                      value={agentFields[key] ?? ''}
                      onChange={(e) => setAgentFields((prev) => ({ ...prev, [key]: e.target.value }))}
                      placeholder={placeholder}
                      className="w-full rounded-md border border-border px-3 py-1.5 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-info focus:border-transparent bg-background"
                    />
                  </div>
                );
              })}
            </div>
          </div>

          {/* Env vars */}
          <div>
            {/* Direct subscription-platform provider binding (issue #653): the
                common path — pick a provider here, no scene needed. */}
            <div className="mb-3">
              <label className="block text-sm font-medium text-foreground mb-1">
                {t('panels.workspaceSettings.envProvider')}
              </label>
              <select
                value={boundProviderId ?? 0}
                onChange={(e) => changeProvider(e.target.value === '0' ? null : Number(e.target.value))}
                disabled={savingProvider}
                className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-info"
              >
                <option value={0}>{t('panels.workspaceSettings.envProviderNone')}</option>
                {providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}{Object.keys(p.base_urls ?? {}).length ? ` · ${Object.keys(p.base_urls).join('/')}` : ''}{p.model ? ` · ${p.model}` : ''}
                  </option>
                ))}
              </select>
              <p className="mt-1 text-xs text-muted-foreground">
                {t('panels.workspaceSettings.envProviderHint')}
              </p>
            </div>

            <div className="flex items-center justify-between mb-1">
              <label className="block text-sm font-medium text-foreground">{t('panels.workspaceSettings.envVars')}</label>
              <div className="flex items-center gap-2">
                <div className="relative preset-dropdown-area">
                  <button
                    onClick={() => setShowPresetDropdown(!showPresetDropdown)}
                    className="flex items-center gap-0.5 text-xs text-blue-500 hover:text-blue-700"
                  >
                    <Copy className="h-3 w-3" />
                    {t('panels.workspaceSettings.importPreset')}
                  </button>
                  {showPresetDropdown && (
                    <div className="absolute right-0 top-full mt-1 w-48 rounded-md border border-border bg-card shadow-lg z-10">
                      {isLoadingPresets && presets.length === 0 && (
                        <div className="px-3 py-2 text-xs text-muted-foreground">{t('common:actions.loading')}</div>
                      )}
                      {!isLoadingPresets && presets.length === 0 && (
                        <div className="px-3 py-2 text-xs text-muted-foreground">{t('panels.workspaceSettings.noPresets')}</div>
                      )}
                      {presets.map((preset) => (
                        <button
                          key={preset.id}
                          onClick={() => importPreset(preset.env)}
                          className="w-full text-left px-3 py-2 text-xs hover:bg-accent border-b border-border last:border-b-0"
                        >
                          <span className="font-medium">{preset.name}</span>
                          {preset.description && (
                            <span className="text-muted-foreground/70 ml-1">{preset.description}</span>
                          )}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
                <button
                  onClick={addEnvVar}
                  className="flex items-center gap-0.5 text-xs text-blue-500 hover:text-blue-700"
                >
                  <Plus className="h-3 w-3" />
                  {t('panels.workspaceSettings.addEnvVar')}
                </button>
              </div>
            </div>
            <p className="text-xs text-muted-foreground mb-2">
              {t('panels.workspaceSettings.envInjectDesc')}
            </p>

            {envVars.length === 0 ? (
              <p className="text-xs text-muted-foreground italic">{t('panels.workspaceSettings.noEnvVars')}</p>
            ) : (
              <div className="space-y-1.5">
                {envVars.map((item, index) => (
                  <div key={index} className="flex items-center gap-1.5">
                    <input
                      type="text"
                      value={item.key}
                      onChange={(e) => updateEnvVar(index, 'key', e.target.value)}
                      placeholder="KEY"
                      className="flex-1 min-w-0 rounded border border-border px-2 py-1 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-info"
                    />
                    <span className="text-muted-foreground/70 text-xs">=</span>
                    <input
                      type="password"
                      value={item.value}
                      onChange={(e) => updateEnvVar(index, 'value', e.target.value)}
                      placeholder="value"
                      autoComplete="off"
                      className="flex-1 min-w-0 rounded border border-border px-2 py-1 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-info"
                    />
                    <button
                      onClick={() => removeEnvVar(index)}
                      className="p-1 text-gray-400 hover:text-red-500"
                    >
                      <Trash2 className="h-3 w-3" />
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Auto-host */}
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">
              {t('panels.workspaceSettings.autohost')}
            </label>
            <p className="text-xs text-muted-foreground mb-2">
              {t('panels.workspaceSettings.autohostDesc')}
            </p>
            <button
              type="button"
              onClick={() => setAutohostDialogOpen(true)}
              className="flex items-center gap-1.5 rounded-md border border-border bg-card px-3 py-1.5 text-sm text-foreground hover:bg-accent transition-colors"
            >
              <ShieldCheck className="h-3.5 w-3.5" />
              {t('panels.workspaceSettings.autohostConfigure')}
            </button>
          </div>

          {/* Schedules */}
          <ScheduleManager workspaceId={String(workspace.id)} />
        </div>

        <AutohostSettingsDialog
          open={autohostDialogOpen}
          onOpenChange={setAutohostDialogOpen}
          workspaceId={String(workspace.id)}
        />

        {/* Danger zone */}
        <div className="border-t border-red-200 pt-4">
          <label className="block text-sm font-medium text-red-600 mb-1">{t('panels.workspaceSettings.dangerZone')}</label>
          <p className="text-xs text-muted-foreground mb-2">{t('panels.workspaceSettings.archiveDesc')}</p>
          <div className="flex gap-2">
            <button
              onClick={() => setConfirmArchive(true)}
              disabled={isArchiving || isDeleting}
              className="rounded-md border border-border bg-muted px-3 py-1.5 text-sm text-muted-foreground hover:bg-accent disabled:opacity-50 flex items-center gap-1.5"
            >
              <Archive className="h-3.5 w-3.5" />
              {isArchiving ? t('panels.workspaceSettings.archiving') : t('panels.workspaceSettings.archive')}
            </button>
            <button
              onClick={async () => {
                try {
                  const changes = await deleteWorkspace(String(workspace.id));
                  if (changes) {
                    setPendingChanges(changes);
                  } else {
                    await refetch();
                    navigate({ to: '/workspaces' });
                    setOpen(false);
                  }
                } catch {
                  // Non-409 errors are already surfaced by apiFetch toast
                }
              }}
              disabled={isDeleting || isArchiving}
              className="rounded-md border border-red-300 bg-red-50 px-3 py-1.5 text-sm text-red-600 hover:bg-red-100 disabled:opacity-50 flex items-center gap-1.5"
            >
              <Trash2 className="h-3.5 w-3.5" />
              {isDeleting ? t('panels.workspaceSettings.deleting') : t('panels.workspaceSettings.delete')}
            </button>
          </div>
        </div>

        {/* Footer */}
        <DialogFooter>
          <button
            onClick={() => setOpen(false)}
            className="rounded-md px-3 py-1.5 text-sm text-muted-foreground hover:bg-accent"
          >
            {t('common:actions.cancel')}
          </button>
          <button
            onClick={handleSave}
            disabled={saving || !name.trim()}
            className="rounded-md bg-info px-3 py-1.5 text-sm text-white hover:bg-info/90 disabled:opacity-50"
          >
            {saving ? t('panels.workspaceSettings.saving') : t('panels.workspaceSettings.save')}
          </button>
        </DialogFooter>
      </DialogContent>
      <DeleteWorkspaceDialog
        open={pendingChanges !== null}
        onOpenChange={(open) => {
          if (!open) setPendingChanges(null);
        }}
        changes={pendingChanges ?? []}
        onForceDelete={() => {
          forceDeleteWorkspace(String(workspace.id), {
            onSuccess: async () => {
              setPendingChanges(null);
              await refetch();
              navigate({ to: '/workspaces' });
              setOpen(false);
            },
          });
        }}
        isDeleting={isDeleting}
      />
      <AlertDialog open={confirmArchive} onOpenChange={setConfirmArchive}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('panels.workspaceSettings.confirmArchiveTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('panels.workspaceSettings.confirmArchiveDesc')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common:actions.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                try {
                  await archiveWorkspace(String(workspace.id));
                  await refetch();
                  navigate({ to: '/workspaces' });
                  setOpen(false);
                } catch {
                  // Errors surfaced by apiFetch toast
                }
              }}
            >
              {isArchiving ? t('panels.workspaceSettings.archiving') : t('panels.workspaceSettings.confirmArchive')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Dialog>
  );
}
