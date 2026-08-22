import { Suspense, lazy, useCallback, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, Loader2, PackagePlus, Save, Send, X } from 'lucide-react';
import { api, ApiError } from '@/lib/api';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { getFileContentUrl } from '@/lib/workspace-file-url';
import { useThemeStore } from '@/stores/theme-store';
import {
  useWorkspacePanelStore,
  type ContentViewerTarget,
} from '@/stores/workspace-panel-store';
import { useWorkspaceComments } from '@/lib/hooks/use-workspace-comments';
import { useWorkspaceDiff } from '@/lib/hooks/use-workspace-diff';
import { useCanvasBridge } from '@/hooks/use-canvas-bridge';
import type { WorkspaceComment } from '@/types/api';
import type { CanvasExporter } from '@/components/canvas/embedded-canvas-panel';
import { isMarkdownFile, isTextLikeFile } from '@/lib/file-type';
import { FilePreview } from '../components/file-preview';
import { DiffPane } from './changes-panel';
import { CodeFileView } from './code-file-view';
import { DiffViewer } from './diff-viewer';
import { checkpointApi } from '@/lib/api';

// The diagram editors are heavy; only pull them in when a diagram is opened.
const ExcalidrawCanvas = lazy(() => import('@/components/canvas/excalidraw-canvas'));
const DrawioCanvas = lazy(() => import('@/components/canvas/drawio-canvas'));

type ViewMode = 'unified' | 'split';

interface ContentViewerPanelProps {
  workspaceId: string;
  target: ContentViewerTarget;
}

function baseName(path: string): string {
  const parts = path.split('/');
  return parts[parts.length - 1] || path;
}

// A /tree/main path for a worktree file looks like `.worktrees/<name>/<rel>`.
// Resolving it yields the repo (worktree) name + worktree-relative path, which
// the comment API (repo/file_path) and the source-persist API both need.
function resolveWorktreePath(path: string): { worktree: string; relPath: string } | null {
  const m = path.match(/^\.worktrees\/([^/]+)\/(.+)$/);
  if (!m) return null;
  return { worktree: m[1], relPath: m[2] };
}

// The last path segment of an absolute worktree path is the `.worktrees` subdir
// (handles both `/` and `\` separators).
function worktreeSubdir(worktreePath: string): string {
  const parts = worktreePath.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] ?? '';
}

async function fetchRawText(workspaceId: string, path: string): Promise<string> {
  const res = await fetch(getFileContentUrl(workspaceId, path, 'raw'), { credentials: 'include' });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.text();
}

/**
 * ContentViewerPanel is the central reading area between chat and the right-side
 * panels. It renders exactly one target — a file (previewed or commentable), a
 * git diff, or an editable diagram — replacing the old file modal + changes
 * focus mode.
 */
export function ContentViewerPanel({ workspaceId, target }: ContentViewerPanelProps) {
  const { t } = useTranslation('workspaces');
  const closeViewer = useWorkspacePanelStore((s) => s.closeContentViewer);

  const path = 'path' in target ? target.path : '';
  const title = target.kind === 'file' ? target.title ?? baseName(path) : baseName(path);

  return (
    <div className="flex h-full min-w-0 flex-col bg-card">
      {/* Header: path/title + per-kind actions + close */}
      <div className="flex h-8 shrink-0 items-center gap-2 border-b border-border px-3">
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground" title={path}>
          {title}
        </span>
        {/* Files and diagrams (excalidraw/drawio) can be submitted as an
            artifact or downloaded straight from the viewer header. */}
        {(target.kind === 'file' || target.kind === 'canvas' || target.kind === 'drawio') && (
          <FileHeaderActions workspaceId={workspaceId} path={path} name={baseName(path)} />
        )}
        <button
          type="button"
          onClick={() => closeViewer(workspaceId)}
          aria-label={t('filePreview.close')}
          title={t('filePreview.close')}
          className="shrink-0 rounded p-0.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Body — children own their scroll so per-view scrollbars stay pinned. */}
      <div className="min-h-0 flex-1 overflow-hidden">
        {target.kind === 'file' && <FileBody workspaceId={workspaceId} path={path} />}
        {target.kind === 'diff' && (
          <CodeView workspaceId={workspaceId} repo={target.repo} relPath={path} allowDiff />
        )}
        {target.kind === 'checkpoint-diff' && (
          <CheckpointDiffBody
            workspaceId={workspaceId}
            checkpointId={target.checkpointId}
            path={path}
            repoName={target.repoName}
          />
        )}
        {target.kind === 'canvas' && (
          <DiagramEditor workspaceId={workspaceId} path={path} kind="canvas" />
        )}
        {target.kind === 'drawio' && (
          <DiagramEditor workspaceId={workspaceId} path={path} kind="drawio" />
        )}
      </div>
    </div>
  );
}

