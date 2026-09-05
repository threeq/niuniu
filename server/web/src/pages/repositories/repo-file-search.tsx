import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Search, X, File, GitCommitHorizontal, Loader2, ArrowLeft } from 'lucide-react';
import { api } from '@/lib/api';
import { cn } from '@/lib/utils';
import type { RepoSearchResponse, FileLogEntry } from '@/types/api';

/**
 * Repository-scoped search over file NAMES and file CONTENT at once.
 *
 * Deliberately NOT split into tabs: both kinds of hit land in one list, so the
 * user never has to decide up front which sort of search they are doing. The two
 * halves arrive in a single response, and a host without ripgrep still gets
 * file-name results (the content half degrades to a notice, not a failed query).
 */
export function RepoSearchBox({
  value,
  onChange,
  onSubmit,
  onClear,
  busy,
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
  onClear: () => void;
  busy?: boolean;
}) {
  const { t } = useTranslation('repositories');
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
      className="shrink-0 border-b border-border p-2"
    >
      <div className="relative">
        <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={t('detail.files.searchPlaceholder')}
          className="w-full rounded-md border border-border bg-background py-1.5 pl-7 pr-7 text-xs outline-none focus:border-ring"
        />
        {busy ? (
          <Loader2 className="absolute right-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 animate-spin text-muted-foreground" />
        ) : (
          value && (
            <button
              type="button"
              onClick={onClear}
              title={t('detail.files.searchClear')}
              className="absolute right-1.5 top-1/2 grid h-5 w-5 -translate-y-1/2 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )
        )}
      </div>
    </form>
  );
}

/**
 * The unified result list: file-name hits and content hits in one scroller,
 * separated by lightweight section labels rather than tabs.
 */
export function RepoSearchResults({
  repoId,
  query,
  onOpenFile,
  onBackToFiles,
}: {
  repoId: string;
  query: string;
  onOpenFile: (path: string, line?: number) => void;
  onBackToFiles: () => void;
}) {
  const { t } = useTranslation('repositories');

  const { data, isLoading } = useQuery<RepoSearchResponse>({
    queryKey: ['repository', repoId, 'search', query],
    queryFn: () =>
      api.get<RepoSearchResponse>(`/repositories/${repoId}/search?q=${encodeURIComponent(query)}`),
    enabled: !!repoId && query.trim().length > 0,
    // Results stay put while the query text is unchanged, so switching to the
    // file list and back does not re-run ripgrep.
    staleTime: 5 * 60 * 1000,
  });

  const fileHits = data?.files ?? [];
  const contentFiles = data?.content?.files ?? [];
  const total = fileHits.length + (data?.content?.totalMatches ?? 0);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-2 py-1.5">
        <button
          type="button"
          onClick={onBackToFiles}
          className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="h-3 w-3" />
          {t('detail.files.searchBackToFiles')}
        </button>
        <span className="ml-auto text-[10px] text-muted-foreground">
          {t('detail.files.searchResultCount', { count: total })}
        </span>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-1.5">
        {isLoading ? (
          <Empty text={t('detail.files.loading')} />
        ) : total === 0 && !data?.contentError ? (
          <Empty text={t('detail.files.searchNoResults')} />
        ) : (
          <>
            {fileHits.length > 0 && (
              <>
                <SectionLabel text={t('detail.files.searchTabFiles')} />
                <div className="space-y-0.5">
                  {fileHits.map((h) => (
                    <button
                      key={h.path}
                      type="button"
                      onClick={() => onOpenFile(h.path)}
                      className="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-xs hover:bg-accent"
                    >
                      <File className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate font-medium">{h.name}</span>
                      <span className="ml-auto truncate text-[10px] text-muted-foreground">
                        {h.path}
                      </span>
                    </button>
                  ))}
                </div>
                {data?.filesTruncated && <Note text={t('detail.files.searchTruncated')} />}
              </>
            )}

            {data?.contentError && (
              <Note
                text={
                  data.contentError === 'no_engine'
                    ? t('detail.files.searchNoEngine')
                    : t('detail.files.searchFailed')
                }
              />
            )}

            {contentFiles.length > 0 && (
              <>
                <SectionLabel text={t('detail.files.searchTabContent')} />
                <div className="space-y-2">
                  {contentFiles.map((f) => (
                    <div key={f.path}>
                      <div className="truncate px-2 py-0.5 text-[10px] font-medium text-muted-foreground">
                        {f.path}
                      </div>
                      {f.matches.map((m) => (
                        <button
                          key={`${f.path}:${m.line}`}
                          type="button"
                          onClick={() => onOpenFile(f.path, m.line)}
                          className="flex w-full items-start gap-2 rounded px-2 py-0.5 text-left hover:bg-accent"
                        >
                          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                            {m.line}
                          </span>
                          <code className="truncate font-mono text-[11px]">{m.text}</code>
                        </button>
                      ))}
                    </div>
                  ))}
                </div>
                {data?.content?.truncated && <Note text={t('detail.files.searchTruncated')} />}
              </>
            )}
          </>
        )}
      </div>
    </div>
  );
}

