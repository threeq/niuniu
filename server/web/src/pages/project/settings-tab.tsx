import { useState, useEffect } from 'react';
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useNavigate } from '@tanstack/react-router';
import { Plus, X, Eye, EyeOff, Trash2 } from 'lucide-react';
import { api } from '@/lib/api';
import { toast } from 'sonner';
import type { Project, ProjectRepositoryBinding, Repository } from '@/types/api';
import { OwnerBadge } from '@/components/shared/owner-badge';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { LabelsSection } from './labels-section';
import { ColumnsEditorSection } from './columns-editor-section';
import { DefaultScenesSection } from './default-scenes-section';
import { ProjectCleanupPolicyCard } from './cleanup-policy-card';
import { SaveAsTemplateDialog } from './save-as-template-dialog';
import { ExternalSourcesPanel } from '@/components/projects/ExternalSourcesPanel';
import { ProjectDataSourcesPanel } from '@/components/projects/ProjectDataSourcesPanel';
import { ProjectKnowledgeBasesPanel } from '@/components/projects/ProjectKnowledgeBasesPanel';
import { ProjectImBotPanel } from '@/components/projects/ProjectImBotPanel';
import { useIsProjectAdmin } from '@/lib/use-is-project-admin';
import { useUpdateProjectStatus, useUpdateProjectColor } from '@/lib/hooks/use-projects';
import { DeleteProjectDialog } from '@/components/dialogs/delete-project-dialog';
import { ProjectColorPicker } from '@/components/shared/project-color-picker';

type SettingsSection = 'basic' | 'repos' | 'labels' | 'board' | 'externalSources' | 'imbot' | 'cleanup' | 'danger';

interface Props {
  projectId: number;
}