/** File body: commentable code view when it's a text file in a worktree, else
 *  the generic type-dispatched preview. */
function FileBody({ workspaceId, path }: { workspaceId: string; path: string }) {
  const wt = resolveWorktreePath(path);
  // Comments anchor on (repo, file_path). Worktree files use the worktree name
  // as repo + the worktree-relative path; workspace-root files (e.g. .mcp.json,
  // CLAUDE.md) use an empty repo + their workspace-relative path. Both are
  // commentable and can be sent to the agent, so any text-viewable file — in a
  // worktree or at the workspace root — supports annotations.
  const repo = wt ? wt.worktree : '';
  const relPath = wt ? wt.relPath : path;

  // Markdown gets a rendered-preview / raw-source toggle; the source side is a
  // commentable full-file view, so markdown too can be annotated.
  if (isMarkdownFile(path)) {
    return <MarkdownFileBody workspaceId={workspaceId} path={path} repo={repo} relPath={relPath} />;
  }
  // Any other text/code file (json, config, code, …) becomes a commentable
  // full-file view; rich formats (images/pdf/office) fall back to the
  // type-dispatched preview. `allowDiff={false}` keeps it a plain content view.
  if (isTextLikeFile(path)) {
    return (
      <CodeView workspaceId={workspaceId} repo={repo} relPath={relPath} rawPath={path} allowDiff={false} />
    );
  }
  return (
    <div className="h-full overflow-auto">
      <FilePreview workspaceId={workspaceId} path={path} />
    </div>
  );
}

/** Checkpoint diff body: one file's diff inside a hidden-ref checkpoint step. The
 *  step's per-file diffs (parent..snapshot) are fetched once by checkpoint row id
 *  and cached; this selects the requested file and renders it read-only (no inline
 *  comments — a checkpoint is a historical snapshot, not the live worktree). */
function CheckpointDiffBody({
  workspaceId,
  checkpointId,
  path,
  repoName,
}: {
  workspaceId: string;
  checkpointId: number;
  path: string;
  repoName: string;
}) {
  const { t } = useTranslation('workspaces');
  const { data, isLoading, error } = useQuery({
    queryKey: ['checkpoint-diff', workspaceId, checkpointId],
    queryFn: () => checkpointApi.diff(workspaceId, checkpointId),
    staleTime: 60_000,
  });
  if (isLoading) return <CenterNote>{t('panels.loading')}</CenterNote>;
  if (error) return <CenterNote>{t('panels.changes.diff.loadError')}</CenterNote>;
  const fd = data?.files.find((f) => f.path === path);
  if (!fd) return <CenterNote>{t('panels.changes.diff.noDiff')}</CenterNote>;
  return (
    <div className="h-full overflow-auto">
      <DiffViewer fileDiff={fd} repoName={repoName} mode="unified" disableCollapse />
    </div>
  );
}

