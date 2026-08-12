import { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ChevronsUpDown, X, Search, Plug, AlertTriangle, Loader2 } from 'lucide-react';
import { api, mcpApi, ApiError } from '@/lib/api';
import { DirectoryBrowser } from './directory-browser';
import { useAuthStore } from '@/stores/auth-store';
import { OwnerPicker } from '@/components/shared/owner-picker';
import type { OwnerRef } from '@/types/org';
import type { Repository, AvailableIssue, MCPDetectResult } from '@/types/api';
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
import { Checkbox } from '@/components/ui/checkbox';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Label } from '@/components/ui/label';
import { toast } from 'sonner';

interface NewWorkspaceDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultIssueId?: string;
  defaultWorkspaceName?: string;
}

interface SelectedRepo {
  repository: Repository;
  branch: string;
  branches: string[];
}

export function NewWorkspaceDialog({ open, onOpenChange, defaultIssueId, defaultWorkspaceName }: NewWorkspaceDialogProps) {
  const { t } = useTranslation('workspaces');
  // Repository source toggle (within the repo section): add an
  // already-registered repository, or browse a local directory to
  // register/reuse one inline. Issue selection is kept as its own independent
  // optional section above — repo and issue are separate concerns.
  const [repoSource, setRepoSource] = useState<'existing' | 'directory'>('existing');
  const [dirPath, setDirPath] = useState('');
  const [dirPathValid, setDirPathValid] = useState(true);
  const [dirAddError, setDirAddError] = useState('');
  const [dirAdding, setDirAdding] = useState(false);
  const [issueId, setIssueId] = useState(defaultIssueId || '');
  const [issueName, setIssueName] = useState('');
  const [issuePickerOpen, setIssuePickerOpen] = useState(false);
  const [issueSearch, setIssueSearch] = useState('');
  const [workspaceName, setWorkspaceName] = useState(defaultWorkspaceName || '');
  const [selectedRepos, setSelectedRepos] = useState<SelectedRepo[]>([]);
  // No-repo mode: create a plain owner-isolated directory with no git
  // worktrees (office / non-code tasks). Hides the repo picker and ships
  // no_repo=true with an empty repos list.
  const [noRepo, setNoRepo] = useState(false);
  const [selectedRepoId, setSelectedRepoId] = useState<number | ''>('');
  // cliType chooses the agent CLI for the workspace. Immutable after create.
  // Codex workspaces skip the Claude account picker since codex has its own
  // ~/.codex/auth.json (M2 will introduce a codex_accounts table).
  const [cliType, setCliType] = useState<'claude' | 'codex' | 'qwen' | 'omp' | 'goose'>('claude');
  const [isSubmitting, setIsSubmitting] = useState(false);
  // MCP picker state — per-workspace MCP config (spec
  // docs/superpowers/specs/2026-05-17-per-workspace-mcp-config-design.md §7).
  // mcpDetect=null until both a Claude account AND at least one repo are
  // selected; we then call /api/workspaces/mcp/detect (debounced via the
  // single useEffect below) and pre-check `recommended` into mcpServers.
  // The user can override by toggling checkboxes; final selection ships in
  // CreateWorkspaceRequest.mcp_servers.
  const [mcpServers, setMcpServers] = useState<string[]>([]);
  const [mcpDetect, setMcpDetect] = useState<MCPDetectResult | null>(null);
  const [mcpLoading, setMcpLoading] = useState(false);
  // Track the last detect "key" so we don't refire on identical state and so
  // the user's manual edits aren't clobbered by a no-op re-detect.
  const lastMcpDetectKeyRef = useRef<string>('');
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const queryClient = useQueryClient();
  const searchInputRef = useRef<HTMLInputElement>(null);
  const initializedRef = useRef(false);

  const { data: allRepositories = [] } = useQuery({
    queryKey: ['repositories'],
    queryFn: () => api.listRepositories(),
    enabled: open,
  });


  const { data: availableIssues = [] } = useQuery({
    queryKey: ['workspace-available-issues'],
    queryFn: () => api.listAvailableIssuesForWorkspace(),
    enabled: open,
  });

  // The trigger for default-repo seeding is the issue selected INSIDE the
  // dialog (via the 关联 Issue picker), NOT the optional defaultIssueId prop.
  // The prop only feeds the initial state at mount. Most call sites open this
  // dialog without an issue context (project header / standalone "+" entry),
  // so the user's act of picking an issue from the dropdown is the natural
  // moment to fan out and pre-check the project's bound repos.
  const issueDefaultsQuery = useQuery({
    queryKey: ['workspace-issue-defaults', issueId],
    queryFn: () => api.getWorkspaceIssueDefaults(issueId),
    enabled: open && !!issueId,
    staleTime: Infinity,
  });

  // Track which issueId the most recent seed was for. When the user picks a
  // different issue inside the dialog, re-arm so the new project's repos
  // populate selectedRepos (overwriting whatever was seeded before).
  // Note: there's also an existing close-side reset effect (search for
  // `if (!open)` on selectedRepos) that runs in declaration order after
  // this one. That effect's !open-only gate must be preserved — if it ever
  // runs on `open=true`, it will race with this effect and clobber the
  // just-seeded selectedRepos.
  // null = no successful seed yet. Distinguishing "never seeded" from
  // "seeded with empty string" matters: the clobber-prevention path runs
  // before the first seed completes, and we must NOT re-arm on every
  // pre-seed re-render or the user's `initializedRef.current=true` mark
  // (set by handleAddRepo etc.) gets clobbered by the late default arrival.
  const lastSeededIssueIdRef = useRef<string | null>(null);
  useEffect(() => {
    if (!open) {
      initializedRef.current = false; // reset so reopens re-seed
      lastSeededIssueIdRef.current = null;
      return;
    }
    // Only re-arm when a previous seed exists AND the issue truly changed.
    if (lastSeededIssueIdRef.current !== null && issueId !== lastSeededIssueIdRef.current) {
      initializedRef.current = false;
    }
    if (initializedRef.current) return;
    if (!issueId) return; // No issue selected → wait. Don't mark initialized;
                           // we want to seed once an issue gets picked.
    if (issueDefaultsQuery.isError) {
      initializedRef.current = true;
      lastSeededIssueIdRef.current = issueId;
      return;
    }
    if (!issueDefaultsQuery.data) return; // wait for fetch
    setSelectedRepos(
      issueDefaultsQuery.data.repos.map(r => ({
        repository: r.repository,
        branch: r.preferred_branch,
        branches: r.branches,
      }))
    );
    initializedRef.current = true;
    lastSeededIssueIdRef.current = issueId;
  }, [open, issueId, issueDefaultsQuery.data, issueDefaultsQuery.isError]);

  // MCP detection — fires whenever the user picks a Claude account AND has
  // at least one repo selected. The detect-key guard avoids refiring when the
  // selection hasn't materially changed (e.g. branch flip on an already-
  // selected repo). Errors are non-fatal: we just leave mcpDetect=null so the
  // section degrades to the "no servers" empty state.
  useEffect(() => {
    if (!open) {
      setMcpDetect(null);
      setMcpServers([]);
      setMcpLoading(false);
      lastMcpDetectKeyRef.current = '';
      return;
    }
    if (selectedRepos.length === 0) {
      setMcpDetect(null);
      setMcpServers([]);
      lastMcpDetectKeyRef.current = '';
      return;
    }
    const repoIds = selectedRepos.map((sr) => sr.repository.id).sort((a, b) => a - b);
    const key = repoIds.join(',');
    if (key === lastMcpDetectKeyRef.current) return;
    lastMcpDetectKeyRef.current = key;
    let cancelled = false;
    setMcpLoading(true);
    mcpApi
      .detect({ repo_ids: repoIds })
      .then((result) => {
        if (cancelled) return;
        setMcpDetect(result);
        setMcpServers(result.recommended ?? []);
      })
      .catch(() => {
        if (cancelled) return;
        setMcpDetect(null);
      })
      .finally(() => {
        if (!cancelled) setMcpLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, selectedRepos]);

  const toggleMcpServer = (name: string) => {
    setMcpServers((prev) =>
      prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]
    );
  };

  // Group issues by project, filtered by search
  const groupedIssues = useMemo(() => {
    let list = availableIssues;
    if (issueSearch) {
      const q = issueSearch.toLowerCase();
      list = list.filter(
        (i) => i.title.toLowerCase().includes(q) || String(i.id).includes(q) || i.project_name.toLowerCase().includes(q)
      );
    }
    const groups = new Map<string, AvailableIssue[]>();
    for (const issue of list) {
      const key = issue.project_name || t('dialogs.newWorkspace.unassignedProject');
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key)!.push(issue);
    }
    return groups;
  }, [availableIssues, issueSearch, t]);

  // Sync selected issue name when availableIssues load
  useEffect(() => {
    if (issueId && availableIssues.length > 0) {
      const found = availableIssues.find((i) => String(i.id) === issueId);
      if (found) setIssueName(`#${found.id} ${found.title}`);
    }
  }, [issueId, availableIssues]);

  useEffect(() => {
    if (!open) {
      setIssueId(defaultIssueId || '');
      setIssueName('');
      setIssueSearch('');
      setIssuePickerOpen(false);
      setWorkspaceName('');
      setSelectedRepos([]);
      setSelectedRepoId('');
      setOwner({ type: 'user', id: userId });
      setRepoSource('existing');
      setDirPath('');
      setDirAddError('');
      setDirAdding(false);
    }
  }, [open, defaultIssueId, userId]);

  useEffect(() => {
    if (defaultWorkspaceName) {
      setWorkspaceName(defaultWorkspaceName);
    }
  }, [defaultWorkspaceName]);

  // Focus search input when picker opens
  useEffect(() => {
    if (issuePickerOpen) {
      setTimeout(() => searchInputRef.current?.focus(), 0);
    } else {
      setIssueSearch('');
    }
  }, [issuePickerOpen]);

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

  const availableRepos = allRepositories.filter(
    (repo) => !selectedRepos.some((sr) => sr.repository.id === repo.id)
  );

  const handleAddRepo = () => {
    initializedRef.current = true;
    if (!selectedRepoId) return;
    const repo = allRepositories.find((r) => r.id === selectedRepoId);
    if (!repo) return;
    api.getRepositoryBranches(String(selectedRepoId)).then(branches => {
      // Pick a default that's actually in the dropdown options. A stale
      // repo.default_branch (e.g. "main" on a repo that only has master/bbb)
      // would otherwise leave the controlled <select> with a value that
      // isn't in its options — the browser shows the first option visually
      // but state keeps the off-list value, so submission silently sends
      // the wrong branch.
      const preferred = repo.default_branch && branches.includes(repo.default_branch)
        ? repo.default_branch
        : branches[0] ?? '';
      setSelectedRepos([
        ...selectedRepos,
        {
          repository: repo,
          branch: preferred,
          branches: branches,
        },
      ]);
    });
    setSelectedRepoId('');
  };

  const handleRemoveRepo = (repoId: number) => {
    initializedRef.current = true;
    setSelectedRepos(selectedRepos.filter((sr) => sr.repository.id !== repoId));
  };

  // Normalize a path for cross-platform comparison: forward slashes, no
  // trailing separator, lowercased (Windows paths are case-insensitive and the
  // directory browser hands us forward-slash, mixed-case strings).
  const normalizePath = (p: string) =>
    p.trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase();

  // Fetch branches for a repo and append it to the selected list (shared by the
  // existing-repo and directory sources). No-op if already selected.
  const addRepoToSelection = (repo: Repository) => {
    initializedRef.current = true;
    if (selectedRepos.some((sr) => sr.repository.id === repo.id)) return;
    api.getRepositoryBranches(String(repo.id)).then((branches) => {
      const preferred =
        repo.default_branch && branches.includes(repo.default_branch)
          ? repo.default_branch
          : branches[0] ?? '';
      setSelectedRepos((prev) => [
        ...prev,
        { repository: repo, branch: preferred, branches },
      ]);
    });
  };

  // Directory source: register (or reuse) the selected local directory as a
  // repository, then add it to the selection. Reusing an already-registered
  // path avoids the REPO_NAME_EXISTS that a blind create would hit.
  const handleAddDirectory = async () => {
    const dir = dirPath.trim();
    if (!dir) {
      setDirAddError(t('dialogs.newWorkspace.fromDir.missingDir'));
      return;
    }
    setDirAddError('');
    const norm = normalizePath(dir);
    const existing = allRepositories.find((r) => normalizePath(r.path) === norm);
    if (existing) {
      addRepoToSelection(existing);
      setDirPath('');
      return;
    }
    setDirAdding(true);
    try {
      const repo = await api.createRepository({
        path: dir,
        auto_init: true,
        ...(owner.id > 0 ? { owner } : {}),
      });
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
      addRepoToSelection(repo);
      setDirPath('');
    } catch (error) {
      if (error instanceof ApiError && error.code === 'GIT_IDENTITY_MISSING') {
        setDirAddError(t('dialogs.newWorkspace.fromDir.identityMissing'));
      } else if (error instanceof ApiError && error.code === 'REPO_NAME_EXISTS') {
        setDirAddError(t('dialogs.newWorkspace.repoSource.nameExists'));
      } else if (error instanceof ApiError && error.code === 'NOT_A_GIT_REPO') {
        setDirAddError(t('dialogs.newWorkspace.repoSource.notGitRepo'));
      } else {
        console.error('Failed to add repository from directory:', error);
        setDirAddError(t('dialogs.newWorkspace.repoSource.addFailed'));
      }
    } finally {
      setDirAdding(false);
    }
  };

  const handleRepoBranchChange = (repoId: number, branch: string) => {
    initializedRef.current = true;
    setSelectedRepos(
      selectedRepos.map((sr) =>
        sr.repository.id === repoId ? { ...sr, branch } : sr
      )
    );
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    // Reject empty branches up-front so we never silently send a request that
    // the server would have to guess a default for (the old "fall back to main"
    // behavior left workspaces with no worktree on disk when main didn't exist).
    // No-repo workspaces have no branches to validate.
    if (!noRepo) {
      const missingBranch = selectedRepos.find((sr) => !sr.branch);
      if (missingBranch) {
        toast.warning(t('dialogs.newWorkspace.missingBranch', { name: missingBranch.repository.name }));
        return;
      }
    }
    setIsSubmitting(true);
    try {
      const repos = noRepo
        ? []
        : selectedRepos.map((sr) => ({
            repo_id: sr.repository.id,
            branch: sr.branch,
          }));
      // Omit `owner` when id is 0 (personal-edition fallback when SPA has no
      // currentUser): handler defaults to `{type:'user', id: caller}`, which
      // is what we actually want. Sending {id: 0} is rejected by
      // EnsureOwnerWritable because 0 != caller's real id.
      const result = await api.createWorkspace({
        name: workspaceName.trim(),
        issue_id: issueId ? parseInt(issueId) : null,
        repos,
        ...(owner.id > 0 ? { owner } : {}),
        // Only ship mcp_servers when the user actually saw the picker and
        // detect resolved — sending [] when the panel never rendered would
        // suppress the backend's auto-detect fallback.
        ...(mcpDetect ? { mcp_servers: mcpServers } : {}),
        cli_type: cliType,
        ...(noRepo ? { no_repo: true } : {}),
      });
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
      queryClient.invalidateQueries({ queryKey: ['workspace-available-issues'] });
      if (issueId) {
        queryClient.invalidateQueries({ queryKey: ['issue-workspace', issueId] });
      }
      // Surface partial-success: workspace shell exists but some worktrees
      // failed. Without this, users saw a "created" dialog close and only
      // discovered the missing worktree by inspecting the workspace card.
      if (result.errors && result.errors.length > 0) {
        const lines = result.errors.map((e) => {
          const repo = selectedRepos.find((sr) => String(sr.repository.id) === e.repository_id);
          const name = repo ? repo.repository.name : `repo#${e.repository_id}`;
          return `${name}: ${e.error}`;
        });
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
          <DialogDescription>
            {t('dialogs.newWorkspace.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit}>
          <div className="grid gap-4 py-4">
            {/* CLI selector. Immutable after create.
                Implements the WAI-ARIA radio group pattern with shadcn
                <Button> elements re-roled as radios — keeps the design
                system's button visual + variants while giving assistive
                tech the correct single-select semantics (role=radio +
                aria-checked, not toggle's aria-pressed). Roving tabindex
                (selected button = 0, other = -1) plus arrow-key handler
                make keyboard navigation match native radio groups. */}
            <div className="grid gap-2">
              <Label id="cliTypeLabel" className="text-sm font-medium">
                {t('dialogs.newWorkspace.cliType.label')}
              </Label>
              <div
                role="radiogroup"
                aria-labelledby="cliTypeLabel"
                className="flex gap-2"
                onKeyDown={(e) => {
                  const order: Array<'claude' | 'codex' | 'qwen' | 'omp' | 'goose'> = ['claude', 'codex', 'qwen', 'goose', 'omp'];
                  const i = order.indexOf(cliType);
                  if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
                    e.preventDefault();
                    setCliType(order[(i + 1) % order.length]);
                  } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
                    e.preventDefault();
                    setCliType(order[(i - 1 + order.length) % order.length]);
                  }
                }}
              >
                {(['claude', 'codex', 'qwen', 'goose', 'omp'] as const).map((opt) => (
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
                    className="flex-1"
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
            {/* Workspace Name */}
            <div className="grid gap-2">
              <label htmlFor="workspaceName" className="text-sm font-medium">
                {t('dialogs.newWorkspace.name')}
              </label>
              <Input
                id="workspaceName"
                value={workspaceName}
                onChange={(e) => setWorkspaceName(e.target.value)}
                placeholder={t('dialogs.newWorkspace.namePlaceholder')}
                disabled={isSubmitting}
              />
            </div>

            {/* Issue selection — an independent, optional section. Repo and
                issue are separate concerns. */}
            <div className="grid gap-2">
              <label className="text-sm font-medium">{t('dialogs.newWorkspace.linkIssue')}</label>
              <Popover open={issuePickerOpen} onOpenChange={setIssuePickerOpen}>
                <PopoverTrigger asChild>
                  <button
                    type="button"
                    disabled={isSubmitting}
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
                      ref={searchInputRef}
                      value={issueSearch}
                      onChange={(e) => setIssueSearch(e.target.value)}
                      placeholder={t('dialogs.newWorkspace.searchIssue')}
                      className="flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                    />
                  </div>
                  <div className="max-h-[240px] overflow-y-auto p-1">
                    {groupedIssues.size === 0 ? (
                      <div className="py-4 text-center text-sm text-muted-foreground">
                        {t('dialogs.newWorkspace.noAvailableIssue')}
                      </div>
                    ) : (
                      Array.from(groupedIssues.entries()).map(([projectName, issues]) => (
                        <div key={projectName}>
                          <div className="px-2 py-1.5 text-xs font-semibold text-muted-foreground">
                            {projectName}
                          </div>
                          {issues.map((issue) => (
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
                          ))}
                        </div>
                      ))
                    )}
                  </div>
                </PopoverContent>
              </Popover>
              <p className="text-xs text-muted-foreground">
                {t('dialogs.newWorkspace.issueHint')}
              </p>
            </div>

            {/* No-repo mode toggle: a plain owner-isolated directory with no
                git worktrees, for office / non-code tasks. */}
            <label className="flex items-start gap-2 cursor-pointer">
              <Checkbox
                checked={noRepo}
                onCheckedChange={(v) => {
                  const on = v === true;
                  setNoRepo(on);
                  if (on) setSelectedRepos([]);
                }}
                disabled={isSubmitting}
                className="mt-0.5"
              />
              <span className="grid gap-0.5">
                <span className="text-sm font-medium">
                  {t('dialogs.newWorkspace.noRepo.label')}
                </span>
                <span className="text-xs text-muted-foreground">
                  {t('dialogs.newWorkspace.noRepo.hint')}
                </span>
              </span>
            </label>

            {/* Repository selection — an independent section with a source
                toggle: pick an already-registered repo, or browse a local
                directory to register/reuse one inline. */}
            {!noRepo && (
            <div className="grid gap-2">
              <Label id="repoSourceLabel" className="text-sm font-medium">
                {t('dialogs.newWorkspace.addRepo')}
              </Label>
              <div role="radiogroup" aria-labelledby="repoSourceLabel" className="flex gap-2">
                <Button
                  type="button"
                  role="radio"
                  aria-checked={repoSource === 'existing'}
                  tabIndex={repoSource === 'existing' ? 0 : -1}
                  variant={repoSource === 'existing' ? 'default' : 'outline'}
                  size="sm"
                  onClick={() => { setRepoSource('existing'); setDirAddError(''); }}
                  disabled={isSubmitting}
                  className="flex-1"
                >
                  {t('dialogs.newWorkspace.repoSource.existing')}
                </Button>
                <Button
                  type="button"
                  role="radio"
                  aria-checked={repoSource === 'directory'}
                  tabIndex={repoSource === 'directory' ? 0 : -1}
                  variant={repoSource === 'directory' ? 'default' : 'outline'}
                  size="sm"
                  onClick={() => setRepoSource('directory')}
                  disabled={isSubmitting}
                  className="flex-1"
                >
                  {t('dialogs.newWorkspace.repoSource.directory')}
                </Button>
              </div>
              {issueId && issueDefaultsQuery.isLoading && (
                <div className="text-xs text-muted-foreground">
                  {t('dialogs.newWorkspace.loadingDefaults')}
                </div>
              )}
              {issueId && issueDefaultsQuery.isError && (
                <div className="text-xs text-destructive">
                  {t('dialogs.newWorkspace.loadDefaultsFailed')}
                </div>
              )}
              {repoSource === 'existing' ? (
                <div className="flex gap-2">
                  <select
                    id="repoSelect"
                    value={selectedRepoId}
                    onChange={(e) => setSelectedRepoId(e.target.value ? Number(e.target.value) : '')}
                    disabled={isSubmitting || availableRepos.length === 0}
                    className="flex-1 h-9 rounded-md border border-input bg-background px-3 py-1 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    <option value="">
                      {availableRepos.length === 0
                        ? t('dialogs.newWorkspace.allReposAdded')
                        : t('dialogs.newWorkspace.selectRepo')}
                    </option>
                    {availableRepos.map((repo) => (
                      <option key={repo.id} value={repo.id}>
                        {repo.name}
                      </option>
                    ))}
                  </select>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={handleAddRepo}
                    disabled={!selectedRepoId || isSubmitting}
                  >
                    {t('common:actions.add')}
                  </Button>
                </div>
              ) : (
                <div className="grid gap-2">
                  <DirectoryBrowser
                    value={dirPath}
                    onChange={(p) => { setDirPath(p); setDirAddError(''); }}
                    disabled={isSubmitting || dirAdding}
                    error={dirAddError || undefined}
                    onValidityChange={setDirPathValid}
                  />
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-xs text-muted-foreground">
                      {t('dialogs.newWorkspace.repoSource.directoryHint')}
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={handleAddDirectory}
                      disabled={!dirPath.trim() || !dirPathValid || isSubmitting || dirAdding}
                      className="shrink-0"
                    >
                      {dirAdding
                        ? t('dialogs.newWorkspace.repoSource.adding')
                        : t('common:actions.add')}
                    </Button>
                  </div>
                </div>
              )}
            </div>
            )}

            {/* Selected Repos List */}
            {selectedRepos.length > 0 && (
              <div className="grid gap-2">
                <label className="text-sm font-medium">
                  {t('dialogs.newWorkspace.selectedRepos', { count: selectedRepos.length })}
                </label>
                <div className="border rounded-md divide-y">
                  {selectedRepos.map((sr) => (
                    <div
                      key={sr.repository.id}
                      className="flex items-center gap-2 p-2"
                    >
                      <div className="flex-1 min-w-0 overflow-hidden">
                        <p data-testid="selected-repo-name" className="text-sm font-medium truncate" title={sr.repository.name}>
                          {sr.repository.name}
                        </p>
                        <p className="text-xs text-muted-foreground truncate" title={sr.repository.path}>
                          {sr.repository.path}
                        </p>
                      </div>
                      <select
                        value={sr.branch}
                        onChange={(e) =>
                          handleRepoBranchChange(sr.repository.id, e.target.value)
                        }
                        disabled={isSubmitting}
                        className="h-8 w-36 shrink-0 text-xs rounded border border-input bg-background px-2 py-1"
                      >
                        {sr.branches.map((branch) => (
                          <option key={branch} value={branch}>
                            {branch}
                          </option>
                        ))}
                      </select>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => handleRemoveRepo(sr.repository.id)}
                        disabled={isSubmitting}
                        className="shrink-0 text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                      >
                        {t('common:actions.delete')}
                      </Button>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* MCP picker — collapsible single-form section (not a wizard step).
                Renders only when the user has picked both a Claude account and
                at least one repo, since detection requires both inputs. */}
            {selectedRepos.length > 0 && (
              <div className="grid gap-2" data-testid="mcp-section">
                <Label className="text-sm font-medium flex items-center gap-1.5">
                  <Plug className="h-3.5 w-3.5 text-muted-foreground" />
                  {t('mcp.section_title')}
                </Label>
                {mcpLoading && (
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    {t('mcp.loading')}
                  </div>
                )}
                {!mcpLoading && mcpDetect && mcpDetect.plugin_conflicts && mcpDetect.plugin_conflicts.length > 0 && (
                  <Alert>
                    <AlertTriangle className="h-4 w-4" />
                    <AlertDescription className="text-xs">
                      {t('mcp.plugin_conflict.global_load')}
                    </AlertDescription>
                  </Alert>
                )}
                {!mcpLoading && mcpDetect && mcpDetect.all.length === 0 && (
                  <p className="text-xs text-muted-foreground italic">
                    {t('mcp.no_servers_available')}
                  </p>
                )}
                {!mcpLoading && mcpDetect && mcpDetect.all.length > 0 && (
                  <div className="border rounded-md divide-y">
                    {mcpDetect.all.map((m) => {
                      const checked = mcpServers.includes(m.name);
                      const recommended = mcpDetect.recommended.includes(m.name);
                      const isPlugin = m.source === 'plugin';
                      return (
                        <label
                          key={m.name}
                          className="flex items-center gap-2 p-2 cursor-pointer hover:bg-accent/40"
                        >
                          <Checkbox
                            checked={checked}
                            onCheckedChange={() => toggleMcpServer(m.name)}
                            disabled={isSubmitting}
                          />
                          <span className="flex-1 min-w-0 text-sm truncate" title={m.name}>
                            {m.name}
                          </span>
                          {recommended && (
                            <span className="shrink-0 rounded-sm bg-brand/10 px-1.5 py-0.5 text-[10px] font-medium text-brand">
                              {t('mcp.recommended_label')}
                            </span>
                          )}
                          {isPlugin && (
                            <span className="shrink-0 rounded-sm bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground" title={m.plugin_name}>
                              plugin
                            </span>
                          )}
                        </label>
                      );
                    })}
                  </div>
                )}
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
            <Button
              type="submit"
              disabled={
                isSubmitting ||
                (!!issueId && issueDefaultsQuery.isLoading)
              }
            >
              {isSubmitting ? t('dialogs.newWorkspace.submitting') : t('dialogs.newWorkspace.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
