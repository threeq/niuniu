import { useEffect, useRef, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { api } from '@/lib/api'
import { listClaudeAccounts } from '@/lib/claude-account-api'
import type { Workspace, WorkspaceRepoDetail, WorktreeGroup, Repository, CreateWorkspaceResponse, WorkspaceStatus } from '@/types/api'
import {
  Box,
  ExternalLink,
  GitBranch,
  FolderGit2,
  Circle,
  Plus,
  Loader2,
  Search,
  X,
  Archive,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { WORKSPACE_STATUS_LABELS } from '@/lib/workspace-status'
import { toast } from 'sonner'

const statusColors: Record<WorkspaceStatus, string> = {
  created: 'bg-muted-foreground/60',
  running: 'bg-green-500 dark:bg-green-400',
  needs_review: 'bg-orange-500 dark:bg-orange-400',
  attention: 'bg-destructive',
  completed: 'bg-info',
  deleting: 'bg-warning',
}

interface IssueWorkspaceProps {
  issueId: number
  issueTitle: string
}

// Per-repo selection state: repo_id -> branch
type RepoSelection = Map<number, string>

interface RepoListProps {
  filteredRepos: Repository[]
  repoSelections: RepoSelection
  repoBranches: Map<number, string[]>
  loadingBranches: Set<number>
  toggleRepo: (repo: Repository) => void
  setBranch: (repoId: number, branch: string) => void
}

function RepoList({
  filteredRepos,
  repoSelections,
  repoBranches,
  loadingBranches,
  toggleRepo,
  setBranch,
}: RepoListProps) {
  const { t } = useTranslation('projects')
  return (
    <div className="max-h-52 overflow-y-auto space-y-1.5">
      {filteredRepos.map((repo) => {
        const isSelected = repoSelections.has(repo.id)
        const branches = repoBranches.get(repo.id) ?? []
        const isLoadingBr = loadingBranches.has(repo.id)
        return (
          <div key={repo.id} className="rounded border border-border/50 px-2 py-1.5">
            <label className="flex items-center gap-2 cursor-pointer text-xs">
              <input
                type="checkbox"
                checked={isSelected}
                onChange={() => toggleRepo(repo)}
                className="rounded border-border"
              />
              <FolderGit2 className="w-3 h-3 text-muted-foreground flex-shrink-0" />
              <span className="truncate font-medium">{repo.name}</span>
            </label>
            {isSelected && (
              <div className="mt-1.5 ml-5 flex items-center gap-1.5">
                <GitBranch className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                {isLoadingBr ? (
                  <span className="flex items-center gap-1 text-[10px] text-muted-foreground">
                    <Loader2 className="w-3 h-3 animate-spin" /> {t('issue.workspace.loadingBranches')}
                  </span>
                ) : (
                  <select
                    value={repoSelections.get(repo.id) ?? ''}
                    onChange={(e) => setBranch(repo.id, e.target.value)}
                    className="flex-1 min-w-0 h-6 rounded border border-border bg-background px-1.5 text-[11px] focus:outline-none focus:ring-1 focus:ring-primary"
                  >
                    {branches.length > 0 ? (
                      branches.map(b => (
                        <option key={b} value={b}>{b}</option>
                      ))
                    ) : (
                      <option value={repoSelections.get(repo.id) ?? 'main'}>
                        {repoSelections.get(repo.id) ?? 'main'}
                      </option>
                    )}
                  </select>
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

export function IssueWorkspace({ issueId, issueTitle }: IssueWorkspaceProps) {
  const { t } = useTranslation('projects')
  const queryClient = useQueryClient()
  const [showRepoPicker, setShowRepoPicker] = useState(false)
  const [repoSelections, setRepoSelections] = useState<RepoSelection>(new Map())
  const [repoBranches, setRepoBranches] = useState<Map<number, string[]>>(new Map())
  const [loadingBranches, setLoadingBranches] = useState<Set<number>>(new Set())
  const [searchKeyword, setSearchKeyword] = useState('')
  const [claudeAccountId, setClaudeAccountId] = useState<number | null>(null)
  const [cliType, setCliType] = useState<'claude' | 'codex' | 'qwen' | 'omp'>('claude')

  const agentStatusLabels: Record<string, string> = {
    idle: t('issue.workspace.agentIdle'),
    running: t('issue.workspace.agentRunning'),
    waiting: t('issue.workspace.agentWaiting'),
    error: t('issue.workspace.agentError'),
  }
  const agentStatusColors: Record<string, string> = {
    idle: 'text-muted-foreground',
    running: 'text-green-500 dark:text-green-400',
    waiting: 'text-yellow-500 dark:text-yellow-400',
    error: 'text-destructive',
  }

  const { data: workspaces, isLoading: workspaceLoading } = useQuery({
    queryKey: ['workspace', 'by-issue', issueId],
    queryFn: () => api.get<Workspace[] | null>(`/issues/${issueId}/workspace`),
    retry: false,
  })

  const activeWorkspace = workspaces?.find((ws) => !ws.is_archived) ?? null;
  const archivedWorkspaces = workspaces?.filter((ws) => ws.is_archived) ?? [];

  // Fetch worktree groups when workspace exists
  const { data: worktreeGroups } = useQuery({
    queryKey: ['workspace-tree-groups', activeWorkspace?.id],
    queryFn: () => api.get<WorktreeGroup[]>(`/workspaces/${activeWorkspace!.id}/tree/groups`),
    enabled: !!activeWorkspace,
    retry: 1,
  })

  // Fetch repositories when workspace exists
  const { data: repos } = useQuery({
    queryKey: ['workspace-repos', activeWorkspace?.id],
    queryFn: () => api.get<WorkspaceRepoDetail[]>(`/workspaces/${activeWorkspace!.id}/repositories`),
    enabled: !!activeWorkspace,
    retry: 1,
  })

  // Fetch all repos for creating workspace
  const { data: allRepos } = useQuery({
    queryKey: ['repositories'],
    queryFn: () => api.listRepositories(),
    enabled: !activeWorkspace && !workspaceLoading,
  })

  const { data: claudeAccounts = [] } = useQuery({
    queryKey: ['claude-accounts'],
    queryFn: listClaudeAccounts,
    enabled: showRepoPicker,
  })

  // Pre-select the issue's project-linked repos when the inline picker opens.
  // Companion to NewWorkspaceDialog's identical pattern (dialogs/
  // new-workspace-dialog.tsx). staleTime:Infinity because the answer is
  // stable per (issue, server-state) within a session.
  const issueDefaultsQuery = useQuery({
    queryKey: ['workspace-issue-defaults', issueId],
    queryFn: () => api.getWorkspaceIssueDefaults(issueId),
    enabled: showRepoPicker && !!issueId,
    staleTime: Infinity,
  })

  // initializedRef guards against the late default-fetch clobbering user
  // edits. Reset on picker close so reopening reseeds. User-driven mutations
  // (toggleRepo, setBranch, removeRepo) all mark this true synchronously,
  // so a slow response after a user edit is dropped on the floor.
  const initializedRef = useRef(false)
  useEffect(() => {
    if (!showRepoPicker) {
      initializedRef.current = false
      return
    }
    if (initializedRef.current) return
    if (issueDefaultsQuery.isError) {
      initializedRef.current = true
      return
    }
    if (!issueDefaultsQuery.data) return
    const newSelections: RepoSelection = new Map()
    setRepoBranches(prev => {
      const next = new Map(prev)
      for (const r of issueDefaultsQuery.data.repos) {
        newSelections.set(r.repository.id, r.preferred_branch)
        next.set(r.repository.id, r.branches)
      }
      return next
    })
    setRepoSelections(newSelections)
    // Pre-select the project's default agent (overridable by the user before
    // submit; initializedRef ensures we only seed once per picker open).
    if (issueDefaultsQuery.data.project_default_cli_type) {
      setCliType(issueDefaultsQuery.data.project_default_cli_type)
    }
    initializedRef.current = true
  }, [showRepoPicker, issueDefaultsQuery.data, issueDefaultsQuery.isError])

  const createWorkspaceMutation = useMutation({
    mutationFn: (data: { name: string; repos: { repo_id: number; branch: string }[]; claude_account_id?: number; cli_type?: 'claude' | 'codex' | 'qwen' | 'omp' }) =>
      api.post<CreateWorkspaceResponse>(`/issues/${issueId}/workspace`, data),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ['workspace', 'by-issue', issueId] })
      queryClient.invalidateQueries({ queryKey: ['workspaces'] })
      setShowRepoPicker(false)
      setRepoSelections(new Map())
      setRepoBranches(new Map())
      setSearchKeyword('')
      setClaudeAccountId(null)
      setCliType('claude')
      // Surface partial-success: workspace shell is created but a worktree
      // failed (e.g. base branch not in repo). Without this the dialog just
      // closes and the missing worktree is only noticed via inspection.
      if (result?.errors && result.errors.length > 0) {
        const lines = result.errors.map((e) => {
          const repo = (allRepos ?? []).find((r) => String(r.id) === e.repository_id)
          return `${repo?.name ?? `repo#${e.repository_id}`}: ${e.error}`
        })
        toast.warning(t('issue.workspace.partialSuccess', { lines: lines.join('\n') }))
      }
    },
  })

  const handleShowRepoPicker = () => {
    setShowRepoPicker(true)
    setRepoSelections(new Map())
    setSearchKeyword('')
    setClaudeAccountId(null)
    setCliType('claude')
  }

  const fetchBranches = async (repoId: number) => {
    setLoadingBranches(prev => new Set(prev).add(repoId))
    try {
      const branches = await api.getRepositoryBranches(String(repoId))
      setRepoBranches(prev => new Map(prev).set(repoId, branches))
    } catch {
      setRepoBranches(prev => new Map(prev).set(repoId, []))
    } finally {
      setLoadingBranches(prev => {
        const next = new Set(prev)
        next.delete(repoId)
        return next
      })
    }
  }

  const toggleRepo = (repo: Repository) => {
    initializedRef.current = true
    setRepoSelections(prev => {
      const next = new Map(prev)
      if (next.has(repo.id)) {
        next.delete(repo.id)
      } else {
        // Initialize with the repo's recorded default. If empty (some repos
        // have no default_branch column populated), leave blank — the
        // useEffect below fills it in once branches/HEAD load. Don't
        // hardcode 'main' here, since many repos use master/develop/etc.
        // and an unresolvable ref makes the server reject the request.
        next.set(repo.id, repo.default_branch || '')
        // Fetch branches if not yet loaded
        if (!repoBranches.has(repo.id)) {
          fetchBranches(repo.id)
        }
      }
      return next
    })
  }

  // Reconcile branch selection with the loaded branch list. Fixes two cases:
  // (1) initial selection is empty (no repo.default_branch) — pick branches[0];
  // (2) initial selection is a value NOT in the loaded options (e.g. stale
  // default_branch="main" when the repo only has bbb/master) — also pick
  // branches[0]. Case (2) was the user-visible bug: the controlled <select>
  // visually showed the first option as a fallback, but state kept the
  // off-list value, so the chip + submit body sent "main" while the dropdown
  // appeared to show "bbb".
  useEffect(() => {
    setRepoSelections(prev => {
      let mutated = false
      const next = new Map(prev)
      for (const [repoId, branch] of prev.entries()) {
        const branches = repoBranches.get(repoId)
        if (!branches || branches.length === 0) continue
        if (branches.includes(branch)) continue
        next.set(repoId, branches[0])
        mutated = true
      }
      return mutated ? next : prev
    })
  }, [repoBranches])

  const setBranch = (repoId: number, branch: string) => {
    initializedRef.current = true
    setRepoSelections(prev => new Map(prev).set(repoId, branch))
  }

  const removeRepo = (repoId: number) => {
    initializedRef.current = true
    setRepoSelections(prev => { const next = new Map(prev); next.delete(repoId); return next })
  }

  const handleCreateWorkspace = () => {
    // Reject empty branches: the server requires a base branch and used to
    // silently fall back to "main" (often missing) if any was unset.
    const missing = Array.from(repoSelections.entries()).find(([, branch]) => !branch)
    if (missing) {
      const repo = (allRepos ?? []).find(r => r.id === missing[0])
      toast.warning(t('issue.workspace.selectBranch', { name: repo?.name ?? `repo#${missing[0]}` }))
      return
    }
    const repoList = Array.from(repoSelections.entries()).map(([id, branch]) => ({
      repo_id: id,
      branch,
    }))
    createWorkspaceMutation.mutate({
      name: issueTitle,
      repos: repoList,
      cli_type: cliType,
      // Codex workspaces don't use claude_account_id; skip to match
      // NewWorkspaceDialog and avoid binding a stale account.
      ...(cliType === 'claude' && claudeAccountId != null ? { claude_account_id: claudeAccountId } : {}),
    })
  }

  // Compute selected chips from repoSelections
  const selectedRepoChips = Array.from(repoSelections.entries()).map(([id, branch]) => {
    const repo = (allRepos ?? []).find(r => r.id === id)
    return { id, name: repo?.name ?? `repo-${id}`, branch }
  })

  // Filter repos by search keyword
  const filteredRepos = (allRepos ?? []).filter(r =>
    r.name.toLowerCase().includes(searchKeyword.toLowerCase())
  )

  if (workspaceLoading) {
    return (
      <div className="border-b border-border px-5 py-3">
        <div className="flex items-center gap-2 mb-2">
          <Box className="w-3.5 h-3.5 text-muted-foreground" />
          <span className="text-xs font-semibold text-muted-foreground">{t('issue.workspace.title')}</span>
        </div>
        <div className="text-xs text-muted-foreground">{t('issue.workspace.loading')}</div>
      </div>
    )
  }

  // No active workspace — show create button or repo picker
  if (!activeWorkspace) {
    return (
      <>
        <div className="border-b border-border px-5 py-3">
          <div className="flex items-center gap-2 mb-3">
            <Box className="w-3.5 h-3.5 text-muted-foreground" />
            <span className="text-xs font-semibold text-muted-foreground">{t('issue.workspace.title')}</span>
          </div>

          {!showRepoPicker ? (
            <div className="flex flex-col items-center gap-2 py-2">
              <p className="text-xs text-muted-foreground">{t('issue.workspace.notBound')}</p>
              <Button
                variant="outline"
                size="sm"
                onClick={handleShowRepoPicker}
                className="w-full"
              >
                <Plus className="w-3.5 h-3.5 mr-1.5" />
                {t('issue.workspace.create')}
              </Button>
            </div>
          ) : (
            <div className="space-y-2">
              <p className="text-xs text-muted-foreground">{t('issue.workspace.selectReposHint')}</p>
              {/* Agent CLI selector — mirrors NewWorkspaceDialog. WAI-ARIA radio
                  group pattern so screen readers see a single-select, not two
                  independent toggles. Codex hides the Claude account picker
                  since codex uses its own ~/.codex/auth.json. */}
              <div
                role="radiogroup"
                aria-label={t('issue.workspace.cliType.label')}
                className="grid gap-1.5"
                onKeyDown={(e) => {
                  const order: Array<'claude' | 'codex' | 'qwen' | 'omp'> = ['claude', 'codex', 'qwen', 'omp']
                  const i = order.indexOf(cliType)
                  if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
                    e.preventDefault()
                    setCliType(order[(i + 1) % order.length])
                  } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
                    e.preventDefault()
                    setCliType(order[(i - 1 + order.length) % order.length])
                  }
                }}
              >
                <span className="text-xs font-medium">{t('issue.workspace.cliType.label')}</span>
                <div className="flex gap-1.5">
                  {(['claude', 'codex', 'qwen', 'omp'] as const).map((opt) => (
                    <button
                      key={opt}
                      type="button"
                      role="radio"
                      aria-checked={cliType === opt}
                      tabIndex={cliType === opt ? 0 : -1}
                      onClick={() => setCliType(opt)}
                      className={`flex-1 h-7 rounded-md border text-xs transition-colors ${
                        cliType === opt
                          ? 'border-primary bg-primary text-primary-foreground'
                          : 'border-border bg-background text-foreground hover:bg-accent'
                      }`}
                    >
                      {t(`issue.workspace.cliType.${opt}`)}
                    </button>
                  ))}
                </div>
              </div>
              {cliType === 'claude' && claudeAccounts.filter(a => a.status === 'active').length > 0 && (
                <div className="grid gap-1.5">
                  <label htmlFor="issue-ws-claude-account" className="text-xs font-medium">
                    {t('issue.workspace.claudeAccount')}
                  </label>
                  <select
                    id="issue-ws-claude-account"
                    value={claudeAccountId ?? ''}
                    onChange={(e) => setClaudeAccountId(e.target.value ? Number(e.target.value) : null)}
                    className="h-8 rounded-md border border-input bg-background px-2 py-1 text-xs ring-offset-background focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    <option value="">{t('issue.workspace.claudeAccountDefault')}</option>
                    {claudeAccounts.filter(a => a.status === 'active').map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}{a.email ? ` (${a.email})` : ''}
                      </option>
                    ))}
                  </select>
                </div>
              )}
              {issueDefaultsQuery.isLoading && (
                <p className="text-xs text-muted-foreground">{t('issue.workspace.loadingDefaults')}</p>
              )}
              {issueDefaultsQuery.isError && (
                <p className="text-xs text-destructive">{t('issue.workspace.loadDefaultsFailed')}</p>
              )}

              {/* Search box */}
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground" />
                <input
                  className="w-full bg-background border border-border rounded px-2 py-1.5 pl-7 text-xs focus:outline-none focus:ring-1 focus:ring-primary"
                  placeholder={t('issue.workspace.searchPlaceholder')}
                  value={searchKeyword}
                  onChange={(e) => setSearchKeyword(e.target.value)}
                  autoFocus
                />
              </div>

              {/* Selected chips */}
              {selectedRepoChips.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {selectedRepoChips.map(chip => (
                    <span key={chip.id} className="inline-flex items-center gap-1 bg-primary/10 text-primary text-[11px] px-2 py-0.5 rounded-full">
                      {chip.name}/{chip.branch}
                      <button className="hover:text-destructive" onClick={() => removeRepo(chip.id)}>
                        <X className="w-3 h-3" />
                      </button>
                    </span>
                  ))}
                </div>
              )}

              {/* Repo list */}
              {!allRepos || allRepos.length === 0 ? (
                <p className="text-xs text-muted-foreground py-2 text-center">{t('issue.workspace.noRepos')}</p>
              ) : filteredRepos.length === 0 ? (
                <p className="text-xs text-muted-foreground py-2 text-center">{t('issue.workspace.noMatchingRepos')}</p>
              ) : (
                <RepoList
                  filteredRepos={filteredRepos}
                  repoSelections={repoSelections}
                  repoBranches={repoBranches}
                  loadingBranches={loadingBranches}
                  toggleRepo={toggleRepo}
                  setBranch={setBranch}
                />
              )}

              <div className="flex gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => { setShowRepoPicker(false); setRepoSelections(new Map()); setRepoBranches(new Map()); setSearchKeyword(''); setClaudeAccountId(null); setCliType('claude') }}
                  className="flex-1"
                >
                  {t('issue.workspace.cancel')}
                </Button>
                <Button
                  size="sm"
                  onClick={handleCreateWorkspace}
                  disabled={createWorkspaceMutation.isPending || issueDefaultsQuery.isLoading}
                  className="flex-1"
                >
                  {createWorkspaceMutation.isPending ? (
                    <Loader2 className="w-3.5 h-3.5 mr-1.5 animate-spin" />
                  ) : null}
                  {t('issue.workspace.createWithCount')}{repoSelections.size > 0 ? t('issue.workspace.createWithCountSuffix', { count: repoSelections.size }) : ''}
                </Button>
              </div>
            </div>
          )}
        </div>

        {archivedWorkspaces.length > 0 && archivedWorkspaces.map((aws) => (
          <div key={aws.id} className="border-b border-border px-5 py-3 opacity-75">
            <div className="rounded-md border border-border bg-muted/20 p-3 space-y-2">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium text-muted-foreground truncate mr-2">{aws.name}</span>
                <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground px-1.5 py-0.5 rounded bg-muted">
                  <Archive className="w-2.5 h-2.5" />
                  {t('issue.workspace.archived')}
                </span>
              </div>
              {aws.worktrees && aws.worktrees.length > 0 && (
                <div className="space-y-1">
                  {aws.worktrees.map((wt, idx) => (
                    <div key={idx} className="flex items-center gap-1.5 text-xs text-muted-foreground">
                      <GitBranch className="w-3 h-3 flex-shrink-0" />
                      <span className="font-mono text-[11px]">{wt.branch}</span>
                      <span className="text-[11px]">← {wt.base_branch}</span>
                    </div>
                  ))}
                </div>
              )}
              {aws.archived_at && (
                <div className="text-[10px] text-muted-foreground">
                  {t('issue.workspace.archivedAt', { date: new Date(aws.archived_at).toLocaleDateString('zh-CN') })}
                </div>
              )}
            </div>
          </div>
        ))}
      </>
    )
  }

  const wsStatus = activeWorkspace.status as WorkspaceStatus
  const statusColor = statusColors[wsStatus] ?? statusColors.created
  const statusLabel = WORKSPACE_STATUS_LABELS[wsStatus] ?? wsStatus
  const agentLabel = agentStatusLabels[activeWorkspace.agent_status] ?? agentStatusLabels.idle
  const agentColor = agentStatusColors[activeWorkspace.agent_status] ?? agentStatusColors.idle

  return (
    <>
      <div className="border-b border-border px-5 py-3">
        {/* Section header */}
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-2">
            <Box className="w-3.5 h-3.5 text-muted-foreground" />
            <span className="text-xs font-semibold text-muted-foreground">{t('issue.workspace.title')}</span>
          </div>
          <a
            href={`/workspaces/${activeWorkspace.id}`}
            className="flex items-center gap-1 text-xs text-primary hover:underline"
          >
            {t('issue.workspace.open')}
            <ExternalLink className="w-3 h-3" />
          </a>
        </div>

        {/* Workspace info card */}
        <div className="rounded-md border border-border bg-muted/30 p-3 space-y-2.5">
          {/* Name & status */}
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium truncate mr-2">{activeWorkspace.name}</span>
            <span className={`inline-flex items-center gap-1 text-[10px] text-white px-1.5 py-0.5 rounded ${statusColor}`}>
              <Circle className="w-1.5 h-1.5 fill-current" />
              {statusLabel}
            </span>
          </div>

          {/* Agent status */}
          <div className="flex items-center gap-3 text-xs text-muted-foreground">
            <span className="flex items-center gap-1">
              <Circle className={`w-2 h-2 fill-current ${agentColor}`} />
              {t('issue.workspace.agent')}: {agentLabel}
            </span>
          </div>

          {/* Worktrees */}
          {worktreeGroups && worktreeGroups.length > 0 && (
            <div className="pt-1 border-t border-border/50">
              <span className="text-[10px] font-medium text-muted-foreground uppercase tracking-wide">Worktrees</span>
              <div className="mt-1 space-y-1">
                {worktreeGroups.map((wt) => (
                  <div key={wt.name} className="flex items-center gap-1.5 text-xs text-foreground/80">
                    <FolderGit2 className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                    <span className="truncate">{wt.name}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Repositories */}
          {repos && repos.length > 0 && (
            <div className="pt-1 border-t border-border/50">
              <span className="text-[10px] font-medium text-muted-foreground uppercase tracking-wide">{t('issue.workspace.repos')}</span>
              <div className="mt-1 space-y-1">
                {repos.map((repo) => (
                  <div key={repo.id} className="flex items-center gap-1.5 text-xs text-foreground/80">
                    <GitBranch className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                    <span className="truncate">{repo.repository?.name ?? `repo-${repo.repository_id}`}</span>
                    <span className="text-[10px] text-muted-foreground ml-auto flex-shrink-0">{repo.branch}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {archivedWorkspaces.length > 0 && archivedWorkspaces.map((aws) => (
        <div key={aws.id} className="border-b border-border px-5 py-3 opacity-75">
          <div className="rounded-md border border-border bg-muted/20 p-3 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-muted-foreground truncate mr-2">{aws.name}</span>
              <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground px-1.5 py-0.5 rounded bg-muted">
                <Archive className="w-2.5 h-2.5" />
                {t('issue.workspace.archived')}
              </span>
            </div>
            {aws.worktrees && aws.worktrees.length > 0 && (
              <div className="space-y-1">
                {aws.worktrees.map((wt, idx) => (
                  <div key={idx} className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <GitBranch className="w-3 h-3 flex-shrink-0" />
                    <span className="font-mono text-[11px]">{wt.branch}</span>
                    <span className="text-[11px]">← {wt.base_branch}</span>
                  </div>
                ))}
              </div>
            )}
            {aws.archived_at && (
              <div className="text-[10px] text-muted-foreground">
                {t('issue.workspace.archivedAt', { date: new Date(aws.archived_at).toLocaleDateString('zh-CN') })}
              </div>
            )}
          </div>
        </div>
      ))}
    </>
  )
}
