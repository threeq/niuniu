import { useState, useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ChevronsUpDown, X, Search, Package } from 'lucide-react';
import { api } from '@/lib/api';
import { useAuthStore } from '@/stores/auth-store';
import { OwnerPicker } from '@/components/shared/owner-picker';
import type { OwnerRef } from '@/types/org';
import type { AvailableIssue, Project } from '@/types/api';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Label } from '@/components/ui/label';
import { CLI_TYPES, DEFAULT_CLI_TYPE, cliTypeForArrowKey, type CliType } from '@/lib/cli-types';
import { toast } from 'sonner';

interface NewWorkspaceDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultIssueId?: string;
  defaultWorkspaceName?: string;
}

/**
 * Create-workspace dialog.
 *
 * A workspace is ALWAYS bound to an issue. The flow therefore asks for the
 * project first (owner → project → issue): pick an existing issue, or leave it
 * empty and type a name — that name becomes a new issue created in the
 * project's first column. Either way the server runs the SAME creation path as
 * "create workspace from an issue", so the project's default repositories,
 * scenes and provider binding are inherited automatically (no per-dialog repo
 * picker, no no-repo switch).
 */
export function NewWorkspaceDialog({ open, onOpenChange, defaultIssueId, defaultWorkspaceName }: NewWorkspaceDialogProps) {
  const { t } = useTranslation('workspaces');
  const [issueId, setIssueId] = useState(defaultIssueId || '');
  const [issueName, setIssueName] = useState('');
  const [issuePickerOpen, setIssuePickerOpen] = useState(false);
  const [issueSearch, setIssueSearch] = useState('');
  const [projectId, setProjectId] = useState<string>('');
  const [workspaceName, setWorkspaceName] = useState(defaultWorkspaceName || '');
  const [cliType, setCliType] = useState<CliType>(DEFAULT_CLI_TYPE);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const queryClient = useQueryClient();

  const { data: projects = [] } = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active' } }),
    enabled: open,
  });

  // Only issues that can take a workspace (unfinished, unlinked) are offered.
  const { data: availableIssues = [] } = useQuery({
    queryKey: ['workspace-available-issues'],
    queryFn: () => api.listAvailableIssuesForWorkspace(),
    enabled: open,
  });

  const projectIssues = useMemo(
    () => availableIssues.filter((i) => String(i.project_id) === projectId),
    [availableIssues, projectId],
  );

  // What the new workspace will inherit. Informational: the server resolves
  // the same list at create time.
  const { data: projectDefaults } = useQuery({
    queryKey: ['workspace-project-defaults', projectId],
    queryFn: () => api.getProjectWorkspaceDefaults(projectId),
    enabled: open && !!projectId,
    staleTime: Infinity,
  });

  // Pre-selected issue (opened from an issue panel): derive its project so the
  // dialog lands on the right project without an extra click.
  useEffect(() => {
    if (!open || !issueId || projectId) return;
    const found = availableIssues.find((i) => String(i.id) === issueId);
    if (found) {
      setProjectId(String(found.project_id));
      setIssueName(`#${found.id} ${found.title}`);
      if (!workspaceName) setWorkspaceName(found.title);
    }
  }, [open, issueId, projectId, availableIssues, workspaceName]);

  useEffect(() => {
    if (!open) {
      setIssueId(defaultIssueId || '');
      setIssueName('');
      setIssueSearch('');
      setIssuePickerOpen(false);
      setProjectId('');
      setWorkspaceName(defaultWorkspaceName || '');
      setCliType(DEFAULT_CLI_TYPE);
      setOwner({ type: 'user', id: userId });
    }
  }, [open, defaultIssueId, defaultWorkspaceName, userId]);

  const handleSelectIssue = (issue: AvailableIssue) => {
    setIssueId(String(issue.id));
    setIssueName(`#${issue.id} ${issue.title}`);
    if (!workspaceName) setWorkspaceName(issue.title);
    setIssuePickerOpen(false);
  };

  const handleClearIssue = () => {
    setIssueId('');
    setIssueName('');
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!projectId) {
      toast.warning(t('dialogs.newWorkspace.projectRequired'));
      return;
    }
    // Without a picked issue the typed name becomes a NEW issue's title, so it
    // is required in that case; with an issue the name auto-fills and is
    // optional (the server falls back to the issue title).
    if (!issueId && !workspaceName.trim()) {
      toast.warning(t('dialogs.newWorkspace.nameRequiredForNewIssue'));
      return;
    }
    setIsSubmitting(true);
    try {
      const result = await api.createWorkspace({
        name: workspaceName.trim(),
        project_id: Number(projectId),
        ...(issueId ? { issue_id: Number(issueId) } : {}),
        // Repositories are inherited server-side from the project's defaults —
        // the same set the issue-based flow uses.
        repos: [],
        ...(owner.id > 0 ? { owner } : {}),
        cli_type: cliType,
      });
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
      queryClient.invalidateQueries({ queryKey: ['workspace-available-issues'] });
      queryClient.invalidateQueries({ queryKey: ['issues'] });
      queryClient.invalidateQueries({ queryKey: ['projects'] });
      if (result.errors && result.errors.length > 0) {
        const lines = result.errors.map((e) => `repo#${e.repository_id}: ${e.error}`);
        toast.warning(t('dialogs.newWorkspace.partialSuccess', { lines: lines.join('\n') }));
      }
      onOpenChange(false);
    } catch (error) {
      console.error('Failed to create workspace:', error);
      toast.error(t('dialogs.newWorkspace.createFailed'));
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[600px] max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>{t('dialogs.newWorkspace.title')}</DialogTitle>
          <DialogDescription>{t('dialogs.newWorkspace.description')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit}>
          <div className="grid gap-4 py-4">
            {/* CLI selector. Immutable after create. */}
            <div className="grid gap-2">
              <Label id="cliTypeLabel" className="text-sm font-medium">
                {t('dialogs.newWorkspace.cliType.label')}
              </Label>
              <div
                role="radiogroup"
                aria-labelledby="cliTypeLabel"
                className="grid grid-cols-3 gap-2"
                onKeyDown={(e) => {
                  const next = cliTypeForArrowKey(cliType, e.key);
                  if (!next) return;
                  e.preventDefault();
                  setCliType(next);
                }}
              >
                {CLI_TYPES.map((opt) => (
                  <Button
                    key={opt}
                    type="button"
                    role="radio"
                    aria-checked={cliType === opt}
                    tabIndex={cliType === opt ? 0 : -1}
                    variant={cliType === opt ? 'default' : 'outline'}
                    size="sm"
                    onClick={() => setCliType(opt)}
                    disabled={isSubmitting}
                    className="w-full min-w-0 truncate"
                  >
                    {t(`dialogs.newWorkspace.cliType.${opt}`)}
                  </Button>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">
                {t(`dialogs.newWorkspace.cliType.${cliType}Hint`)}
              </p>
            </div>

            <OwnerPicker value={owner} onChange={setOwner} userId={userId} />

            {/* Project — required: it decides the inherited repos, scenes and
                provider, and hosts the issue the workspace is bound to. */}
            <div className="grid gap-2">
              <label htmlFor="projectSelect" className="text-sm font-medium">
                {t('dialogs.newWorkspace.project')}
              </label>
              <select
                id="projectSelect"
                value={projectId}
                onChange={(e) => {
                  setProjectId(e.target.value);
                  // The issue list is project-scoped; a stale pick from another
                  // project would create a cross-project mismatch.
                  handleClearIssue();
                }}
                disabled={isSubmitting || projects.length === 0}
                className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              >
                <option value="">
                  {projects.length === 0
                    ? t('dialogs.newWorkspace.noProject')
                    : t('dialogs.newWorkspace.selectProject')}
                </option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </div>

            {/* Issue — optional. Leaving it empty creates a new issue titled
                with the workspace name. */}
            <div className="grid gap-2">
              <label className="text-sm font-medium">{t('dialogs.newWorkspace.linkIssue')}</label>
              <Popover open={issuePickerOpen} onOpenChange={setIssuePickerOpen}>
                <PopoverTrigger asChild>
                  <button
                    type="button"
                    disabled={isSubmitting || !projectId}
                    className="flex w-full items-center justify-between h-9 rounded-md border border-input bg-background px-3 py-1 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 overflow-hidden"
                  >
                    {issueName ? (
                      <span className="min-w-0 truncate text-foreground" title={issueName}>{issueName}</span>
                    ) : (
                      <span className="text-muted-foreground">{t('dialogs.newWorkspace.selectIssue')}</span>
                    )}
                    <div className="flex items-center gap-1 shrink-0 ml-2">
                      {issueName && (
                        <span
                          role="button"
                          className="text-muted-foreground hover:text-foreground"
                          onClick={(e) => { e.stopPropagation(); handleClearIssue(); }}
                        >
                          <X className="h-3.5 w-3.5" />
                        </span>
                      )}
                      <ChevronsUpDown className="h-3.5 w-3.5 text-muted-foreground" />
                    </div>
                  </button>
                </PopoverTrigger>
                <PopoverContent className="w-[var(--radix-popover-trigger-width)] p-0" align="start">
                  <div className="flex items-center border-b px-3 py-2">
                    <Search className="h-4 w-4 text-muted-foreground shrink-0 mr-2" />
                    <input
                      value={issueSearch}
                      onChange={(e) => setIssueSearch(e.target.value)}
                      placeholder={t('dialogs.newWorkspace.searchIssue')}
                      className="flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                    />
                  </div>
                  <div className="max-h-[240px] overflow-y-auto p-1">
                    {projectIssues.filter((i) =>
                      !issueSearch ||
                      i.title.toLowerCase().includes(issueSearch.toLowerCase()) ||
                      String(i.id).includes(issueSearch)
                    ).length === 0 ? (
                      <div className="py-4 text-center text-sm text-muted-foreground">
                        {t('dialogs.newWorkspace.noAvailableIssue')}
                      </div>
                    ) : (
                      projectIssues
                        .filter((i) =>
                          !issueSearch ||
                          i.title.toLowerCase().includes(issueSearch.toLowerCase()) ||
                          String(i.id).includes(issueSearch)
                        )
                        .map((issue) => (
                          <button
                            key={issue.id}
                            type="button"
                            onClick={() => handleSelectIssue(issue)}
                            className={`flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-accent hover:text-accent-foreground cursor-pointer ${
                              String(issue.id) === issueId ? 'bg-accent' : ''
                            }`}
                          >
                            <span className="text-muted-foreground shrink-0">#{issue.id}</span>
                            <span className="truncate" title={issue.title}>{issue.title}</span>
                          </button>
                        ))
                    )}
                  </div>
                </PopoverContent>
              </Popover>
              <p className="text-xs text-muted-foreground">
                {t('dialogs.newWorkspace.issueHint')}
              </p>
            </div>

            {/* Workspace name. Doubles as the NEW issue's title when no issue
                is picked, which is why it becomes required in that case. */}
            <div className="grid gap-2">
              <label htmlFor="workspaceName" className="text-sm font-medium">
                {issueId
                  ? t('dialogs.newWorkspace.name')
                  : t('dialogs.newWorkspace.nameAndNewIssue')}
              </label>
              <Input
                id="workspaceName"
                value={workspaceName}
                onChange={(e) => setWorkspaceName(e.target.value)}
                placeholder={t('dialogs.newWorkspace.namePlaceholder')}
                disabled={isSubmitting}
              />
            </div>

            {/* Inherited defaults (read-only): the same list the server applies. */}
            {projectId && (
              <div className="rounded-md border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
                <div className="flex items-center gap-1.5 font-medium text-foreground">
                  <Package className="h-3.5 w-3.5" />
                  {t('dialogs.newWorkspace.inheritTitle')}
                </div>
                <div className="mt-1">
                  {projectDefaults === undefined
                    ? t('dialogs.newWorkspace.loadingDefaults')
                    : projectDefaults.repos.length === 0
                      ? t('dialogs.newWorkspace.inheritNoRepos')
                      : t('dialogs.newWorkspace.inheritRepos', {
                          count: projectDefaults.repos.length,
                          names: projectDefaults.repos.map((r) => r.repository.name).join(', '),
                        })}
                </div>
              </div>
            )}
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
            <Button type="submit" disabled={isSubmitting || !projectId}>
              {isSubmitting ? t('dialogs.newWorkspace.submitting') : t('dialogs.newWorkspace.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
