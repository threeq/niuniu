import { useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Send, Loader2, GitBranch } from 'lucide-react';
import { api } from '@/lib/api';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { useCanvasBridge } from '@/hooks/use-canvas-bridge';
import { toast } from 'sonner';

export interface CanvasExportResult {
  /** Exported raster image (PNG) for the agent. */
  blob: Blob;
  /** Editor source content to persist into the worktree (e.g. `.excalidraw`). */
  sourceContent?: string;
}

/**
 * An embedded editor's scene exporter: produces the current canvas as a PNG
 * (+ optional diffable source), or null when there's nothing to send (empty
 * canvas). Registered with the panel by each concrete editor.
 */
export type CanvasExporter = () => Promise<CanvasExportResult | null>;

export interface EmbeddedCanvasPanelProps {
  workspaceId: string;
  title: string;
  icon?: ReactNode;
  /** Default base filename (without extension), e.g. "annotation". */
  defaultBaseName?: string;
  /** Source file extension without the dot (e.g. "excalidraw"). When set and a
   *  worktree is available, the editor source is persisted there so it's
   *  diffable in the Changes panel. */
  sourceExt?: string;
  /** Worktree-relative directory the source file is written under. */
  sourceDir?: string;
  /** Build the message text sent alongside the exported image. */
  buildPrompt: (baseName: string) => string;
  /** Export the current canvas. Return null to abort (e.g. empty canvas). */
  onExport: () => Promise<CanvasExportResult | null>;
  /** The embedded editor. */
  children: ReactNode;
}

// Keep filenames filesystem- and git-friendly: word chars, dot, dash only.
function sanitizeBaseName(raw: string, fallback: string): string {
  const cleaned = raw.trim().replace(/[^\w.-]+/g, '_').replace(/^_+|_+$/g, '');
  return cleaned || fallback;
}

/**
 * EmbeddedCanvasPanel is the reusable chrome for the "inline canvas + send to
 * Agent" skeleton: a header, the editor body, and a footer action bar that
 * drives the shared {@link useCanvasBridge} pipeline (export → persist source →
 * upload → one-click send). Concrete editors (Excalidraw, and future ones) plug
 * in via `children` + `onExport`.
 */
export function EmbeddedCanvasPanel({
  workspaceId,
  title,
  icon,
  defaultBaseName = 'canvas',
  sourceExt,
  sourceDir = 'canvas',
  buildPrompt,
  onExport,
  children,
}: EmbeddedCanvasPanelProps) {
  const { t } = useTranslation('workspaces');
  const { send, sending } = useCanvasBridge(workspaceId);
  const [baseName, setBaseName] = useState(defaultBaseName);

  const { data: worktrees = [] } = useQuery({
    queryKey: ['workspace', workspaceId, 'tree-groups'],
    queryFn: () => api.getWorkspaceTreeGroups(workspaceId),
    staleTime: 60_000,
  });

  const [target, setTarget] = useState<string>('');
  const activeTarget = target || worktrees[0]?.name || '';
  const canPersist = !!sourceExt && worktrees.length > 0;

  const sourcePath = useMemo(() => {
    if (!sourceExt) return '';
    const name = sanitizeBaseName(baseName, defaultBaseName);
    const dir = sourceDir ? `${sourceDir.replace(/\/+$/, '')}/` : '';
    return `${dir}${name}.${sourceExt}`;
  }, [sourceExt, sourceDir, baseName, defaultBaseName]);

  async function handleSend() {
    const name = sanitizeBaseName(baseName, defaultBaseName);
    const exported = await onExport();
    if (!exported) {
      toast.info(t('canvas.empty'));
      return;
    }
    const result = await send({
      blob: exported.blob,
      filename: `${name}.png`,
      prompt: buildPrompt(name),
      autoSend: true,
      source:
        canPersist && exported.sourceContent
          ? { worktree: activeTarget, path: sourcePath, content: exported.sourceContent }
          : undefined,
    });
    if (result) {
      toast.success(
        result.sourcePath
          ? t('canvas.sentWithSource', { path: result.sourcePath })
          : t('canvas.sent'),
      );
    }
  }

  return (
    <div className="flex h-full flex-col bg-warm-surface">
      {/* Header */}
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-warm-border px-3">
        {icon}
        <span className="text-sm font-medium text-warm-text">{title}</span>
      </div>

      {/* Editor body */}
      <div className="relative min-h-0 flex-1">{children}</div>

      {/* Footer action bar */}
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-t border-warm-border bg-warm-muted px-3 py-2">
        <label className="flex items-center gap-1.5 text-xs text-warm-text-muted">
          {t('canvas.fileName')}
          <Input
            value={baseName}
            onChange={(e) => setBaseName(e.target.value)}
            className="h-7 w-32 text-xs"
            spellCheck={false}
            aria-label={t('canvas.fileName')}
          />
        </label>

        {canPersist && (
          <div className="flex items-center gap-1.5 text-xs text-warm-text-muted">
            <GitBranch className="h-3.5 w-3.5 shrink-0" aria-hidden />
            {worktrees.length > 1 ? (
              <select
                value={activeTarget}
                onChange={(e) => setTarget(e.target.value)}
                aria-label={t('canvas.targetWorktree')}
                className="h-7 rounded-md border border-warm-border bg-warm-surface px-2 text-xs text-warm-text focus:border-brand focus:outline-none"
              >
                {worktrees.map((w) => (
                  <option key={w.name} value={w.name}>
                    {w.name}
                  </option>
                ))}
              </select>
            ) : (
              <span className="font-mono text-warm-text" title={sourcePath}>
                {activeTarget}
              </span>
            )}
            <span className="truncate font-mono text-warm-text-muted/80" title={sourcePath}>
              /{sourcePath}
            </span>
          </div>
        )}

        <Button
          type="button"
          size="sm"
          onClick={handleSend}
          disabled={sending}
          className="ml-auto"
        >
          {sending ? (
            <Loader2 className={cn('mr-1 h-4 w-4 animate-spin')} aria-hidden />
          ) : (
            <Send className="mr-1 h-4 w-4" aria-hidden />
          )}
          {t('canvas.sendToAgent')}
        </Button>
      </div>
    </div>
  );
}
