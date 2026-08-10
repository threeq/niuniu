import { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { RefreshCw, Settings, GitBranch, FileText, File, Folder, GitBranchIcon, FolderGit2, Save, Terminal as TerminalIcon, ChevronDown, ChevronRight, Search, Globe, Monitor, Plus, Undo2, Check, AlertCircle } from 'lucide-react';
import { VSCodeIcon } from '@/components/ui/vscode-icon';
import { useParams } from '@tanstack/react-router';
import i18n from '@/i18n';
import { api } from '@/lib/api';
import { confirm } from '@/lib/confirm';
import { toast } from 'sonner';
import { FilePreviewByUrl } from '@/pages/workspaces/components/file-preview';
import { getRepoFileContentUrl } from '@/lib/repo-file-url';
import { openInVSCode } from '@/lib/vscode';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import type { Repository, RepositoryStats, FileEntry, WorktreeWithWorkspace, GraphCommit, BranchTree, CommitDetail } from '@/types/api';
import { cn } from '@/lib/utils';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { getAccessToken } from '@/stores/auth-store';
import { useThemeStore } from '@/stores/theme-store';
import { LIGHT_TERMINAL_THEME, DARK_TERMINAL_THEME } from '@/lib/terminal-themes';
import { computeGraphLayout } from '@/lib/commit-graph';
import { RepoGitIdentitySection } from './repo-git-identity-section';

type Tab = 'files' | 'branches' | 'worktrees' | 'settings';

export function RepositoryDetailPage() {
  const { t } = useTranslation('repositories');
  const params = useParams({ strict: false });
  const id = (params as Record<string, string | undefined>).id!;
  const [activeTab, setActiveTab] = useState<Tab>('branches');
  const [terminalOpen, setTerminalOpen] = useState(false);

  const tabs: { id: Tab; label: string; icon: typeof FileText }[] = [
    { id: 'files', label: t('detail.tabs.files'), icon: FileText },
    { id: 'branches', label: t('detail.tabs.branches'), icon: GitBranchIcon },
    { id: 'worktrees', label: t('detail.tabs.worktrees'), icon: FolderGit2 },
    { id: 'settings', label: t('detail.tabs.settings'), icon: Settings },
  ];

  const { data: repository, isLoading, refetch } = useQuery<Repository>({
    queryKey: ['repository', id],
    queryFn: () => api.get<Repository>(`/repositories/${id}`),
    enabled: !!id,
  });

  const { data: stats } = useQuery<RepositoryStats>({
    queryKey: ['repository', id, 'stats'],
    queryFn: () => api.get<RepositoryStats>(`/repositories/${id}/stats`),
    enabled: !!id,
  });

  const handleOpenInVSCode = () => {
    if (repository?.path) {
      openInVSCode(repository.path).catch(() => {})
    }
  };

  return (
    <div className="h-full flex flex-col bg-background">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 bg-card border-b">
        <div className="flex items-center gap-3">
          <GitBranch className="w-4 h-4 text-muted-foreground" />
          <h1 className="font-semibold text-foreground">
            {isLoading ? t('common:actions.loading') : repository?.name}
          </h1>
          {repository?.default_branch && (
            <span className="text-xs text-muted-foreground bg-muted px-2 py-0.5 rounded">
              {repository.default_branch}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {stats && (
            <div className="flex items-center gap-4 mr-2 text-xs text-muted-foreground">
              <span>{stats.total_commits} {t('detail.header.commitsSuffix')}</span>
              <span>{stats.total_branches} {t('detail.header.branchesSuffix')}</span>
              <span>{stats.total_contributors} {t('detail.header.contributorsSuffix')}</span>
            </div>
          )}
          <Button variant="ghost" size="sm" className="h-8 w-8 p-0" onClick={() => refetch()} title={t('detail.header.refresh')}>
            <RefreshCw className="w-4 h-4" />
          </Button>
          <Button variant="ghost" size="sm" className="h-8 w-8 p-0" onClick={handleOpenInVSCode} title={t('detail.header.openInVSCode')} disabled={!repository?.path}>
            <VSCodeIcon className="w-4 h-4" />
          </Button>
          <Button variant={terminalOpen ? 'default' : 'ghost'} size="sm" className="h-8 w-8 p-0" onClick={() => setTerminalOpen(!terminalOpen)} title={t('detail.header.terminal')}>
            <TerminalIcon className="w-4 h-4" />
          </Button>
        </div>
      </div>

      {/* Tab Navigation */}
      <div className="flex items-center gap-1 px-4 bg-card border-b">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              'flex items-center gap-2 px-3 py-2 text-sm font-medium border-b-2 -mb-px transition-colors',
              activeTab === tab.id
                ? 'border-info text-info'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            )}
          >
            <tab.icon className="w-4 h-4" />
            {tab.label}
          </button>
        ))}
      </div>

      {/* Tab Content + Terminal */}
      <div className="flex-1 min-h-0 flex flex-col">
        <div className="flex-1 min-h-0 overflow-hidden">
          {activeTab === 'files' && <RepoFilesTab repoId={id!} />}
          {activeTab === 'branches' && <RepoBranchesTab repoId={id!} />}
          {activeTab === 'worktrees' && <RepoWorktreesTab repoId={id!} />}
          {activeTab === 'settings' && <RepoSettingsTab repoId={id!} repository={repository} />}
        </div>
        {terminalOpen && <RepoTerminalPanel repoId={id!} onClose={() => setTerminalOpen(false)} />}
      </div>
    </div>
  );
}

// ==================== Branches Tab (3-panel: tree + graph + detail) ====================

