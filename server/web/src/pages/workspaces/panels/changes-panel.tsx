import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import {
  ChevronDown,
  ChevronRight,
  Code,
  ExternalLink,
  Folder,
  GitBranch,
  GitCommitHorizontal,
  GitCompareArrows,
  GitMerge,
  Package,
  Search,
  Send,
  X,
} from 'lucide-react';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { api } from '@/lib/api';
import { openInVSCode } from '@/lib/vscode';
import type { WorkspaceComment } from '@/types/api';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { useChatInputBridge } from '@/stores/chat-input-bridge-store';
import {
  useWorkspaceDiff,
  type DiffFileRow,
  type RepoDiffGroup,
} from '@/lib/hooks/use-workspace-diff';
import { useRepoDiff } from '@/lib/hooks/use-file-diff';
import { useWorkspaceComments } from '@/lib/hooks/use-workspace-comments';
import { buildFileTree, type TreeNode } from '@/lib/changes-file-tree';
import { DiffViewer } from './diff-viewer';
import { CheckpointTimeline } from './checkpoint-timeline';

interface ChangesPanelProps {
  workspaceId: string;
}

type ViewMode = 'unified' | 'split';

// Maps the backend status word to a single-letter badge + a semantic color.
// Tokens only (no hardcoded green/red) — see docs/design-system.md.
const STATUS_BADGE: Record<string, { letter: string; cls: string }> = {
  modified: { letter: 'M', cls: 'bg-warning text-warning-foreground' },
  added: { letter: 'A', cls: 'bg-success text-success-foreground' },
  deleted: { letter: 'D', cls: 'bg-destructive text-destructive-foreground' },
  renamed: { letter: 'R', cls: 'bg-info text-info-foreground' },
  copied: { letter: 'C', cls: 'bg-info text-info-foreground' },
};

function statusBadge(status: string) {
  return STATUS_BADGE[status] ?? STATUS_BADGE.modified;
}

/** Inline +N / −N additions/deletions, using success/destructive tokens. */
function StatChips({ additions, deletions }: { additions: number; deletions: number }) {
  return (
    <span className="flex shrink-0 items-center gap-1.5 font-mono text-[11px]">
      {additions > 0 && <span className="text-success">+{additions}</span>}
      {deletions > 0 && <span className="text-destructive">−{deletions}</span>}
    </span>
  );
}

// --- File tree ---------------------------------------------------------------

function TreeFileRow({
  file,
  name,
  depth,
  selected,
  onSelect,
}: {
  file: DiffFileRow;
  name: string;
  depth: number;
  selected: boolean;
  onSelect: () => void;
}) {
  const badge = statusBadge(file.status);
  return (
    <button
      type="button"
      onClick={onSelect}
      title={file.path}
      style={{ paddingLeft: `${8 + depth * 16}px` }}
      className={cn(
        'flex w-full items-center gap-2 py-1 pr-3 text-left text-[13px] transition-colors',
        selected ? 'bg-brand-soft' : 'hover:bg-accent',
      )}
    >
      <span
        className={cn(
          'grid h-[18px] w-[18px] shrink-0 place-items-center rounded-md text-[10px] font-semibold',
          badge.cls,
        )}
      >
        {badge.letter}
      </span>
      <span
        className={cn(
          'flex-1 truncate',
          selected ? 'font-semibold text-brand' : 'font-medium text-foreground',
        )}
      >
        {name}
      </span>
      <StatChips additions={file.additions} deletions={file.deletions} />
      {file.commentCount > 0 && (
        <span className="grid h-[17px] min-w-[17px] shrink-0 place-items-center rounded-full bg-brand px-1 text-[10px] font-semibold text-brand-foreground">
          {file.commentCount}
        </span>
      )}
    </button>
  );
}

function TreeNodes({
  nodes,
  depth,
  repoName,
  selectedFile,
  onSelectFile,
  searching,
}: {
  nodes: TreeNode[];
  depth: number;
  repoName: string;
  selectedFile: string | null;
  onSelectFile: (key: string) => void;
  searching: boolean;
}) {
  return (
    <>
      {nodes.map((node) =>
        node.kind === 'dir' ? (
          <TreeDir
            key={`d:${node.path}`}
            node={node}
            depth={depth}
            repoName={repoName}
            selectedFile={selectedFile}
            onSelectFile={onSelectFile}
            searching={searching}
          />
        ) : (
          <TreeFileRow
            key={`f:${node.file.path}`}
            file={node.file}
            name={node.name}
            depth={depth}
            selected={selectedFile === `${repoName}:${node.file.path}`}
            onSelect={() => onSelectFile(`${repoName}:${node.file.path}`)}
          />
        ),
      )}
    </>
  );
}