/**
 * Commit history for one file, as a closable right-hand sidebar.
 *
 * This panel only LISTS commits; selecting one raises it to the parent, which
 * renders the diff in the main content area. Keeping the history visible while
 * the diff is shown is the point — it is a navigation rail, not a replacement
 * for the file view.
 */
export function FileHistorySidebar({
  repoId,
  path,
  selectedHash,
  onSelect,
  onClose,
}: {
  repoId: string;
  path: string;
  selectedHash?: string;
  onSelect: (entry: FileLogEntry) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation('repositories');

  const { data: history, isLoading } = useQuery<FileLogEntry[]>({
    queryKey: ['repository', repoId, 'file-history', path],
    queryFn: () =>
      api.get<FileLogEntry[]>(
        `/repositories/${repoId}/files/history?path=${encodeURIComponent(path)}`,
      ),
    enabled: !!repoId && !!path,
  });

  return (
    <div className="flex h-full min-h-0 w-72 shrink-0 flex-col border-l border-border bg-card">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5">
        <GitCommitHorizontal className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium">{t('detail.files.historyTab')}</span>
        <button
          type="button"
          onClick={onClose}
          title={t('detail.files.historyClose')}
          className="ml-auto grid h-5 w-5 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-1.5">
        {isLoading ? (
          <Empty text={t('detail.files.historyLoading')} />
        ) : !history || history.length === 0 ? (
          <Empty text={t('detail.files.historyEmpty')} />
        ) : (
          history.map((e) => (
            <button
              key={e.hash}
              type="button"
              onClick={() => onSelect(e)}
              className={cn(
                'flex w-full flex-col gap-0.5 rounded px-2 py-1.5 text-left transition-colors',
                selectedHash === e.hash ? 'bg-info/10' : 'hover:bg-accent',
              )}
            >
              <span className="truncate text-xs font-medium">{e.message}</span>
              <span className="flex items-center gap-2 text-[10px] text-muted-foreground">
                <code className="font-mono">{e.hash.slice(0, 8)}</code>
                <span className="truncate">{e.author}</span>
              </span>
              <span className="text-[10px] text-muted-foreground">{formatDate(e.date)}</span>
              {/* Surfacing the old name makes a rename visible instead of a gap. */}
              {e.path_at_commit !== path && (
                <span className="truncate text-[10px] text-warning">
                  {t('detail.files.historyRenamed', { path: e.path_at_commit })}
                </span>
              )}
            </button>
          ))
        )}
      </div>
    </div>
  );
}

/** Renders the git ISO-ish date without pulling in a date library. */
function formatDate(raw: string): string {
  const d = new Date(raw);
  return Number.isNaN(d.getTime()) ? raw : d.toLocaleString();
}

function SectionLabel({ text }: { text: string }) {
  return (
    <div className="px-2 pb-0.5 pt-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
      {text}
    </div>
  );
}

function Empty({ text }: { text: string }) {
  return (
    <div className="flex h-full items-center justify-center py-8 text-xs text-muted-foreground">
      {text}
    </div>
  );
}

function Note({ text }: { text: string }) {
  return <div className="px-2 py-2 text-[11px] text-muted-foreground">{text}</div>;
}