function RepoBranchesTab({ repoId }: { repoId: string }) {
  const { t } = useTranslation('repositories');
  const queryClient = useQueryClient();
  const [selectedCommit, setSelectedCommit] = useState<string | null>(null);
  const selectedCommitRepoRef = useRef(repoId);

  useEffect(() => {
    setSelectedCommit(null);
    selectedCommitRepoRef.current = repoId;
  }, [repoId]);

  const selectCommit = (hash: string | null) => {
    setSelectedCommit(hash);
    selectedCommitRepoRef.current = repoId;
  };
  const [branchFilter, setBranchFilter] = useState('');
  const [localExpanded, setLocalExpanded] = useState(true);
  const [remoteExpanded, setRemoteExpanded] = useState(true);
  const [changesExpanded, setChangesExpanded] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [newBranchName, setNewBranchName] = useState('');
  const [isCreating, setIsCreating] = useState(false);
  const [commitMessage, setCommitMessage] = useState('');
  const [isCommitting, setIsCommitting] = useState(false);
  const [isDiscarding, setIsDiscarding] = useState(false);

  const { data: branchTree, refetch: refetchTree } = useQuery<BranchTree>({
    queryKey: ['repository', repoId, 'branch-tree'],
    queryFn: () => api.get<BranchTree>(`/repositories/${repoId}/branch-tree`),
  });

  // Fetch the FULL commit set (no limit): lanes are only topologically correct
  // when computed over the whole DAG. Rendering is virtualized below, so even
  // 10k+ commits stay smooth and the header count reflects the true total.
  const { data: graphCommits } = useQuery<GraphCommit[]>({
    queryKey: ['repository', repoId, 'graph'],
    queryFn: () => api.get<GraphCommit[]>(`/repositories/${repoId}/graph`),
  });

  const { data: commitDetail } = useQuery<CommitDetail>({
    queryKey: ['repository', repoId, 'commit-detail', selectedCommit],
    queryFn: () => api.get<CommitDetail>(`/repositories/${repoId}/commits/${selectedCommit}`),
    enabled: !!selectedCommit && selectedCommitRepoRef.current === repoId,
  });

  const { data: gitStatus, refetch: refetchStatus } = useQuery<{ modified: string[]; added: string[]; deleted: string[]; untracked: string[] }>({
    queryKey: ['repository', repoId, 'git-status'],
    queryFn: () => api.get(`/repositories/${repoId}/git/status`),
    refetchInterval: changesExpanded ? 5000 : false,
  });

  const allChanges = useMemo(() => {
    if (!gitStatus) return [];
    const files: { path: string; status: string }[] = [];
    gitStatus.modified?.forEach(p => files.push({ path: p, status: 'M' }));
    gitStatus.added?.forEach(p => files.push({ path: p, status: 'A' }));
    gitStatus.deleted?.forEach(p => files.push({ path: p, status: 'D' }));
    gitStatus.untracked?.forEach(p => files.push({ path: p, status: '?' }));
    return files;
  }, [gitStatus]);

  const handleCommitAll = async () => {
    if (!commitMessage.trim()) return;
    setIsCommitting(true);
    try {
      await api.post(`/repositories/${repoId}/commit`, { message: commitMessage });
      setCommitMessage('');
      refetchStatus();
      queryClient.invalidateQueries({ queryKey: ['repository', repoId, 'graph'] });
    } catch (err) {
      console.error('Failed to commit:', err);
    } finally {
      setIsCommitting(false);
    }
  };

  const handleDiscardAll = async () => {
    if (!(await confirm(t('detail.branches.confirmDiscardAll')))) return;
    setIsDiscarding(true);
    try {
      await api.post(`/repositories/${repoId}/discard`, {});
      refetchStatus();
    } catch (err) {
      console.error('Failed to discard:', err);
    } finally {
      setIsDiscarding(false);
    }
  };

  const handleDiscardFile = async (filePath: string) => {
    if (!(await confirm(t('detail.branches.confirmDiscardFile', { path: filePath })))) return;
    try {
      await api.post(`/repositories/${repoId}/discard-file`, { path: filePath });
      refetchStatus();
    } catch (err) {
      console.error('Failed to discard file:', err);
    }
  };

  const currentBranch = branchTree?.current_branch ?? '';
  const localBranches = (branchTree?.local_branches ?? []).filter(b => !branchFilter || b.toLowerCase().includes(branchFilter.toLowerCase()));
  const remoteBranches = (branchTree?.remote_branches ?? []).filter(b => !branchFilter || b.toLowerCase().includes(branchFilter.toLowerCase()));

  const layout = useMemo(() => graphCommits ? computeGraphLayout(graphCommits) : { nodes: [], edges: [], maxLane: 0 }, [graphCommits]);

  const ROW_HEIGHT = 28;
  const LANE_WIDTH = 16;
  const NODE_RADIUS = 4;
  const GRAPH_PADDING = 12;
  const graphWidth = Math.max(60, (layout.maxLane + 1) * LANE_WIDTH + GRAPH_PADDING * 2);

  // --- Virtualized rendering ---
  // The full DAG is computed above, but we only mount the rows/edges in (and
  // near) the viewport so the DOM stays tiny even for 10k+ commits. The scroll
  // container keeps full height; we render the window [visibleStart, visibleEnd)
  // plus an overscan buffer so edges crossing the viewport edge are not clipped.
  const graphScrollRef = useRef<HTMLDivElement>(null);
  const [graphScrollTop, setGraphScrollTop] = useState(0);
  const [graphViewportH, setGraphViewportH] = useState(0);
  useEffect(() => {
    const el = graphScrollRef.current;
    if (!el) return;
    const update = () => { setGraphScrollTop(el.scrollTop); setGraphViewportH(el.clientHeight); };
    update();
    el.addEventListener('scroll', update, { passive: true });
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => { el.removeEventListener('scroll', update); ro.disconnect(); };
  }, [layout.nodes.length]);
  const OVERSCAN = 12;
  const totalRows = layout.nodes.length;
  const visibleStart = Math.max(0, Math.floor(graphScrollTop / ROW_HEIGHT) - OVERSCAN);
  const visibleEnd = Math.min(totalRows, Math.ceil((graphScrollTop + (graphViewportH || 600)) / ROW_HEIGHT) + OVERSCAN);

  const handleCreateBranch = async () => {
    if (!newBranchName.trim()) return;
    setIsCreating(true);
    try {
      await api.post(`/repositories/${repoId}/branches`, { name: newBranchName });
      setNewBranchName('');
      setShowCreate(false);
      refetchTree();
    } catch (err) {
      console.error('Failed to create branch:', err);
    } finally {
      setIsCreating(false);
    }
  };

  const handleCheckout = async (name: string) => {
    try {
      await api.put(`/repositories/${repoId}/branches/checkout?name=${encodeURIComponent(name)}`, {});
      refetchTree();
    } catch (err) {
      console.error('Failed to checkout:', err);
    }
  };

  const handleDeleteBranch = async (name: string) => {
    if (!(await confirm(t('detail.branches.confirmDeleteBranch', { name })))) return;
    try {
      await api.delete(`/repositories/${repoId}/branches?name=${encodeURIComponent(name)}`);
      refetchTree();
    } catch (err) {
      console.error('Failed to delete branch:', err);
    }
  };

  const fileStatusLabel = (status: string) => {
    switch (status) {
      case 'A': return { label: 'A', color: 'text-green-400' };
      case 'M': return { label: 'M', color: 'text-yellow-400' };
      case 'D': return { label: 'D', color: 'text-red-400' };
      case 'R': return { label: 'R', color: 'text-blue-400' };
      default: return { label: status, color: 'text-gray-400' };
    }
  };

  return (
    <div className="h-full flex bg-[#1e1e2e]">
      {/* Left: Branch tree sidebar */}
      <div className="w-52 shrink-0 border-r border-[#313244] flex flex-col bg-[#181825]">
        {/* Search + Add */}
        <div className="p-2 border-b border-[#313244]">
          <div className="flex items-center gap-1">
            <div className="relative flex-1">
              <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-[#6c7086]" />
              <input
                type="text"
                placeholder={t('detail.branches.filterPlaceholder')}
                value={branchFilter}
                onChange={(e) => setBranchFilter(e.target.value)}
                className="w-full bg-[#313244] text-[#cdd6f4] text-xs rounded pl-6 pr-2 py-1.5 border border-[#45475a] focus:border-[#89b4fa] focus:outline-none placeholder-[#6c7086]"
              />
            </div>
            <button
              onClick={() => setShowCreate(!showCreate)}
              className="p-1.5 rounded hover:bg-[#313244] text-[#6c7086] hover:text-[#cdd6f4]"
              title={t('detail.branches.newBranch')}
            >
              <Plus className="w-3.5 h-3.5" />
            </button>
          </div>
          {showCreate && (
            <div className="mt-2 flex gap-1">
              <input
                type="text"
                placeholder={t('detail.branches.branchNamePlaceholder')}
                value={newBranchName}
                onChange={(e) => setNewBranchName(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleCreateBranch()}
                className="flex-1 bg-[#313244] text-[#cdd6f4] text-xs rounded px-2 py-1 border border-[#45475a] focus:border-[#89b4fa] focus:outline-none"
                autoFocus
              />
              <button
                onClick={handleCreateBranch}
                disabled={isCreating}
                className="text-xs px-2 py-1 rounded bg-[#89b4fa] text-[#1e1e2e] hover:bg-[#74c7ec] disabled:opacity-50"
              >
                {t('detail.branches.createBranch')}
              </button>
            </div>
          )}
        </div>

        <div className="flex-1 overflow-auto text-xs">
          {/* Git Changes — shown first */}
          <div className="border-b border-[#313244]">
            <button
              onClick={() => setChangesExpanded(!changesExpanded)}
              className="flex items-center gap-1 w-full px-2 py-1 text-[#a6adc8] hover:bg-[#313244]"
            >
              {changesExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
              <AlertCircle className="w-3 h-3" />
              <span>{t('detail.branches.changes')}</span>
              {allChanges.length > 0 && (
                <span className="ml-auto text-[10px] bg-[#f38ba8] text-[#1e1e2e] rounded-full px-1.5 leading-4 font-medium">
                  {allChanges.length}
                </span>
              )}
            </button>
            {changesExpanded && (
              <div>
                {allChanges.length > 0 ? (
                  <>
                    {/* Commit input + rollback */}
                    <div className="px-2 py-1.5 border-b border-[#313244]">
                      <div className="flex gap-1">
                        <input
                          type="text"
                          placeholder={t('detail.branches.commitPlaceholder')}
                          value={commitMessage}
                          onChange={(e) => setCommitMessage(e.target.value)}
                          onKeyDown={(e) => e.key === 'Enter' && handleCommitAll()}
                          className="flex-1 bg-[#313244] text-[#cdd6f4] text-[10px] rounded px-1.5 py-1 border border-[#45475a] focus:border-[#89b4fa] focus:outline-none placeholder-[#6c7086]"
                        />
                        <button
                          onClick={handleCommitAll}
                          disabled={isCommitting || !commitMessage.trim()}
                          className="p-1 rounded bg-[#a6e3a1] text-[#1e1e2e] hover:bg-[#94e2d5] disabled:opacity-50"
                          title={t('detail.branches.commit')}
                        >
                          <Check className="w-3 h-3" />
                        </button>
                        <button
                          onClick={handleDiscardAll}
                          disabled={isDiscarding}
                          className="p-1 rounded hover:bg-[#f38ba8]/20 text-[#f38ba8] disabled:opacity-50"
                          title={t('detail.branches.discardAll')}
                        >
                          <Undo2 className="w-3 h-3" />
                        </button>
                      </div>
                    </div>

                    {/* File list */}
                    <div className="max-h-48 overflow-auto">
                      {allChanges.map((file) => {
                        const statusColors: Record<string, string> = { M: 'text-[#f9e2af]', A: 'text-[#a6e3a1]', D: 'text-[#f38ba8]', '?': 'text-[#6c7086]' };
                        return (
                          <div
                            key={file.path}
                            className="flex items-center gap-1 pl-4 pr-2 py-0.5 hover:bg-[#313244] group"
                          >
                            <span className={cn('font-mono w-3 text-center shrink-0 text-[10px]', statusColors[file.status] || 'text-[#6c7086]')}>
                              {file.status}
                            </span>
                            <span className="text-[#cdd6f4] truncate flex-1 text-[11px]" title={file.path}>
                              {file.path.split('/').pop()}
                            </span>
                            <button
                              onClick={(e) => { e.stopPropagation(); handleDiscardFile(file.path); }}
                              className="hidden group-hover:block text-[#f38ba8] hover:text-[#eba0ac] shrink-0"
                              title={t('detail.branches.discardFile')}
                            >
                              <Undo2 className="w-2.5 h-2.5" />
                            </button>
                          </div>
                        );
                      })}
                    </div>
                  </>
                ) : (
                  <div className="px-4 py-2 text-[10px] text-[#6c7086]">{t('detail.branches.noChanges')}</div>
                )}
              </div>
            )}
          </div>

          {/* HEAD */}
          <div className="px-2 py-1.5 text-[#6c7086] flex items-center gap-1">
            <Monitor className="w-3 h-3" />
            <span>{t('detail.branches.head')}</span>
          </div>

          {/* Local branches */}
          <div>
            <button
              onClick={() => setLocalExpanded(!localExpanded)}
              className="flex items-center gap-1 w-full px-2 py-1 text-[#a6adc8] hover:bg-[#313244]"
            >
              {localExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
              <Monitor className="w-3 h-3" />
              <span>{t('detail.branches.local')}</span>
            </button>
            {localExpanded && localBranches.map((branch) => (
              <div
                key={branch}
                className={cn(
                  'flex items-center gap-1 pl-6 pr-2 py-1 cursor-pointer group',
                  branch === currentBranch ? 'bg-[#313244] text-[#89b4fa]' : 'text-[#cdd6f4] hover:bg-[#313244]'
                )}
                onDoubleClick={() => handleCheckout(branch)}
              >
                <GitBranchIcon className="w-3 h-3 shrink-0" />
                <span className="truncate flex-1">{branch}</span>
                {branch === currentBranch && (
                  <span className="text-[10px] text-[#6c7086]">*</span>
                )}
                {branch !== currentBranch && (
                  <button
                    onClick={(e) => { e.stopPropagation(); handleDeleteBranch(branch); }}
                    className="hidden group-hover:block text-[#f38ba8] hover:text-[#eba0ac] text-[10px]"
                    title={t('detail.branches.delete')}
                  >
                    ×
                  </button>
                )}
              </div>
            ))}
          </div>

          {/* Remote branches */}
          {remoteBranches.length > 0 && (
            <div>
              <button
                onClick={() => setRemoteExpanded(!remoteExpanded)}
                className="flex items-center gap-1 w-full px-2 py-1 text-[#a6adc8] hover:bg-[#313244]"
              >
                {remoteExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
                <Globe className="w-3 h-3" />
                <span>{t('detail.branches.remote')}</span>
              </button>
              {remoteExpanded && remoteBranches.map((branch) => (
                <div
                  key={branch}
                  className="flex items-center gap-1 pl-6 pr-2 py-1 text-[#a6adc8] hover:bg-[#313244] cursor-default"
                >
                  <GitBranchIcon className="w-3 h-3 shrink-0 text-[#a6e3a1]" />
                  <span className="truncate">{branch}</span>
                </div>
              ))}
            </div>
          )}

        </div>
      </div>

      {/* Center: Git graph */}
      <div className="flex-1 min-w-0 flex flex-col">
        {/* Graph toolbar */}
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-[#313244] bg-[#1e1e2e] shrink-0">
          <div className="flex items-center gap-2 text-xs text-[#a6adc8]">
            <GitBranchIcon className="w-3.5 h-3.5" />
            <span>{t('detail.branches.commitsCount', { count: graphCommits?.length ?? 0 })}</span>
          </div>
          <button
            onClick={() => { refetchTree(); queryClient.invalidateQueries({ queryKey: ['repository', repoId, 'graph'] }); refetchStatus(); }}
            className="p-1 rounded hover:bg-[#313244] text-[#6c7086] hover:text-[#cdd6f4]"
            title={t('detail.header.refresh')}
          >
            <RefreshCw className="w-3.5 h-3.5" />
          </button>
        </div>

        {/* Graph content */}
        <div ref={graphScrollRef} className="flex-1 min-h-0 overflow-auto">
          {layout.nodes.length > 0 ? (
            <div className="relative" style={{ minHeight: layout.nodes.length * ROW_HEIGHT }}>
              {/* SVG edges + nodes */}
              <svg
                className="absolute left-0 top-0 pointer-events-none"
                width={graphWidth}
                height={layout.nodes.length * ROW_HEIGHT}
                style={{ zIndex: 1 }}
              >
                {/* Continuous edges from child commit to parent commit.
                    Only edges that cross the visible window are mounted. */}
                {layout.edges
                  .filter((edge) =>
                    Math.max(edge.fromRow, edge.toRow) >= visibleStart &&
                    Math.min(edge.fromRow, edge.toRow) < visibleEnd
                  )
                  .map((edge) => {
                  const ekey = `e-${edge.fromRow}-${edge.toRow}-${edge.fromLane}-${edge.toLane}`;
                  const x1 = GRAPH_PADDING + edge.fromLane * LANE_WIDTH;
                  const y1 = edge.fromRow * ROW_HEIGHT + ROW_HEIGHT / 2;
                  const x2 = GRAPH_PADDING + edge.toLane * LANE_WIDTH;
                  const y2 = edge.toRow * ROW_HEIGHT + ROW_HEIGHT / 2;

                  if (x1 === x2) {
                    return <line key={ekey} x1={x1} y1={y1} x2={x2} y2={y2} stroke={edge.color} strokeWidth={2} opacity={0.8} />;
                  }

                  // Curve at the start then go straight down to the target
                  const curveEnd = Math.min(y1 + ROW_HEIGHT, y2);
                  const midY = (y1 + curveEnd) / 2;

                  if (curveEnd >= y2) {
                    // Short edge: just a bezier curve
                    return <path key={ekey} d={`M ${x1} ${y1} C ${x1} ${midY}, ${x2} ${midY}, ${x2} ${y2}`} stroke={edge.color} strokeWidth={2} fill="none" opacity={0.8} />;
                  }

                  // Long edge: curve to the target lane, then straight down
                  return (
                    <path
                      key={ekey}
                      d={`M ${x1} ${y1} C ${x1} ${midY}, ${x2} ${midY}, ${x2} ${curveEnd} L ${x2} ${y2}`}
                      stroke={edge.color} strokeWidth={2} fill="none" opacity={0.8}
                    />
                  );
                })}

                {/* Commit nodes (drawn on top of edges) — visible window only */}
                {layout.nodes.slice(visibleStart, visibleEnd).map((node, i) => {
                  const rowIdx = visibleStart + i;
                  const cx = GRAPH_PADDING + node.lane * LANE_WIDTH;
                  const cy = rowIdx * ROW_HEIGHT + ROW_HEIGHT / 2;
                  const isMerge = node.commit.parents && node.commit.parents.length > 1;
                  return (
                    <circle
                      key={node.commit.hash}
                      cx={cx} cy={cy} r={NODE_RADIUS + (isMerge ? 1 : 0)}
                      fill={node.commit.is_current ? '#1e1e2e' : node.color}
                      stroke={node.color}
                      strokeWidth={node.commit.is_current ? 2.5 : 1.5}
                    />
                  );
                })}
              </svg>

              {/* Commit info rows — visible window only */}
              {layout.nodes.slice(visibleStart, visibleEnd).map((node, i) => {
                const rowIdx = visibleStart + i;
                return (
                <div
                  key={node.commit.hash}
                  className={cn(
                    'flex items-center absolute w-full cursor-pointer',
                    selectedCommit === node.commit.hash ? 'bg-[#313244]' : 'hover:bg-[#313244]/50'
                  )}
                  style={{ height: ROW_HEIGHT, top: rowIdx * ROW_HEIGHT, paddingLeft: graphWidth + 8 }}
                  onClick={() => selectCommit(node.commit.hash)}
                >
                  <div className="flex items-center gap-1.5 min-w-0 flex-1">
                    {node.commit.refs && node.commit.refs.length > 0 && node.commit.refs.map((ref) => {
                      const isLocal = !ref.startsWith('origin/');
                      const isCurrent = node.commit.is_current && isLocal;
                      return (
                        <span
                          key={ref}
                          className={cn(
                            'text-[10px] px-1.5 py-0 rounded font-medium shrink-0 leading-4',
                            isCurrent
                              ? 'bg-[#89b4fa] text-[#1e1e2e]'
                              : isLocal
                                ? 'bg-[#a6e3a1]/20 text-[#a6e3a1] border border-[#a6e3a1]/40'
                                : 'bg-[#89b4fa]/20 text-[#89b4fa] border border-[#89b4fa]/40'
                          )}
                        >
                          {ref}
                        </span>
                      );
                    })}
                    <span className="text-[13px] text-[#cdd6f4] truncate">{node.commit.message}</span>
                  </div>
                  <div className="flex items-center gap-3 shrink-0 ml-3 pr-3">
                    <span className="text-xs text-[#6c7086] w-20 text-right truncate">{node.commit.author}</span>
                    <span className="text-xs text-[#6c7086] w-20 text-right">{formatRelativeTime(node.commit.date)}</span>
                  </div>
                </div>
                );
              })}
            </div>
          ) : (
            <div className="flex items-center justify-center h-full text-[#6c7086] text-sm">
              {graphCommits ? t('detail.branches.noCommits') : t('common:actions.loading')}
            </div>
          )}
        </div>
      </div>

      {/* Right: Commit detail panel */}
      {selectedCommit && commitDetail && (
        <div className="w-72 shrink-0 border-l border-[#313244] bg-[#181825] flex flex-col overflow-hidden">
          {/* Files changed */}
          <div className="p-3 border-b border-[#313244] shrink-0">
            <div className="text-xs text-[#6c7086] mb-2">{t('detail.branches.filesChanged')}</div>
            <div className="space-y-0.5 max-h-40 overflow-auto">
              {commitDetail.files_changed?.map((file) => {
                const st = fileStatusLabel(file.status);
                return (
                  <div key={file.path} className="flex items-center gap-1.5 text-xs py-0.5">
                    <span className={cn('font-mono w-3 text-center shrink-0', st.color)}>{st.label}</span>
                    <span className="text-[#cdd6f4] truncate" title={file.path}>{file.path.split('/').pop()}</span>
                  </div>
                );
              })}
              {(!commitDetail.files_changed || commitDetail.files_changed.length === 0) && (
                <div className="text-[#6c7086] text-xs">{t('detail.branches.noFilesChanged')}</div>
              )}
            </div>
          </div>

          {/* Commit info */}
          <div className="p-3 flex-1 overflow-auto">
            <div className="text-sm text-[#cdd6f4] font-medium mb-3 leading-snug">
              {commitDetail.message}
            </div>
            <div className="space-y-2 text-xs">
              <div>
                <span className="text-[#6c7086]">{t('detail.branches.commitInfo.commit')}</span>
                <code className="ml-2 text-[#89b4fa] font-mono">{commitDetail.short_hash}</code>
              </div>
              <div>
                <span className="text-[#6c7086]">{t('detail.branches.commitInfo.author')}</span>
                <span className="ml-2 text-[#cdd6f4]">{commitDetail.author}</span>
              </div>
              <div>
                <span className="text-[#6c7086]">{t('detail.branches.commitInfo.email')}</span>
                <span className="ml-2 text-[#a6adc8]">{commitDetail.author_email}</span>
              </div>
              <div>
                <span className="text-[#6c7086]">{t('detail.branches.commitInfo.date')}</span>
                <span className="ml-2 text-[#a6adc8]">{commitDetail.date}</span>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ==================== Repository Terminal ====================

function RepoTerminalPanel({ repoId, onClose }: { repoId: string; onClose: () => void }) {
  const { t } = useTranslation('repositories');
  const terminalRef = useRef<HTMLDivElement>(null);
  const terminalInstanceRef = useRef<Terminal | null>(null);
  const [isConnected, setIsConnected] = useState(false);
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme);

  useEffect(() => {
    if (!terminalRef.current) return;

    const palette = resolvedTheme === 'dark' ? DARK_TERMINAL_THEME : LIGHT_TERMINAL_THEME;
    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: '"JetBrains Mono", "Fira Code", Consolas, monospace',
      theme: palette,
    });

    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.loadAddon(new WebLinksAddon());
    terminal.open(terminalRef.current);
    fitAddon.fit();
    terminalInstanceRef.current = terminal;

    const token = getAccessToken();
    const tokenParam = token ? `?token=${encodeURIComponent(token)}` : '';
    const ws = new WebSocket(`/ws/repositories/${repoId}/terminal${tokenParam}`);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => setIsConnected(true);
    ws.onclose = () => setIsConnected(false);
    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        terminal.write(new TextDecoder().decode(event.data));
      } else if (typeof event.data === 'string') {
        terminal.write(event.data);
      }
    };

    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data);
    });

    const resizeObserver = new ResizeObserver(() => {
      fitAddon.fit();
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }));
      }
    });
    resizeObserver.observe(terminalRef.current);

    return () => {
      resizeObserver.disconnect();
      ws.close();
      terminal.dispose();
      terminalInstanceRef.current = null;
    };
    // resolvedTheme intentionally omitted: initial palette set here; hot-swap handled below
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repoId]);

  // Hot-swap theme without recreating the terminal (preserves scrollback)
  useEffect(() => {
    if (terminalInstanceRef.current) {
      terminalInstanceRef.current.options.theme =
        resolvedTheme === 'dark' ? DARK_TERMINAL_THEME : LIGHT_TERMINAL_THEME;
    }
  }, [resolvedTheme]);

  const termBg = resolvedTheme === 'dark' ? DARK_TERMINAL_THEME.background : LIGHT_TERMINAL_THEME.background;

  return (
    <div className="flex flex-col border-t" style={{ height: 260 }}>
      <div className="flex items-center justify-between px-3 py-1 border-b border-border bg-background/80 shrink-0">
        <div className="flex items-center gap-2">
          <TerminalIcon className="w-3.5 h-3.5 text-muted-foreground" />
          <span className="text-xs font-medium text-foreground">{t('detail.terminal.title')}</span>
        </div>
        <div className="flex items-center gap-2">
          <span className={cn('h-2 w-2 rounded-full', isConnected ? 'bg-success' : 'bg-destructive')} />
          <span className="text-xs text-muted-foreground">{isConnected ? t('detail.terminal.connected') : t('detail.terminal.disconnected')}</span>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground text-xs px-1" title={t('detail.terminal.close')}>
            <ChevronDown className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
      <div ref={terminalRef} className="flex-1 min-h-0 overflow-hidden p-1" style={{ background: termBg }} />
    </div>
  );
}