function TreeDir({
  node,
  depth,
  repoName,
  selectedFile,
  onSelectFile,
  searching,
}: {
  node: Extract<TreeNode, { kind: 'dir' }>;
  depth: number;
  repoName: string;
  selectedFile: string | null;
  onSelectFile: (key: string) => void;
  searching: boolean;
}) {
  const [collapsed, setCollapsed] = useState(false);
  // While searching, force every folder open so matches are never hidden.
  const open = searching || !collapsed;
  return (
    <div>
      <button
        type="button"
        onClick={() => setCollapsed((c) => !c)}
        style={{ paddingLeft: `${8 + depth * 16}px` }}
        className="flex w-full items-center gap-1 py-1 pr-3 text-left text-[12.5px] text-foreground transition-colors hover:bg-accent"
      >
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" />
        )}
        <Folder className="h-3.5 w-3.5 shrink-0 text-info" />
        <span className="truncate">{node.name}</span>
      </button>
      {open && (
        <TreeNodes
          nodes={node.children}
          depth={depth + 1}
          repoName={repoName}
          selectedFile={selectedFile}
          onSelectFile={onSelectFile}
          searching={searching}
        />
      )}
    </div>
  );
}

/** Small inline status line shown in place of the diff while it loads / fails. */
function InlineNote({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-2 my-1.5 rounded-md border border-border bg-card px-3 py-2 text-xs text-muted-foreground">
      {children}
    </div>
  );
}

function RepoSection({
  workspaceId,
  repo,
  selectedFile,
  onSelectFile,
  search = '',
}: {
  workspaceId: string;
  repo: RepoDiffGroup;
  selectedFile: string | null;
  onSelectFile: (key: string) => void;
  /** Case-insensitive path filter (from the file-list search box). */
  search?: string;
}) {
  const { t } = useTranslation('workspaces');
  const fileCount = repo.files.length;
  // Empty repos start collapsed so the panel stays compact; repos with changes
  // start expanded. State is local but stable (RepoSection keyed by repo name).
  const [collapsed, setCollapsed] = useState(fileCount === 0);

  const q = search.trim().toLowerCase();
  const visibleFiles = useMemo(
    () => (q ? repo.files.filter((f) => f.path.toLowerCase().includes(q)) : repo.files),
    [repo.files, q],
  );
  const tree = useMemo(() => buildFileTree(visibleFiles), [visibleFiles]);

  // While searching, hide repos with no matching files and force-expand the rest.
  if (q && visibleFiles.length === 0) return null;
  const expanded = q ? true : !collapsed;

  return (
    <div>
      <div className="sticky top-0 z-[1] flex items-center gap-2 border-t border-border bg-muted px-3 py-2 first:border-t-0">
        {/* Title cluster toggles collapse; action buttons sit outside it so they
            don't trigger a toggle. */}
        <button
          type="button"
          onClick={() => setCollapsed((c) => !c)}
          title={t(collapsed ? 'panels.changes.expandRepo' : 'panels.changes.collapseRepo')}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
        >
          <ChevronRight
            className={cn(
              'h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform',
              !collapsed && 'rotate-90',
            )}
          />
          <Package className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate text-[12.5px] font-semibold text-foreground">
            {repo.name}
          </span>
          {repo.currentBranch && (
            <span
              title={t('panels.changes.repoCurrent', { branch: repo.currentBranch })}
              className="inline-flex min-w-0 shrink items-center gap-1 rounded-md border border-border bg-background px-1.5 py-px text-[10px] text-foreground"
            >
              <GitBranch className="h-2.5 w-2.5 shrink-0" />
              <span className="truncate">{repo.currentBranch}</span>
            </span>
          )}
        </button>
        <span className="flex shrink-0 items-center gap-1.5">
          {/* Per-repo change summary: always shown, even at 0 changes. The file
              count is rendered compactly (bare number) so the header doesn't
              overflow in a narrow panel — the full label lives in the toolbar. */}
          {fileCount === 0 ? (
            <span className="text-[11px] text-muted-foreground/70">
              {t('panels.changes.repoNoChanges')}
            </span>
          ) : (
            <>
              <span
                className="text-[11px] tabular-nums text-muted-foreground/80"
                title={t('panels.changes.changedFilesCount', { count: fileCount })}
              >
                {fileCount}
              </span>
              <StatChips additions={repo.additions} deletions={repo.deletions} />
            </>
          )}
          {/* Action buttons: always visible (no longer hover-gated). Sync needs a
              resolved repository (it merges another branch into this worktree's
              repo), so it is gated on repoId — not just the worktree path. */}
          {repo.repoId && repo.worktreePath && (
            <SyncFromBranchPopover
              workspaceId={workspaceId}
              repoId={repo.repoId}
              worktreeName={repo.name}
              worktreePath={repo.worktreePath}
            />
          )}
          {repo.worktreePath && (
            <button
              type="button"
              title={t('panels.changes.openInVSCode')}
              onClick={() => openInVSCode(repo.worktreePath!).catch(() => {})}
              className="rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-info"
            >
              <Code className="h-3.5 w-3.5" />
            </button>
          )}
          {repo.repoId && (
            <Link
              to="/repositories/$id"
              params={{ id: repo.repoId }}
              title={t('panels.changes.openRepo')}
              className="rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-info"
            >
              <ExternalLink className="h-3.5 w-3.5" />
            </Link>
          )}
        </span>
      </div>
      {expanded && (
        <TreeNodes
          nodes={tree}
          depth={0}
          repoName={repo.name}
          selectedFile={selectedFile}
          onSelectFile={onSelectFile}
          searching={!!q}
        />
      )}
    </div>
  );
}

