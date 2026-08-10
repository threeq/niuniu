import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useQueryClient } from '@tanstack/react-query';
import { FileText, FileSpreadsheet, Presentation, Image as ImageIcon, File, Download, Globe, FileCode, Trash2 } from 'lucide-react';
import { api, ApiError } from '@/lib/api';
import { cn } from '@/lib/utils';
import { getFileContentUrl } from '@/lib/workspace-file-url';
import { artifactKind, type ArtifactKind } from '@/lib/artifact-types';
import { useWorkspacePanelStore, contentTargetForPath } from '@/stores/workspace-panel-store';
import { FilePreview } from '../components/file-preview';

export interface ArtifactFile {
  path: string;
  name: string;
}

/**
 * - `viewer` (workspace 产物 panel): clicking opens the deliverable in the
 *   workspace's central content viewer.
 * - `inline` (牛牛助手 产物 panel): there is no content viewer, so clicking
 *   selects the row and previews its content inline below the list.
 */
export type ArtifactPanelVariant = 'viewer' | 'inline';

interface ArtifactPreviewPanelProps {
  workspaceId: string;
  artifacts: ArtifactFile[];
  variant?: ArtifactPanelVariant;
}

const KIND_ICON: Record<ArtifactKind, React.ComponentType<{ className?: string }>> = {
  doc: FileText,
  sheet: FileSpreadsheet,
  slide: Presentation,
  image: ImageIcon,
  pdf: FileText,
  html: Globe,
  markdown: FileText,
  text: FileCode,
  other: File,
};

// ArtifactPreviewPanel is the 产物 panel: a plain list of the agent's
// deliverables with per-row download + remove. How a click renders content
// depends on `variant` (workspace content viewer vs inline preview).
export function ArtifactPreviewPanel({ workspaceId, artifacts, variant = 'viewer' }: ArtifactPreviewPanelProps) {
  const { t } = useTranslation('workspaces');
  const queryClient = useQueryClient();
  const openViewer = useWorkspacePanelStore((s) => s.openContentViewer);
  const viewerTarget = useWorkspacePanelStore((s) => s.contentViewer[workspaceId] ?? null);
  const [removingPath, setRemovingPath] = useState<string | null>(null);
  // Inline variant: local selection (defaults to the first artifact so a
  // preview is always visible). Viewer variant derives selection from the
  // central content viewer target.
  const [inlineSelected, setInlineSelected] = useState<string>('');

  if (artifacts.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full gap-2 p-8 text-sm text-muted-foreground">
        <Presentation className="w-8 h-8" aria-hidden="true" />
        <span>{t('artifactPreview.empty')}</span>
      </div>
    );
  }

  const inlineActive = artifacts.find((a) => a.path === inlineSelected)?.path ?? artifacts[0].path;
  const selectedPath =
    variant === 'inline'
      ? inlineActive
      : viewerTarget && 'path' in viewerTarget
        ? viewerTarget.path
        : null;

  const handleOpen = (a: ArtifactFile) => {
    if (variant === 'inline') setInlineSelected(a.path);
    else openViewer(workspaceId, contentTargetForPath(a.path, a.name));
  };

  // Remove a deliverable from the manifest (the file on disk is left untouched),
  // then refresh the manifest query so the list updates.
  const handleRemove = async (a: ArtifactFile) => {
    if (removingPath) return;
    setRemovingPath(a.path);
    try {
      await api.delete(`/workspaces/${workspaceId}/artifacts?path=${encodeURIComponent(a.path)}`);
      await queryClient.invalidateQueries({ queryKey: ['assistant-artifacts', workspaceId] });
      toast.success(t('artifactPreview.removeDone', { name: a.name }));
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : t('artifactPreview.removeFailed');
      toast.error(msg || t('artifactPreview.removeFailed'));
    } finally {
      setRemovingPath(null);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-card">
      <div
        className={cn(
          'overflow-y-auto py-1',
          variant === 'inline' ? 'max-h-64 shrink-0 border-b border-border' : 'flex-1',
        )}
      >
        {artifacts.map((a) => {
          const Icon = KIND_ICON[artifactKind(a.path)] ?? File;
          const selected = a.path === selectedPath;
          const downloadUrl = getFileContentUrl(workspaceId, a.path, 'raw');
          return (
            <div
              key={a.path}
              className={cn(
                'group flex items-center gap-1 pr-2',
                selected ? 'bg-brand-soft' : 'hover:bg-accent',
              )}
            >
              <button
                type="button"
                onClick={() => handleOpen(a)}
                title={a.path}
                className="flex min-w-0 flex-1 items-center gap-2 py-1.5 pl-3 text-left"
              >
                <Icon
                  className={cn('h-3.5 w-3.5 shrink-0', selected ? 'text-brand' : 'text-muted-foreground')}
                  aria-hidden="true"
                />
                <span
                  className={cn('truncate text-xs', selected ? 'font-medium text-brand' : 'text-foreground')}
                >
                  {a.name}
                </span>
              </button>
              <a
                href={downloadUrl}
                download={a.name}
                title={t('artifactPreview.download')}
                onClick={(e) => e.stopPropagation()}
                className="shrink-0 rounded p-1 text-muted-foreground opacity-0 transition-colors hover:bg-background hover:text-info group-hover:opacity-100"
              >
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
              </a>
              <button
                type="button"
                onClick={() => handleRemove(a)}
                disabled={removingPath === a.path}
                title={t('artifactPreview.remove')}
                className="shrink-0 rounded p-1 text-muted-foreground opacity-0 transition-colors hover:bg-background hover:text-destructive group-hover:opacity-100 disabled:opacity-50"
              >
                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </div>
          );
        })}
      </div>

      {/* Inline variant previews the selected deliverable below the list. */}
      {variant === 'inline' && (
        <div className="min-h-0 flex-1 overflow-auto" key={selectedPath}>
          <FilePreview workspaceId={workspaceId} path={selectedPath!} />
        </div>
      )}
    </div>
  );
}