// ==================== Files Tab ====================

function RepoFilesTab({ repoId }: { repoId: string }) {
  const { t } = useTranslation('repositories');
  const [currentPath, setCurrentPath] = useState('');
  const [selectedFile, setSelectedFile] = useState<string | null>(null);

  const { data: files, isLoading, refetch: refetchFiles } = useQuery<FileEntry[]>({
    queryKey: ['repository', repoId, 'files', currentPath],
    queryFn: () => api.get<FileEntry[]>(`/repositories/${repoId}/files?path=${encodeURIComponent(currentPath)}`),
    enabled: !!repoId,
  });

  const handleFileClick = (file: FileEntry) => {
    if (file.type === 'dir') {
      setCurrentPath(file.path);
      setSelectedFile(null);
    } else {
      setSelectedFile(file.path);
    }
  };

  const handleNavigateUp = () => {
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    setCurrentPath(parts.join('/'));
    setSelectedFile(null);
  };

  return (
    <div className="h-full flex">
      <div className="w-1/3 border-r bg-card overflow-auto">
        <div className="flex items-center gap-1 px-3 py-2 border-b bg-muted">
          <button onClick={() => refetchFiles()} className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground shrink-0" title={t('detail.files.refresh')}><RefreshCw className="w-3.5 h-3.5" /></button>
          <button onClick={() => { setCurrentPath(''); setSelectedFile(null); }} className="text-xs text-info hover:underline">root</button>
          {currentPath && (
            <>
              <span className="text-muted-foreground">/</span>
              {currentPath.split('/').filter(Boolean).map((part, i, arr) => (
                <span key={i} className="flex items-center">
                  <span className="text-xs text-foreground">{part}</span>
                  {i < arr.length - 1 && <span className="text-muted-foreground">/</span>}
                </span>
              ))}
              <button onClick={handleNavigateUp} className="ml-auto text-xs text-muted-foreground hover:text-foreground">{t('detail.files.navigateUp')}</button>
            </>
          )}
        </div>
        <div className="p-2">
          {isLoading ? (
            <div className="space-y-1">{[...Array(5)].map((_, i) => <div key={i} className="h-8 bg-muted rounded animate-pulse" />)}</div>
          ) : files && files.length > 0 ? (
            files.map((file) => (
              <div key={file.path} onClick={() => handleFileClick(file)} className={cn('flex items-center gap-2 px-2 py-1.5 rounded cursor-pointer text-sm', selectedFile === file.path ? 'bg-info/10 text-info' : 'hover:bg-accent')}>
                {file.type === 'dir' ? (
                  <Folder className="w-4 h-4 text-info shrink-0" />
                ) : (
                  <File className="w-4 h-4 text-muted-foreground shrink-0" />
                )}
                <span className={cn('truncate', file.type === 'dir' && 'font-medium')}>{file.name}</span>
                {file.type === 'file' && <span className="ml-auto text-xs text-muted-foreground shrink-0">{formatBytes(file.size)}</span>}
              </div>
            ))
          ) : (
            <div className="text-center py-8 text-muted-foreground text-sm">{t('detail.files.emptyDir')}</div>
          )}
        </div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto bg-card">
        {selectedFile ? (
          <FilePreviewByUrl key={selectedFile} url={getRepoFileContentUrl(repoId, selectedFile)} path={selectedFile} />
        ) : (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">{t('detail.files.selectFile')}</div>
        )}
      </div>
    </div>
  );
}