/**
 * Loads and renders the selected file's line-level diff. DiffViewer renders its
 * own breadcrumb header (📦 repo › path) + body. Exported so the central content
 * viewer can render diffs with the same plumbing.
 */
export function DiffPane({
  workspaceId,
  repo,
  path,
  viewMode,
  comments,
  onQueueComment,
  onSendComment,
}: {
  workspaceId: string;
  repo: RepoDiffGroup;
  path: string;
  viewMode: ViewMode;
  comments: WorkspaceComment[];
  onQueueComment: (line: number, content: string) => Promise<void>;
  onSendComment: (line: number, content: string) => Promise<void>;
}) {
  const { t } = useTranslation('workspaces');

  // Resolved repos lazily load the line-level diff by id (cached per repo, so
  // opening multiple files in one repo shares one request). Groups with no
  // registered repository (repoId null) cannot fetch by id — but the workspace
  // diff response already carried their full per-file diffs (hunks + raw_patch),
  // so the viewer renders those inline instead of showing "unresolved".
  const usingInline = !repo.repoId;
  const {
    data: repoDiff,
    isLoading,
    error,
  } = useRepoDiff(workspaceId, repo.repoId, Boolean(repo.repoId));

  if (!usingInline) {
    if (isLoading) return <InlineNote>{t('panels.loading')}</InlineNote>;
    if (error) return <InlineNote>{t('panels.changes.diff.loadError')}</InlineNote>;
  }
  const fd = usingInline
    ? repo.rawFiles.find((f) => f.path === path)
    : repoDiff?.find((f) => f.path === path);
  if (!fd) return <InlineNote>{t('panels.changes.diff.noDiff')}</InlineNote>;

  return (
    <DiffViewer
      fileDiff={fd}
      repoName={repo.name}
      mode={viewMode}
      onOpenExternal={
        repo.worktreePath ? () => openInVSCode(repo.worktreePath!).catch(() => {}) : undefined
      }
      comments={comments}
      onQueueComment={onQueueComment}
      onSendComment={onSendComment}
    />
  );
}