export function ProjectSettingsTab({ projectId }: Props) {
  const { t } = useTranslation(['projects', 'presets']);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const updateStatus = useUpdateProjectStatus();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [activeSection, setActiveSection] = useState<SettingsSection>('basic');
  const { data: project, isLoading } = useQuery({
    queryKey: ['project', String(projectId)],
    queryFn: () => api.get<Project>(`/projects/${projectId}`),
  });

  const { data: serverBindings = [] } = useQuery({
    queryKey: ['project-repos', String(projectId)],
    queryFn: () => api.listProjectRepositories(projectId),
  });

  const { data: candidateRepos = [] } = useQuery({
    queryKey: ['repositories-by-owner', project?.owner?.type, project?.owner?.id],
    queryFn: () => api.listRepositories({
      owner: project?.owner ? { type: project.owner.type, id: project.owner.id, slug: project.owner.slug } : undefined,
    }),
    enabled: !!project?.owner,
  });

  const branchesByRepo = useQuery({
    queryKey: ['branches-for-bindings', serverBindings.map((b: ProjectRepositoryBinding) => b.repository_id).join(',')],
    queryFn: async () => {
      const out: Record<number, string[]> = {};
      await Promise.all(serverBindings.map(async (b: ProjectRepositoryBinding) => {
        try { out[b.repository_id] = await api.getRepositoryBranches(String(b.repository_id)); }
        catch { out[b.repository_id] = [b.repo_default_branch || 'main']; }
      }));
      return out;
    },
    enabled: serverBindings.length > 0,
  });

  const addMut = useMutation({
    mutationFn: async (input: { repository_id: number; default_branch: string }) =>
      api.addProjectRepository(projectId, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-repos', String(projectId)] });
      qc.invalidateQueries({ queryKey: ['project', String(projectId)] });
    },
    onError: (err: unknown) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast.error(t('tabs.settings.addRepoFailed', { message: msg }));
    },
  });

  const removeMut = useMutation({
    mutationFn: async (repoId: number) => api.removeProjectRepository(projectId, repoId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-repos', String(projectId)] });
      qc.invalidateQueries({ queryKey: ['project', String(projectId)] });
    },
  });

  const patchMut = useMutation({
    mutationFn: async ({ repoId, branch }: { repoId: number; branch: string }) =>
      api.updateProjectRepositoryBranch(projectId, repoId, branch),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-repos', String(projectId)] });
    },
  });

  // Project default agent — drives the pre-selected engine when creating
  // workspaces under this project (and the engine for issue-auto-created ones).
  const defaultAgentMut = useMutation({
    mutationFn: async (cli: string) =>
      api.put(`/projects/${projectId}/default-cli-type`, { default_cli_type: cli }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project', String(projectId)] });
      qc.invalidateQueries({ queryKey: ['projects'] });
    },
    onError: () => toast.error(t('tabs.settings.saveFailed')),
  });

  // Project default Provider — inherited by new workspaces created from issues.
  const { data: providers = [] } = useQuery({
    queryKey: ['env-providers'],
    queryFn: () => api.listEnvProviders(),
  });
  const providerMut = useMutation({
    mutationFn: async (providerId: number | null) =>
      api.setProjectEnvProvider(String(projectId), providerId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project', String(projectId)] });
      qc.invalidateQueries({ queryKey: ['projects'] });
    },
    onError: () => toast.error(t('tabs.settings.saveFailed')),
  });

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [savingBasic, setSavingBasic] = useState(false);
  const [pickRepoId, setPickRepoId] = useState<number | ''>('');

  useEffect(() => {
    if (project) {
      setName(project.name);
      setDescription(project.description ?? '');
    }
  }, [project]);

  // Hooks must run unconditionally — call before any early returns.
  const canManageLabels = useIsProjectAdmin(project ?? null);
  const isAdmin = canManageLabels;
  const updateColor = useUpdateProjectColor(projectId);

  if (isLoading || !project) {
    return <div className="p-6 text-sm text-muted-foreground">{t('tabs.settings.loading')}</div>;
  }

  const saveBasic = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setSavingBasic(true);
    try {
      await api.put(`/projects/${projectId}`, { name, description });
      qc.invalidateQueries({ queryKey: ['project', String(projectId)] });
      qc.invalidateQueries({ queryKey: ['projects'] });
    } catch (err) {
      console.error(err);
      toast.error(t('tabs.settings.saveFailed'));
    } finally {
      setSavingBasic(false);
    }
  };

  const availableRepos = (candidateRepos ?? []).filter(
    (r: Repository) => !serverBindings.some((b: ProjectRepositoryBinding) => b.repository_id === r.id),
  );

  const handleAdd = async () => {
    if (!pickRepoId) return;
    const repo = (candidateRepos as Repository[]).find((r) => r.id === pickRepoId);
    if (!repo) return;
    const defaultBranch = repo.default_branch || 'main';
    await addMut.mutateAsync({ repository_id: pickRepoId as number, default_branch: defaultBranch });
    setPickRepoId('');
  };

  const sections: { key: SettingsSection; label: string }[] = [
    { key: 'basic', label: t('tabs.settings.sections.basic') },
    { key: 'repos', label: t('tabs.settings.sections.repos') },
    { key: 'labels', label: t('tabs.settings.sections.labels') },
    { key: 'board', label: t('tabs.settings.sections.board') },
    { key: 'externalSources', label: t('tabs.settings.sections.externalSources') },
    { key: 'imbot', label: t('tabs.settings.sections.imbot') },
    { key: 'cleanup', label: t('tabs.settings.sections.cleanup') },
    { key: 'danger', label: t('tabs.settings.sections.danger') },
  ];

  return (
    <div className="p-6 space-y-6 max-w-2xl">
      <h2 className="text-lg font-semibold">{t('tabs.settings.title')}</h2>

      {/* Sub-tab nav strip */}
      <nav className="flex gap-1 border-b border-warm-border">
        {sections.map(({ key, label }) => (
          <button
            key={key}
            type="button"
            onClick={() => setActiveSection(key)}
            className={[
              'px-3 py-2 text-sm font-medium border-b-2 -mb-px',
              activeSection === key
                ? 'border-b-info text-info'
                : 'border-b-transparent text-muted-foreground hover:text-warm-text',
            ].join(' ')}
          >
            {label}
          </button>
        ))}
      </nav>

      {/* Section: Board — columns (含 Gate 规范配置) + default scenes + a
          save-as-template entry. Full template management lives in the global
          project-blueprints settings page. */}
      {activeSection === 'board' && (
        <div className="space-y-6">
          <ColumnsEditorSection projectId={projectId} />
          <DefaultScenesSection projectId={projectId} canManage={isAdmin} />
          {isAdmin && (
            <div className="border rounded-lg p-4 flex items-start justify-between gap-4">
              <div className="min-w-0">
                <h3 className="text-sm font-semibold">{t('tabs.settings.templates.title')}</h3>
                <p className="mt-1 text-xs text-muted-foreground">{t('tabs.settings.templates.hint')}</p>
              </div>
              <SaveAsTemplateDialog projectId={projectId} />
            </div>
          )}
        </div>
      )}

      {/* Section: Basic info */}
      {activeSection === 'basic' && (
        <form onSubmit={saveBasic} className="border rounded-lg p-4 space-y-4">
          <h3 className="text-sm font-semibold">{t('tabs.settings.basicInfo')}</h3>
          <div className="grid gap-2">
            <label className="text-sm font-medium">{t('tabs.settings.projectName')} <span className="text-destructive">*</span></label>
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={savingBasic} />
          </div>
          <div className="grid gap-2">
            <label className="text-sm font-medium">{t('tabs.settings.projectDescription')}</label>
            <Input value={description} onChange={(e) => setDescription(e.target.value)} disabled={savingBasic} />
          </div>
          {/* 项目颜色 — race-guarded by updateColor.isPending so rapid A→B→A clicks don't reorder */}
          <div className="grid gap-2">
            <ProjectColorPicker
              value={project.color ?? null}
              onChange={(next) => updateColor.mutate(next)}
              disabled={!isAdmin || updateColor.isPending}
            />
          </div>
          {/* Default agent — applied immediately (mirrors color). */}
          <div className="grid gap-2">
            <label className="text-sm font-medium">{t('tabs.settings.defaultAgent')}</label>
            <select
              value={project.default_cli_type ?? 'claude'}
              onChange={(e) => defaultAgentMut.mutate(e.target.value)}
              disabled={!isAdmin || defaultAgentMut.isPending}
              className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
            >
              <option value="claude">{t('issue.workspace.cliType.claude')}</option>
              <option value="codex">{t('issue.workspace.cliType.codex')}</option>
              <option value="qwen">{t('issue.workspace.cliType.qwen')}</option>
              <option value="omp">{t('issue.workspace.cliType.omp')}</option>
            </select>
            <p className="text-xs text-muted-foreground">{t('tabs.settings.defaultAgentHint')}</p>
          </div>
          {/* Default Provider — inherited by new workspaces. */}
          <div className="grid gap-2">
            <label className="text-sm font-medium">{t('tabs.settings.defaultProvider')}</label>
            <select
              value={project.env_provider_id ?? 0}
              onChange={(e) => providerMut.mutate(e.target.value === '0' ? null : Number(e.target.value))}
              disabled={!isAdmin || providerMut.isPending}
              className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
            >
              <option value={0}>{t('tabs.settings.defaultProviderNone')}</option>
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}{Object.keys(p.base_urls ?? {}).length ? ` · ${Object.keys(p.base_urls).join('/')}` : ''}{p.model ? ` · ${p.model}` : ''}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">{t('tabs.settings.defaultProviderHint')}</p>
          </div>
          <div className="grid gap-2">
            <label className="text-sm font-medium">{t('tabs.settings.owner')}</label>
            <div className="flex items-center gap-2">
              {project.owner && <OwnerBadge owner={project.owner} />}
              <span className="text-xs text-muted-foreground">
                {t('tabs.settings.ownerHint')}<code className="bg-muted px-1 rounded">niuniu admin transfer-resource</code>
              </span>
            </div>
          </div>
          <div className="flex justify-end">
            <Button type="submit" disabled={savingBasic || !name.trim()}>
              {savingBasic ? t('tabs.settings.saving') : t('tabs.settings.save')}
            </Button>
          </div>
        </form>
      )}

      {/* Section: Linked repos — operations take effect immediately */}
      {activeSection === 'repos' && (
        <div className="border rounded-lg p-4 space-y-4">
          <h3 className="text-sm font-semibold">{t('tabs.settings.linkedRepos')}</h3>
          <p className="text-xs text-muted-foreground">
            {t('tabs.settings.linkedReposHint')}
          </p>

          {serverBindings.length > 0 ? (
            <div className="border rounded-md divide-y">
              {serverBindings.map((b: ProjectRepositoryBinding) => {
                const branches = branchesByRepo.data?.[b.repository_id] ?? [b.project_default_branch || b.repo_default_branch || 'main'];
                return (
                  <div key={b.repository_id} className="flex items-center gap-2 p-2">
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium truncate">{b.name}</p>
                      <p className="text-xs text-muted-foreground truncate">{b.path}</p>
                    </div>
                    <select
                      value={b.project_default_branch || ''}
                      onChange={(e) => patchMut.mutate({ repoId: b.repository_id, branch: e.target.value })}
                      disabled={patchMut.isPending}
                      className="h-8 w-36 text-xs rounded border border-input bg-background px-2 py-1"
                    >
                      {branches.map((br: string) => (
                        <option key={br} value={br}>{br}</option>
                      ))}
                    </select>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => removeMut.mutate(b.repository_id)}
                      disabled={removeMut.isPending}
                      className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  </div>
                );
              })}
            </div>
          ) : (
            <p className="text-xs text-muted-foreground text-center py-2">{t('tabs.settings.noLinkedRepos')}</p>
          )}

          <div className="flex gap-2">
            <select
              value={pickRepoId}
              onChange={(e) => setPickRepoId(e.target.value ? Number(e.target.value) : '')}
              disabled={addMut.isPending || availableRepos.length === 0}
              className="flex-1 h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
            >
              <option value="">{availableRepos.length === 0 ? t('tabs.settings.allReposLinked') : t('tabs.settings.selectRepo')}</option>
              {availableRepos.map((r: Repository) => (
                <option key={r.id} value={r.id}>{r.name}</option>
              ))}
            </select>
            <Button type="button" variant="outline" size="sm" onClick={handleAdd} disabled={!pickRepoId || addMut.isPending}>
              <Plus className="h-4 w-4 mr-1" /> {t('tabs.settings.addRepo')}
            </Button>
          </div>
        </div>
      )}

      {/* Section: External sources — bind GitHub/etc. trackers AND data
          sources (dataconn) to this project. */}
      {activeSection === 'externalSources' && (
        <div className="space-y-6">
          <ExternalSourcesPanel
            projectId={projectId}
            projectOwner={project?.owner}
          />
          <ProjectDataSourcesPanel projectId={projectId} />
          <ProjectKnowledgeBasesPanel projectId={projectId} />
        </div>
      )}

      {/* Section: IM Bot — project-level IM channels + chat pairing (Epic #555). */}
      {activeSection === 'imbot' && (
        <ProjectImBotPanel projectId={projectId} projectOwner={project?.owner} />
      )}

      {/* Section: Workspace cleanup — per-project auto-cleanup policy for
          completed / not-started workspaces idle past a retention window. */}
      {activeSection === 'cleanup' && (
        <ProjectCleanupPolicyCard projectId={projectId} />
      )}

      {/* Section: Labels — shared label dictionary for the project's issues. */}
      {activeSection === 'labels' && (
        <div className="border rounded-lg p-4">
          <LabelsSection projectId={projectId} canManage={canManageLabels} />
        </div>
      )}

      {/* Section: Danger zone — hide / delete actions migrated from the sidebar
          dropdown. Hosted here so the sidebar list rows stay focused on
          navigation + at-a-glance status. */}
      {activeSection === 'danger' && (
        <div className="border border-destructive/30 rounded-lg p-4 space-y-4 bg-destructive/5">
          <h3 className="text-sm font-semibold text-destructive">
            {t('tabs.settings.dangerZone')}
          </h3>

          {/* Hide / Show toggle */}
          <div className="flex items-start justify-between gap-4">
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-warm-text">
                {project.status === 'hidden'
                  ? t('detail.menu.show')
                  : t('detail.menu.hide')}
              </div>
              <div className="text-xs text-warm-text-muted mt-1">
                {project.status === 'hidden'
                  ? t('tabs.settings.showProjectDesc')
                  : t('tabs.settings.hideProjectDesc')}
              </div>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={updateStatus.isPending}
              onClick={() => {
                const next = project.status === 'hidden' ? 'active' : 'hidden';
                updateStatus.mutate(
                  { id: projectId, status: next },
                  {
                    onSuccess: () => {
                      if (next === 'hidden') {
                        navigate({ to: '/projects', replace: true });
                      }
                    },
                  }
                );
              }}
            >
              {project.status === 'hidden' ? (
                <Eye className="h-4 w-4 mr-1" aria-hidden="true" />
              ) : (
                <EyeOff className="h-4 w-4 mr-1" aria-hidden="true" />
              )}
              {project.status === 'hidden'
                ? t('detail.menu.show')
                : t('detail.menu.hide')}
            </Button>
          </div>

          {/* Delete project */}
          <div className="flex items-start justify-between gap-4 pt-3 border-t border-destructive/20">
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-destructive">
                {t('detail.menu.delete')}
              </div>
              <div className="text-xs text-warm-text-muted mt-1">
                {t('tabs.settings.deleteProjectDesc')}
              </div>
            </div>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              onClick={() => setDeleteOpen(true)}
            >
              <Trash2 className="h-4 w-4 mr-1" aria-hidden="true" />
              {t('detail.menu.delete')}
            </Button>
          </div>
        </div>
      )}

      <DeleteProjectDialog
        open={deleteOpen}
        onOpenChange={(open) => setDeleteOpen(open)}
        project={project}
      />

    </div>
  );
}
