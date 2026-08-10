import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ExternalLink, ChevronDown, ChevronUp } from 'lucide-react';
import { cn } from '@/lib/utils';
import { simpleHighlight } from '@/lib/simple-highlight';
import { getFileContentUrl } from '@/lib/workspace-file-url';
import type { ChatAttachment } from '@/types/api';
import { ImageLightbox } from './image-lightbox';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';

function isImage(name: string): boolean {
  const ext = name.split('.').pop()?.toLowerCase() ?? '';
  return ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif'].includes(ext);
}

function isTextLike(name: string): boolean {
  const ext = name.split('.').pop()?.toLowerCase() ?? '';
  return ['txt', 'log', 'json', 'md', 'csv', 'xml', 'yaml', 'yml', 'toml',
    'ts', 'tsx', 'js', 'jsx', 'go', 'py', 'rs', 'java', 'c', 'cpp', 'h',
    'css', 'html', 'sql', 'sh', 'bash', 'zsh'].includes(ext);
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}


interface AttachmentPreviewProps {
  attachment: ChatAttachment;
  workspaceId: string;
}

export function AttachmentPreview({ attachment, workspaceId }: AttachmentPreviewProps) {
  const { t } = useTranslation('workspaces');
  const [imgError, setImgError] = useState(false);
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [codePreview, setCodePreview] = useState<string | null>(null);
  const [codeExpanded, setCodeExpanded] = useState(false);
  const [codeTruncated, setCodeTruncated] = useState(false);
  const [codeLoading, setCodeLoading] = useState(false);
  const { togglePanel, openPanels } = useWorkspacePanelStore();

  // For @ ref files — green link tag (unchanged)
  if (attachment.type === 'ref') {
    return (
      <span
        className="inline-flex items-center gap-1 rounded-md bg-green-50 border border-green-200 px-2 py-0.5 text-xs text-green-700 cursor-pointer hover:bg-green-100 dark:bg-green-950/30 dark:border-green-800 dark:text-green-300 dark:hover:bg-green-950/50"
        onClick={() => {
          if (!openPanels.includes('files')) {
            togglePanel('files');
          }
        }}
      >
        <span className="font-medium">@</span>
        <span>{attachment.path.replace(/^\.worktrees\/[^/]+\//, '')}</span>
        <ExternalLink className="w-3 h-3 text-green-400" />
      </span>
    );
  }

  // Image files — real thumbnail + lightbox
  if (isImage(attachment.name) && !imgError) {
    const imgUrl = getFileContentUrl(workspaceId, attachment.path, 'raw');
    return (
      <>
        <div
          className="inline-block rounded-lg border border-border overflow-hidden max-w-[200px] cursor-pointer hover:border-info transition-colors"
          onClick={() => setLightboxOpen(true)}
        >
          <img
            src={imgUrl}
            alt={attachment.name}
            className="w-[200px] h-[130px] object-cover bg-muted"
            loading="lazy"
            onError={() => setImgError(true)}
          />
          <div className="px-2 py-1.5 bg-card">
            <div className="text-xs text-foreground truncate">{attachment.name}</div>
            {attachment.size != null && (
              <div className="text-[10px] text-muted-foreground/70">{t('panels.attachment.clickToZoom', { size: formatSize(attachment.size) })}</div>
            )}
          </div>
        </div>
        {lightboxOpen && (
          <ImageLightbox
            src={imgUrl}
            alt={attachment.name}
            onClose={() => setLightboxOpen(false)}
          />
        )}
      </>
    );
  }

  // Text/code files — content preview
  if (isTextLike(attachment.name)) {
    const loadPreview = async () => {
      if (codePreview !== null || codeLoading) return;
      setCodeLoading(true);
      try {
        const url = getFileContentUrl(workspaceId, attachment.path);
        const res = await fetch(url);
        if (res.ok) {
          const data = await res.json();
          setCodePreview(data.content);
          setCodeTruncated(data.truncated);
        }
      } catch { /* ignore */ } finally {
        setCodeLoading(false);
      }
    };

    return (
      <div className="rounded-lg border border-border overflow-hidden max-w-[400px]">
        {codePreview !== null && (
          <div className={cn(
            'bg-muted font-mono text-[11px] text-muted-foreground leading-relaxed px-3 py-2 overflow-hidden relative',
            !codeExpanded && 'max-h-[160px]',
          )}>
            <pre className="whitespace-pre-wrap break-all">{simpleHighlight(codePreview, attachment.name)}</pre>
            {!codeExpanded && codeTruncated && (
              <div className="absolute bottom-0 left-0 right-0 h-10 bg-gradient-to-t from-muted to-transparent" />
            )}
          </div>
        )}
        <div className="px-3 py-1.5 bg-card flex items-center justify-between border-t border-border">
          <div>
            <div className="text-xs text-foreground">{attachment.name}</div>
            {attachment.size != null && (
              <div className="text-[10px] text-muted-foreground/70">{formatSize(attachment.size)}</div>
            )}
          </div>
          <div className="flex items-center gap-1">
            {codePreview === null && (
              <button
                onClick={loadPreview}
                disabled={codeLoading}
                className="text-[10px] text-blue-500 hover:text-blue-600"
              >
                {codeLoading ? t('panels.attachment.loading') : t('panels.attachment.preview')}
              </button>
            )}
            {codePreview !== null && codeTruncated && (
              <button
                onClick={() => setCodeExpanded(!codeExpanded)}
                className="p-0.5 text-gray-400 hover:text-gray-600"
              >
                {codeExpanded ? <ChevronUp className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
              </button>
            )}
          </div>
        </div>
      </div>
    );
  }

  // Generic file card
  return (
    <div className="inline-flex items-center gap-2 rounded-lg border border-border px-3 py-2 bg-card">
      <span className="text-lg">📎</span>
      <div>
        <div className="text-xs text-foreground">{attachment.name}</div>
        {attachment.size != null && (
          <div className="text-[10px] text-muted-foreground/70">{formatSize(attachment.size)}</div>
        )}
      </div>
    </div>
  );
}