function LocalToolbar({
  baseBranches,
  pendingCount,
  onSendAll,
  sendingAll,
  totalFiles,
  totalAdditions,
  totalDeletions,
}: {
  baseBranches: string[];
  pendingCount: number;
  onSendAll: () => void;
  sendingAll: boolean;
  totalFiles: number;
  totalAdditions: number;
  totalDeletions: number;
}) {
  const { t } = useTranslation('workspaces');

  return (
    <div className="flex min-h-8 flex-wrap items-center gap-2 border-b border-border bg-card px-3 py-1.5">
      <span className="flex items-center gap-1.5 text-[12.5px] font-semibold text-foreground">
        <GitCompareArrows className="h-4 w-4" />
        {t('panels.changes.title')}
      </span>
      {baseBranches.map((branch) => (
        <span
          key={branch}
          title={t('panels.changes.repoBase', { branch })}
          className="inline-flex min-w-0 items-center gap-1 rounded-md border border-border bg-background px-1.5 py-px text-[10px] text-muted-foreground"
        >
          <GitBranch className="h-2.5 w-2.5 shrink-0" />
          <span className="truncate">{t('panels.changes.repoBase', { branch })}</span>
        </span>
      ))}
      {/* Right cluster: all-repos total summary + send-queue action. The diff
          itself (and its view-mode toggle) now lives in the central content
          viewer, opened by clicking a file below. */}
      <span className="ml-auto flex items-center gap-2.5">
        <span className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
          <span className="font-semibold text-foreground">
            {t('panels.changes.changedFilesCount', { count: totalFiles })}
          </span>
          {totalFiles > 0 && <StatChips additions={totalAdditions} deletions={totalDeletions} />}
        </span>
        {pendingCount > 0 && (
          <Button
            type="button"
            size="sm"
            className="h-7 gap-1.5"
            onClick={onSendAll}
            disabled={sendingAll}
          >
            <Send className="h-3.5 w-3.5" />
            {t('panels.changes.comments.sendQueue', { count: pendingCount })}
          </Button>
        )}
      </span>
    </div>
  );
}