/** 提交为产物 + 下载 — carried over from the retired file modal header. */
function FileHeaderActions({
  workspaceId,
  path,
  name,
}: {
  workspaceId: string;
  path: string;
  name: string;
}) {
  const { t } = useTranslation('workspaces');
  const queryClient = useQueryClient();
  const [submitting, setSubmitting] = useState(false);
  const downloadUrl = getFileContentUrl(workspaceId, path, 'raw');

  const handleSubmitArtifact = async () => {
    if (submitting) return;
    setSubmitting(true);
    try {
      await api.post(`/workspaces/${workspaceId}/artifacts`, { path, title: name });
      await queryClient.invalidateQueries({ queryKey: ['workspace-artifacts', workspaceId] });
      toast.success(t('filePreview.submitArtifactDone', { name }));
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : t('filePreview.submitArtifactFailed');
      toast.error(msg || t('filePreview.submitArtifactFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="h-6 shrink-0 gap-1 px-2 text-xs"
        onClick={handleSubmitArtifact}
        disabled={submitting}
      >
        <PackagePlus className="h-3.5 w-3.5" aria-hidden="true" />
        <span className="hidden sm:inline">{t('filePreview.submitArtifact')}</span>
      </Button>
      <Button asChild variant="outline" size="sm" className="h-6 shrink-0 gap-1 px-2 text-xs">
        <a href={downloadUrl} download={name}>
          <Download className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="hidden sm:inline">{t('filePreview.download')}</span>
        </a>
      </Button>
    </>
  );
}

/** A small unified/split toggle shared by the diff + commentable-file views. */
function ViewModeToggle({ mode, onChange }: { mode: ViewMode; onChange: (m: ViewMode) => void }) {
  const { t } = useTranslation('workspaces');
  return (
    <div className="flex rounded-lg bg-muted p-0.5 text-[11.5px]">
      {(['unified', 'split'] as ViewMode[]).map((m) => (
        <button
          key={m}
          type="button"
          onClick={() => onChange(m)}
          className={cn(
            'rounded-md px-2.5 py-1 transition-colors',
            mode === m
              ? 'bg-background font-medium text-foreground shadow-sm'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {t(m === 'unified' ? 'panels.changes.viewUnified' : 'panels.changes.viewSplit')}
        </button>
      ))}
    </div>
  );
}

/** Per-file comment queue wiring shared by the code view and the markdown
 *  source view: filters the workspace comments to this file and exposes
 *  queue/send/send-all handlers with the same toasts. */
function useFileCommentActions(workspaceId: string, repo: string, relPath: string) {
  const { t } = useTranslation('workspaces');
  const { comments, queueComment, sendComment, sendAllPending } = useWorkspaceComments(workspaceId);
  const fileComments = comments.filter((c) => c.repo === repo && c.file_path === relPath);
  const pendingCount = fileComments.filter((c) => c.sent_to_agent !== true).length;
  const [sendingAll, setSendingAll] = useState(false);

  const handleQueue = (line: number, content: string) =>
    queueComment({ repo, file_path: relPath, line_number: line, content }).then(() => {
      toast.success(t('panels.changes.comments.queued'));
    });
  const handleSend = (line: number, content: string) =>
    sendComment({ repo, file_path: relPath, line_number: line, content }).then(() => {
      toast.success(t('panels.changes.comments.sentOne'));
    });
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

  return { fileComments, pendingCount, sendingAll, handleQueue, handleSend, handleSendAll };
}

/** The "send N queued comments to agent" button; renders nothing when empty. */
function SendQueueButton({
  pendingCount,
  sending,
  onClick,
}: {
  pendingCount: number;
  sending: boolean;
  onClick: () => void;
}) {
  const { t } = useTranslation('workspaces');
  if (pendingCount === 0) return null;
  return (
    <Button type="button" size="sm" className="h-7 gap-1.5" onClick={onClick} disabled={sending}>
      <Send className="h-3.5 w-3.5" />
      {t('panels.changes.comments.sendQueue', { count: pendingCount })}
    </Button>
  );
}

type MarkdownMode = 'preview' | 'source';

/** Markdown body: a rendered-preview / raw-source toggle. Preview reuses the
 *  rich markdown renderer; source is the same commentable full-file view as
 *  code, so markdown source can be annotated and sent to the agent. */
function MarkdownFileBody({
  workspaceId,
  path,
  repo,
  relPath,
}: {
  workspaceId: string;
  path: string;
  repo: string;
  relPath: string;
}) {
  const { t } = useTranslation('workspaces');
  const [mode, setMode] = useState<MarkdownMode>('preview');
  const { fileComments, pendingCount, sendingAll, handleQueue, handleSend, handleSendAll } =
    useFileCommentActions(workspaceId, repo, relPath);

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card px-3 py-1.5">
        <div className="flex rounded-lg bg-muted p-0.5 text-[11.5px]">
          {(['preview', 'source'] as MarkdownMode[]).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              className={cn(
                'rounded-md px-2.5 py-1 transition-colors',
                mode === m
                  ? 'bg-background font-medium text-foreground shadow-sm'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t(m === 'preview' ? 'contentViewer.viewPreview' : 'contentViewer.viewSource')}
            </button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-2">
          <SendQueueButton pendingCount={pendingCount} sending={sendingAll} onClick={handleSendAll} />
        </div>
      </div>
      <div className="min-h-0 flex-1">
        {mode === 'preview' ? (
          <div className="h-full overflow-auto">
            <FilePreview workspaceId={workspaceId} path={path} />
          </div>
        ) : (
          <FileContentBody
            workspaceId={workspaceId}
            rawPath={path}
            repo={repo}
            relPath={relPath}
            comments={fileComments}
            onQueue={handleQueue}
            onSend={handleSend}
          />
        )}
      </div>
    </div>
  );
}

type CodeMode = 'diff' | 'file';

/**
 * CodeView renders a repo file as a diff and/or its full content, with a
 * GitHub-style toggle between the two (when the file has a diff) and shared
 * per-line comment queue/send. Both modes flow through the DiffViewer, so the
 * horizontal scrollbar stays pinned to the pane bottom.
 */
function CodeView({
  workspaceId,
  repo,
  relPath,
  rawPath,
  allowDiff,
}: {
  workspaceId: string;
  repo: string;
  relPath: string;
  /** Exact workspace-root-relative path for the file tree; omitted for the
   *  changes list, where it's derived from the diff group's worktree path. */
  rawPath?: string;
  /** Show the diff/file toggle. Only the changes list (which always has a diff)
   *  enables it; the file tree opens a plain content view with no toggle. */
  allowDiff: boolean;
}) {
  const { t } = useTranslation('workspaces');
  const [mode, setMode] = useState<CodeMode>(allowDiff ? 'diff' : 'file');
  const [viewMode, setViewMode] = useState<ViewMode>('unified');

  // The changes-list diff group's `name` is the REPOSITORY name, not the
  // `.worktrees` subdir the file actually sits under — derive the subdir from
  // the group's absolute worktreePath so the file-content fetch hits a real path.
  const { repos } = useWorkspaceDiff(workspaceId);
  const group = repos.find((r) => r.name === repo);
  const fileRawPath =
    rawPath ??
    (group?.worktreePath
      ? `.worktrees/${worktreeSubdir(group.worktreePath)}/${relPath}`
      : `.worktrees/${repo}/${relPath}`);

  const { fileComments, pendingCount, sendingAll, handleQueue, handleSend, handleSendAll } =
    useFileCommentActions(workspaceId, repo, relPath);

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card px-3 py-1.5">
        {allowDiff && (
          <div className="flex rounded-lg bg-muted p-0.5 text-[11.5px]">
            {(['diff', 'file'] as CodeMode[]).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={cn(
                  'rounded-md px-2.5 py-1 transition-colors',
                  mode === m
                    ? 'bg-background font-medium text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {t(m === 'diff' ? 'contentViewer.viewDiff' : 'contentViewer.viewFile')}
              </button>
            ))}
          </div>
        )}
        <div className="ml-auto flex items-center gap-2">
          <SendQueueButton pendingCount={pendingCount} sending={sendingAll} onClick={handleSendAll} />
          {mode === 'diff' && <ViewModeToggle mode={viewMode} onChange={setViewMode} />}
        </div>
      </div>
      <div className="min-h-0 flex-1">
        {mode === 'diff' ? (
          <DiffModeBody
            workspaceId={workspaceId}
            repo={repo}
            relPath={relPath}
            viewMode={viewMode}
            comments={fileComments}
            onQueue={handleQueue}
            onSend={handleSend}
          />
        ) : (
          <FileContentBody
            workspaceId={workspaceId}
            rawPath={fileRawPath}
            repo={repo}
            relPath={relPath}
            comments={fileComments}
            onQueue={handleQueue}
            onSend={handleSend}
          />
        )}
      </div>
    </div>
  );
}

/** Diff mode: resolves the repo group and renders the file's line-level diff. */
function DiffModeBody({
  workspaceId,
  repo,
  relPath,
  viewMode,
  comments,
  onQueue,
  onSend,
}: {
  workspaceId: string;
  repo: string;
  relPath: string;
  viewMode: ViewMode;
  comments: WorkspaceComment[];
  onQueue: (line: number, content: string) => Promise<void>;
  onSend: (line: number, content: string) => Promise<void>;
}) {
  const { t } = useTranslation('workspaces');
  const { repos, isLoading } = useWorkspaceDiff(workspaceId);
  const repoGroup = repos.find((r) => r.name === repo) ?? null;
  if (isLoading && !repoGroup) return <CenterNote>{t('panels.loading')}</CenterNote>;
  if (!repoGroup) return <CenterNote>{t('panels.changes.diff.noDiff')}</CenterNote>;
  return (
    <DiffPane
      workspaceId={workspaceId}
      repo={repoGroup}
      path={relPath}
      viewMode={viewMode}
      comments={comments}
      onQueueComment={onQueue}
      onSendComment={onSend}
    />
  );
}

/** File mode: the file's FULL content as plain line-numbered code (no diff
 *  chrome) with per-line comments. */
function FileContentBody({
  workspaceId,
  rawPath,
  repo,
  relPath,
  comments,
  onQueue,
  onSend,
}: {
  workspaceId: string;
  rawPath: string;
  repo: string;
  relPath: string;
  comments: WorkspaceComment[];
  onQueue: (line: number, content: string) => Promise<void>;
  onSend: (line: number, content: string) => Promise<void>;
}) {
  const { t } = useTranslation('workspaces');
  const { data: text, isLoading, error } = useQuery({
    queryKey: ['content-viewer-file', workspaceId, rawPath],
    queryFn: () => fetchRawText(workspaceId, rawPath),
    staleTime: 15_000,
  });

  if (isLoading) return <CenterNote>{t('filePreview.loading')}</CenterNote>;
  if (error || text == null) return <CenterNote>{t('filePreview.unsupported')}</CenterNote>;

  return (
    <CodeFileView
      content={text}
      repoName={repo}
      filePath={relPath}
      comments={comments}
      onQueueComment={onQueue}
      onSendComment={onSend}
    />
  );
}

/** Editable diagram (Excalidraw / draw.io) loaded from a worktree file, with
 *  save-back-to-file and send-to-agent actions. */
function DiagramEditor({
  workspaceId,
  path,
  kind,
}: {
  workspaceId: string;
  path: string;
  kind: 'canvas' | 'drawio';
}) {
  const { t } = useTranslation('workspaces');
  const dark = useThemeStore((s) => s.resolvedTheme) === 'dark';
  const queryClient = useQueryClient();
  const wt = resolveWorktreePath(path);
  const { send, sending } = useCanvasBridge(workspaceId);
  const [saving, setSaving] = useState(false);

  const exporterRef = useRef<CanvasExporter | null>(null);
  const registerExporter = useCallback((fn: CanvasExporter | null) => {
    exporterRef.current = fn;
  }, []);
  const runExport = useCallback(
    () => (exporterRef.current ? exporterRef.current() : Promise.resolve(null)),
    [],
  );

  const { data: content, isLoading, error } = useQuery({
    queryKey: ['content-viewer-diagram', workspaceId, path],
    queryFn: () => fetchRawText(workspaceId, path),
    staleTime: 30_000,
  });

  const nameNoExt = baseName(path).replace(/\.(excalidraw|drawio)$/i, '');

  const handleSave = async () => {
    if (!wt || saving) return;
    const exported = await runExport();
    if (!exported?.sourceContent) {
      toast.info(t('canvas.empty'));
      return;
    }
    setSaving(true);
    try {
      await api.writeWorktreeFile(workspaceId, wt.worktree, wt.relPath, exported.sourceContent);
      queryClient.setQueryData(['content-viewer-diagram', workspaceId, path], exported.sourceContent);
      toast.success(t('contentViewer.saved'));
    } catch {
      toast.error(t('contentViewer.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const handleSend = async () => {
    if (sending) return;
    const exported = await runExport();
    if (!exported) {
      toast.info(t('canvas.empty'));
      return;
    }
    await send({
      blob: exported.blob,
      filename: `${nameNoExt}.png`,
      prompt: t(kind === 'canvas' ? 'canvas.defaultPrompt' : 'drawio.defaultPrompt', { name: nameNoExt }),
      autoSend: true,
      source:
        wt && exported.sourceContent
          ? { worktree: wt.worktree, path: wt.relPath, content: exported.sourceContent }
          : undefined,
    });
  };

  const loadingKey = kind === 'canvas' ? 'canvas.loading' : 'drawio.loading';
  if (isLoading) return <CenterNote>{t(loadingKey)}</CenterNote>;
  if (error || content == null) return <CenterNote>{t('filePreview.unsupported')}</CenterNote>;

  return (
    <div className="flex h-full flex-col">
      <div className="relative min-h-0 flex-1">
        <Suspense fallback={<CenterNote>{t(loadingKey)}</CenterNote>}>
          {kind === 'canvas' ? (
            <ExcalidrawCanvas dark={dark} initialContent={content} registerExporter={registerExporter} />
          ) : (
            <DrawioCanvas dark={dark} initialContent={content} registerExporter={registerExporter} />
          )}
        </Suspense>
      </div>
      <div className="flex shrink-0 items-center justify-end gap-2 border-t border-border bg-warm-muted px-3 py-2">
        {wt && (
          <Button type="button" variant="outline" size="sm" className="h-7 gap-1.5" onClick={handleSave} disabled={saving}>
            {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
            {t('contentViewer.save')}
          </Button>
        )}
        <Button type="button" size="sm" className="h-7 gap-1.5" onClick={handleSend} disabled={sending}>
          {sending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Send className="h-3.5 w-3.5" />}
          {t('canvas.sendToAgent')}
        </Button>
      </div>
    </div>
  );
}

function CenterNote({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center px-6 text-center text-xs text-muted-foreground">
      {children}
    </div>
  );
}