// ==================== Worktrees Tab ====================

function RepoWorktreesTab({ repoId }: { repoId: string }) {
  const { t } = useTranslation('repositories');
  const [worktrees, setWorktrees] = useState<WorktreeWithWorkspace[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [newWtPath, setNewWtPath] = useState('');
  const [newWtBranch, setNewWtBranch] = useState('');
  const [isLoading, setIsLoading] = useState(false);

  const fetchWorktrees = useCallback(async () => {
    try {
      const data = await api.get<WorktreeWithWorkspace[]>(`/repositories/${repoId}/worktrees`);
      setWorktrees(data || []);
    } catch (err) {
      console.error('Failed to fetch worktrees:', err);
    }
  }, [repoId]);

  useEffect(() => { fetchWorktrees(); }, [fetchWorktrees]);

  const handleCreateWorktree = async () => {
    if (!newWtPath.trim() || !newWtBranch.trim()) return;
    setIsLoading(true);
    try {
      await api.post(`/repositories/${repoId}/worktrees`, { path: newWtPath, branch: newWtBranch });
      setNewWtPath(''); setNewWtBranch(''); setShowCreate(false);
      fetchWorktrees();
    } catch (err) { console.error('Failed to create worktree:', err); }
    finally { setIsLoading(false); }
  };

  const handleRemoveWorktree = async (wt: WorktreeWithWorkspace) => {
    if (!wt.id) return;
    if (!(await confirm(t('detail.worktrees.confirmRemove', { path: wt.path })))) return;
    try { await api.delete(`/repositories/${repoId}/worktrees/${wt.id}`); fetchWorktrees(); }
    catch (err) { console.error('Failed to remove worktree:', err); }
  };

  return (
    <div className="h-full flex flex-col bg-card">
      <div className="flex items-center justify-between px-4 py-3 border-b">
        <h2 className="font-semibold">{t('detail.worktrees.title')}</h2>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" className="h-8 w-8 p-0" onClick={() => fetchWorktrees()} title={t('detail.worktrees.refresh')}>
            <RefreshCw className="w-4 h-4" />
          </Button>
          <Button size="sm" onClick={() => setShowCreate(!showCreate)}>{showCreate ? t('common:actions.cancel') : t('detail.worktrees.create')}</Button>
        </div>
      </div>
      {showCreate && (
        <div className="flex items-center gap-2 px-4 py-3 border-b bg-muted">
          <Input placeholder={t('detail.worktrees.pathPlaceholder')} value={newWtPath} onChange={(e) => setNewWtPath(e.target.value)} className="w-64" />
          <Input placeholder={t('detail.worktrees.branchPlaceholder')} value={newWtBranch} onChange={(e) => setNewWtBranch(e.target.value)} className="w-40" />
          <Button size="sm" onClick={handleCreateWorktree} disabled={isLoading}>{t('detail.worktrees.createButton')}</Button>
        </div>
      )}
      <div className="flex-1 overflow-auto p-4">
        {worktrees.length > 0 ? worktrees.map((wt) => (
          <div key={wt.path} className="flex items-center justify-between px-3 py-2 rounded hover:bg-accent mb-1">
            <div className="flex items-center gap-2">
              <FolderGit2 className="w-4 h-4 text-info" />
              <div>
                <div className="text-sm font-medium">{wt.branch}</div>
                <div className="text-xs text-muted-foreground">{wt.path}</div>
              </div>
            </div>
            <div className="flex items-center gap-2">
              {wt.workspace_id ? <span className="text-xs text-success bg-success/10 dark:bg-emerald-950/30 dark:text-emerald-300 px-2 py-0.5 rounded">{t('detail.worktrees.workspaceLabel', { path: wt.workspace_path })}</span> : <span className="text-xs text-muted-foreground">{t('detail.worktrees.noWorkspace')}</span>}
              {wt.has_changes && <span className="text-xs text-warning">{t('detail.worktrees.uncommittedChanges')}</span>}
              {wt.id && <Button variant="ghost" size="sm" className="h-7 text-xs text-destructive hover:text-destructive/80" onClick={() => handleRemoveWorktree(wt)}>{t('detail.worktrees.remove')}</Button>}
            </div>
          </div>
        )) : <div className="text-center py-8 text-muted-foreground">{t('detail.worktrees.empty')}</div>}
      </div>
    </div>
  );
}

// ==================== Settings Tab ====================

function RepoSettingsTab({ repoId, repository }: { repoId: string; repository?: Repository }) {
  const { t } = useTranslation('repositories');
  const [name, setName] = useState(repository?.name || '');
  const [gitRemote, setGitRemote] = useState(repository?.git_remote || '');
  const [defaultBranch, setDefaultBranch] = useState(repository?.default_branch || '');
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    if (repository) { setName(repository.name); setGitRemote(repository.git_remote || ''); setDefaultBranch(repository.default_branch || ''); }
  }, [repository]);

  const handleSave = async () => {
    setIsSaving(true);
    try { await api.put(`/repositories/${repoId}`, { name, path: repository?.path, git_remote: gitRemote, default_branch: defaultBranch }); toast.success(t('detail.settings.saveSuccess')); }
    catch (err) { console.error('Failed to save:', err); toast.error(t('detail.settings.saveFailed')); }
    finally { setIsSaving(false); }
  };

  const handleDelete = async (deleteFiles: boolean) => {
    const msg = deleteFiles ? t('detail.settings.confirmDeleteWithFiles') : t('detail.settings.confirmDelete');
    if (!(await confirm({ description: msg, destructive: true }))) return;
    try { await api.delete(`/repositories/${repoId}${deleteFiles ? '?delete_directory=true' : ''}`); window.location.href = '/'; }
    catch (err) { console.error('Failed to delete:', err); toast.error(t('detail.settings.deleteFailed')); }
  };

  return (
    <div className="h-full overflow-auto bg-card">
      <div className="max-w-2xl mx-auto p-6">
        <h2 className="text-lg font-semibold mb-6">{t('detail.settings.title')}</h2>
        <div className="space-y-6">
          <div><label className="block text-sm font-medium text-foreground mb-1">{t('detail.settings.name')}</label><Input value={name} onChange={(e) => setName(e.target.value)} /></div>
          <div><label className="block text-sm font-medium text-foreground mb-1">{t('detail.settings.path')}</label><Input value={repository?.path || ''} disabled className="bg-muted" /></div>
          <div><label className="block text-sm font-medium text-foreground mb-1">{t('detail.settings.remote')}</label><Input value={gitRemote} onChange={(e) => setGitRemote(e.target.value)} placeholder={t('detail.settings.remotePlaceholder')} /></div>
          <div><label className="block text-sm font-medium text-foreground mb-1">{t('detail.settings.defaultBranch')}</label><Input value={defaultBranch} onChange={(e) => setDefaultBranch(e.target.value)} /></div>
          <Button onClick={handleSave} disabled={isSaving}><Save className="w-4 h-4 mr-2" />{isSaving ? t('detail.settings.saving') : t('detail.settings.save')}</Button>
          <hr className="my-6" />
          <RepoGitIdentitySection repoId={repoId} />
          <hr className="my-6" />
          <div>
            <h3 className="text-sm font-semibold text-destructive mb-2">{t('detail.settings.dangerZone')}</h3>
            <div className="flex items-center gap-4">
              <Button variant="outline" size="sm" onClick={() => handleDelete(false)}>{t('detail.settings.deleteRepository')}</Button>
              <Button variant="destructive" size="sm" onClick={() => handleDelete(true)}>{t('detail.settings.deleteWithFiles')}</Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

// ==================== Helpers ====================

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function formatRelativeTime(dateStr: string): string {
  if (!dateStr) return '';
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMin = Math.floor(diffMs / 60000);
  if (diffMin < 1) return i18n.t('repositories:time.now');
  if (diffMin < 60) return i18n.t('repositories:time.minutesAgo', { count: diffMin });
  const diffHour = Math.floor(diffMin / 60);
  if (diffHour < 24) return i18n.t('repositories:time.hoursAgo', { count: diffHour });
  const diffDay = Math.floor(diffHour / 24);
  if (diffDay < 7) return i18n.t('repositories:time.daysAgo', { count: diffDay });
  return date.toLocaleDateString(i18n.language || 'zh-CN', { month: '2-digit', day: '2-digit' });
}