export function ChangesPanel({ workspaceId }: ChangesPanelProps) {
  const { t } = useTranslation('workspaces');
  // File-list search (path filter), stored per-workspace in the panel store so
  // it survives remounts.
  const fileSearch = useWorkspacePanelStore((s) => s.changesFileSearch?.[workspaceId] ?? '');
  const setFileSearchFor = useWorkspacePanelStore((s) => s.setChangesFileSearch);
  const setFileSearch = (query: string) => setFileSearchFor(workspaceId, query);

  // Clicking a file opens its diff in the central content viewer. The selected
  // `${repo}:${path}` key (for row highlight) is derived from that viewer target.
  const openViewer = useWorkspacePanelStore((s) => s.openContentViewer);
  const viewerTarget = useWorkspacePanelStore((s) => s.contentViewer[workspaceId] ?? null);
  const selectedFile =
    viewerTarget && viewerTarget.kind === 'diff'
      ? `${viewerTarget.repo}:${viewerTarget.path}`
      : null;

  const handleSelectFile = (key: string) => {
    const sep = key.indexOf(':');
    if (sep < 0) return;
    const repo = key.slice(0, sep);
    const path = key.slice(sep + 1);
    openViewer(workspaceId, { kind: 'diff', repo, path });
  };

  const { repos, totalFiles, totalAdditions, totalDeletions, totalAhead, baseBranches, isLoading } =
    useWorkspaceDiff(workspaceId);

  const { pendingCount, sendAllPending } = useWorkspaceComments(workspaceId);
  const [sendingAll, setSendingAll] = useState(false);

  const handleSendAll = async () => {
    setSendingAll(true);
    try {
      const { sent, failed } = await sendAllPending();
      if (failed > 0) toast.error(t('panels.changes.comments.sentPartial', { sent, failed }));
      else toast.success(t('panels.changes.comments.sentAll', { count: sent }));
    } finally {
      setSendingAll(false);
    }
  };

  // The path-filter search box appears only once there are enough files to make
  // it useful (> 10).
  const showSearch = totalFiles > 10;
  const activeSearch = showSearch ? fileSearch : '';
  const fileQuery = activeSearch.trim().toLowerCase();
  const hasMatches =
    !fileQuery || repos.some((r) => r.files.some((f) => f.path.toLowerCase().includes(fileQuery)));

  const searchBox = (
    <div className="shrink-0 border-b border-border p-2">
      <div className="relative">
        <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <input
          value={fileSearch}
          onChange={(e) => setFileSearch(e.target.value)}
          placeholder={t('panels.changes.searchFiles')}
          className="w-full rounded-md border border-border bg-background py-1.5 pl-7 pr-7 text-xs outline-none focus:border-ring"
        />
        {fileSearch && (
          <button
            type="button"
            onClick={() => setFileSearch('')}
            title={t('panels.changes.searchClear')}
            className="absolute right-1.5 top-1/2 grid h-5 w-5 -translate-y-1/2 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
    </div>
  );

  // File list rendered as a folder tree (both modes), filtered by the search
  // box. Repos with no match drop out (RepoSection returns null); when nothing
  // matches at all, show a no-results note.
  const treeFileBody = isLoading ? (
    <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
      {t('panels.loading')}
    </div>
  ) : repos.length === 0 ? (
    <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
      {t('panels.changes.noChanges')}
    </div>
  ) : !hasMatches ? (
    <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
      {t('panels.changes.searchNoMatch')}
    </div>
  ) : (
    repos.map((repo) => (
      <RepoSection
        key={repo.name}
        workspaceId={workspaceId}
        repo={repo}
        selectedFile={selectedFile}
        onSelectFile={handleSelectFile}
        search={activeSearch}
      />
    ))
  );

  // Bottom weak hint: commit count is informational only, not expandable.
  const commitHint =
    totalAhead > 0 ? (
      <div className="flex items-center gap-1.5 border-t border-border bg-card px-3.5 py-2 text-[11px] text-muted-foreground/80">
        <GitCommitHorizontal className="h-3.5 w-3.5 shrink-0" />
        <span>{t('panels.changes.commitHint', { count: totalAhead })}</span>
      </div>
    ) : null;

  return (
    <div className="flex h-full flex-col bg-card">
      <LocalToolbar
        baseBranches={baseBranches}
        pendingCount={pendingCount}
        onSendAll={handleSendAll}
        sendingAll={sendingAll}
        totalFiles={totalFiles}
        totalAdditions={totalAdditions}
        totalDeletions={totalDeletions}
      />

      {/* Autohost 安全网: per-worktree hidden-ref checkpoint timeline (collapsed by
          default), bound to this workspace — view step diffs / one-click revert. */}
      <CheckpointTimeline workspaceId={workspaceId} />

      {/* File tree (+ search when many files). Clicking a file opens its diff in
          the central content viewer; the clicked row stays highlighted. */}
      {showSearch && searchBox}
      <div className="min-h-0 flex-1 overflow-y-auto">{treeFileBody}</div>
      {commitHint}
    </div>
  );
}

function SyncFromBranchPopover({
  workspaceId,
  repoId,
  worktreeName,
  worktreePath,
}: {
  workspaceId: string;
  repoId: string;
  worktreeName: string;
  worktreePath: string;
}) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState('');
  const requestPrompt = useChatInputBridge((s) => s.request);

  // repoId is resolved upstream (by the diff response), so the branch list loads
  // directly — no fragile re-resolution by worktree path, which previously hung
  // forever for stale/reverse-resolved worktrees absent from the joined repo list.
  const { data: branchesData, isLoading: branchesLoading } = useQuery({
    queryKey: ['workspace-repo-branches', workspaceId, repoId],
    queryFn: () => api.getWorkspaceRepoBranches(workspaceId, repoId),
    enabled: open,
  });

  const currentBranch = branchesData?.current_branch ?? '';

  const filtered = useMemo(() => {
    const branches = branchesData?.branches ?? [];
    const current = branchesData?.current_branch ?? '';
    const f = filter.trim().toLowerCase();
    return branches
      .filter((b) => b !== current)
      .filter((b) => !f || b.toLowerCase().includes(f));
  }, [branchesData, filter]);

  const handlePick = (branch: string) => {
    setOpen(false);
    setFilter('');
    const prompt = t('panels.changes.syncFromBranchPrompt', {
      sourceBranch: branch,
      currentBranch: currentBranch || worktreeName,
      worktreeName,
      worktreePath,
    });
    requestPrompt(workspaceId, prompt, true);
  };

  const loading = branchesLoading;

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          title={t('panels.changes.syncFromBranchTitle')}
          onClick={(e) => e.stopPropagation()}
          className="rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-info"
        >
          <GitMerge className="h-3.5 w-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        side="bottom"
        className="w-64 p-1"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="px-2 pt-2 pb-1 text-[10px] uppercase tracking-wider text-muted-foreground/70">
          {t('panels.changes.syncFromBranchPickerTitle')}
        </div>
        <input
          autoFocus
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={t('panels.changes.syncFromBranchSearchPlaceholder')}
          className="mb-1 w-full rounded border border-border bg-background px-2 py-1 text-xs outline-none focus:border-ring"
        />
        <div className="max-h-60 overflow-y-auto">
          {loading ? (
            <p className="px-2 py-3 text-center text-xs text-muted-foreground/70">
              {t('panels.loading')}
            </p>
          ) : filtered.length === 0 ? (
            <p className="px-2 py-3 text-center text-xs text-muted-foreground/70">
              {t('panels.changes.syncFromBranchEmpty')}
            </p>
          ) : (
            filtered.map((b) => (
              <button
                key={b}
                onClick={() => handlePick(b)}
                className="flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent"
                title={b}
              >
                <GitBranch className="h-3 w-3 shrink-0 text-muted-foreground" />
                <span className="truncate font-mono">{b}</span>
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
