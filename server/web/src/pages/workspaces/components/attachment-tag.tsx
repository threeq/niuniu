import { X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import type { ChatAttachment } from '@/types/api';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';

function getFileIcon(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() ?? '';
  const imageExts = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'];
  const codeExts = ['ts', 'tsx', 'js', 'jsx', 'go', 'py', 'rs', 'java', 'c', 'cpp', 'h'];
  const textExts = ['txt', 'log', 'md', 'json', 'yaml', 'yml', 'csv', 'xml', 'toml'];
  if (imageExts.includes(ext)) return '🖼️';
  if (codeExts.includes(ext)) return '💻';
  if (textExts.includes(ext)) return '📝';
  return '📎';
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

interface AttachmentTagProps {
  attachment: ChatAttachment;
  onRemove?: () => void;
}

export function AttachmentTag({ attachment, onRemove }: AttachmentTagProps) {
  const { t } = useTranslation('workspaces');
  const isRef = attachment.type === 'ref';
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs',
        isRef
          ? 'bg-green-50 border-green-200 text-green-700 dark:bg-green-950/30 dark:border-green-800 dark:text-green-300'
          : 'bg-info/10 border-info/30 text-info',
      )}
    >
      {isRef ? (
        <span className="text-green-600 dark:text-green-400 font-medium">@</span>
      ) : (
        <span>{getFileIcon(attachment.name)}</span>
      )}
      {isRef && attachment.repo && (
        <span className="text-green-500 dark:text-green-400 text-[10px]">{attachment.repo}/</span>
      )}
      <span className="max-w-[180px] truncate">
        {isRef ? attachment.path.replace(/^\.worktrees\/[^/]+\//, '') : attachment.name}
      </span>
      {!isRef && attachment.size != null && (
        <span className="text-muted-foreground/70">{formatSize(attachment.size)}</span>
      )}
      {!isRef && attachment.optimized && attachment.originalSize != null && (
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="text-muted-foreground/70">
                · {t('panels.attachment.optimized.badge')}
              </span>
            </TooltipTrigger>
            <TooltipContent>
              {t('panels.attachment.optimized.tooltip', {
                orig: formatSize(attachment.originalSize),
                new: formatSize(attachment.size ?? 0),
              })}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      )}
      {onRemove && (
        <button onClick={onRemove} className="ml-0.5 p-0.5 rounded hover:bg-black/10">
          <X className="w-3 h-3" />
        </button>
      )}
    </span>
  );
}
