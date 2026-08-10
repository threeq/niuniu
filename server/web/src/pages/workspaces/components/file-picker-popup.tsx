import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { Loader2 } from 'lucide-react';

interface FileResult {
  path: string;
  name: string;
  repo: string;
  isDir: boolean;
}

interface FilePickerPopupProps {
  results: FileResult[];
  selectedIndex: number;
  loading: boolean;
  query: string;
  onSelect: (file: FileResult) => void;
  onClose: () => void;
}

export function FilePickerPopup({ results, selectedIndex, loading, query, onSelect }: FilePickerPopupProps) {
  const { t } = useTranslation('workspaces');
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = listRef.current?.children[selectedIndex] as HTMLElement;
    el?.scrollIntoView({ block: 'nearest' });
  }, [selectedIndex]);

  return (
    <div className="w-full bg-card border border-border rounded-lg shadow-lg overflow-hidden">
      <div className="px-3 py-1.5 border-b border-border text-[11px] text-muted-foreground/70 flex items-center justify-between">
        <span>{t('panels.filePicker.header')}</span>
        {loading && <Loader2 className="w-3 h-3 animate-spin" />}
      </div>
      <div ref={listRef} className="max-h-[200px] overflow-y-auto">
        {results.length === 0 && !loading && (
          <div className="px-3 py-4 text-center text-xs text-muted-foreground/70">
            {query ? t('panels.filePicker.noResults') : t('panels.filePicker.searchHint')}
          </div>
        )}
        {results.map((file, idx) => (
          <button
            key={file.path}
            onClick={() => onSelect(file)}
            className={cn(
              'w-full text-left px-3 py-1.5 text-sm flex items-center gap-2 cursor-pointer',
              idx === selectedIndex ? 'bg-accent text-foreground' : 'text-muted-foreground hover:bg-muted',
            )}
          >
            <span className="text-muted-foreground/70 text-xs">📄</span>
            {file.repo && (
              <span className="text-[10px] text-muted-foreground/70 bg-muted rounded px-1 py-0.5 shrink-0">
                {file.repo}
              </span>
            )}
            <span className="truncate">
              {highlightMatch(file.repo ? file.path.replace(`.worktrees/${file.repo}/`, '') : file.path, query)}
            </span>
          </button>
        ))}
      </div>
      <div className="px-3 py-1 border-t border-border text-[11px] text-muted-foreground/70 flex justify-between">
        <span>{t('panels.filePicker.navHint')}</span>
        <span>{t('panels.filePicker.cancelHint')}</span>
      </div>
    </div>
  );
}

function highlightMatch(text: string, query: string): React.ReactNode {
  if (!query) return text;
  const lowerText = text.toLowerCase();
  const lowerQuery = query.toLowerCase();
  const idx = lowerText.indexOf(lowerQuery);
  if (idx === -1) return text;
  return (
    <>
      {text.slice(0, idx)}
      <span className="text-blue-500 font-medium">{text.slice(idx, idx + query.length)}</span>
      {text.slice(idx + query.length)}
    </>
  );
}
