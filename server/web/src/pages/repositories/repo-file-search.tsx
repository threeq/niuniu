import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Search, X, File, GitCommitHorizontal, ChevronLeft, Loader2 } from 'lucide-react';
import { api } from '@/lib/api';
import { cn } from '@/lib/utils';
import { DiffViewer } from '@/pages/workspaces/panels/diff-viewer';
import type { GitFileDiff } from '@/lib/hooks/use-file-diff';
import type { RepoSearchResponse, FileLogEntry } from '@/types/api';

/**
 * Repository-scoped search over file NAMES and file CONTENT at once.
 *
 * One input, both kinds of result — the user never has to decide up front which
 * mode they want, mirroring the workspace search dialog. The two halves come
 * back in a single response, so a host without ripgrep still gets file-name
 * results (the content half degrades to a notice instead of failing the query).
 */
export function RepoSearchPanel({
  repoId,
  onOpenFile,
}: {
  repoId: string;
  onOpenFile: (path: string, line?: number) => void;
}) {
  const { t } = useTranslation('repositories');
  const [input, setInput] = useState('');
  // Only the SUBMITTED query hits the server. Content search shells out to
  // ripgrep, so firing it per keystroke would spawn a process per character.
  const [query, setQuery] = useState('');
  const [tab, setTab] = useState<'files' | 'content'>('files');

  const { data, isFetching } = useQuery<RepoSearchResponse>({
    queryKey: ['repository', repoId, 'search', query],
    queryFn: () =>
      api.get<RepoSearchResponse>(
        `/repositories/${repoId}/search?q=${encodeURIComponent(query)}`,
      ),
    enabled: !!repoId && query.trim().length > 0,
  });

  const contentFiles = data?.content?.files ?? [];
  const fileCount = data?.files.length ?? 0;
  const contentCount = data?.content?.totalMatches ?? 0;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setQuery(input);
  };

  const clear = () => {
    setInput('');
    setQuery('');
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <form onSubmit={submit} className="shrink-0 border-b border-border p-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder={t('detail.files.searchPlaceholder')}
            className="w-full rounded-md border border-border bg-background py-1.5 pl-7 pr-7 text-xs outline-none focus:border-ring"
          />
          {input && (
            <button
              type="button"
              onClick={clear}
              title={t('detail.files.searchClear')}
              className="absolute right-1.5 top-1/2 grid h-5 w-5 -translate-y-1/2 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
      </form>

      {query && (
        <div className="flex shrink-0 gap-1 border-b border-border px-2 py-1.5">
          {(['files', 'content'] as const).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => setTab(k)}
              className={cn(
                'rounded px-2 py-0.5 text-[11px] transition-colors',
                tab === k
                  ? 'bg-accent font-medium text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t(k === 'files' ? 'detail.files.searchTabFiles' : 'detail.files.searchTabContent')}
              <span className="ml-1 opacity-60">{k === 'files' ? fileCount : contentCount}</span>
            </button>
          ))}
          {isFetching && <Loader2 className="ml-auto h-3.5 w-3.5 animate-spin text-muted-foreground" />}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-auto p-1.5">
        {!query ? null : tab === 'files' ? (
          <FileHitList
            hits={data?.files ?? []}
            truncated={data?.filesTruncated ?? false}
            onOpenFile={onOpenFile}
          />
        ) : (
          <ContentHitList
            files={contentFiles}
            error={data?.contentError}
            truncated={data?.content?.truncated ?? false}
            onOpenFile={onOpenFile}
          />
        )}
      </div>
    </div>
  );
}

function FileHitList({
  hits,
  truncated,
  onOpenFile,
}: {
  hits: { path: string; name: string }[];
  truncated: boolean;
  onOpenFile: (path: string) => void;
}) {
  const { t } = useTranslation('repositories');
  if (hits.length === 0) {
    return <Empty text={t('detail.files.searchNoResults')} />;
  }
  return (
    <div className="space-y-0.5">
      {hits.map((h) => (
        <button
          key={h.path}
          type="button"
          onClick={() => onOpenFile(h.path)}
          className="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-xs hover:bg-accent"
        >
          <File className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate font-medium">{h.name}</span>
          <span className="ml-auto truncate text-[10px] text-muted-foreground">{h.path}</span>
        </button>
      ))}
      {truncated && <Note text={t('detail.files.searchTruncated')} />}
    </div>
  );
}

function ContentHitList({
  files,
  error,
  truncated,
  onOpenFile,
}: {
  files: { path: string; matches: { line: number; text: string }[] }[];
  error?: 'no_engine' | 'failed';
  truncated: boolean;
  onOpenFile: (path: string, line?: number) => void;
}) {
  const { t } = useTranslation('repositories');

  if (error) {
    return (
      <Note
        text={
          error === 'no_engine'
            ? t('detail.files.searchNoEngine')
            : t('detail.files.searchFailed')
        }
      />
    );
  }
  if (files.length === 0) {
    return <Empty text={t('detail.files.searchNoResults')} />;
  }

  return (
    <div className="space-y-2">
      {files.map((f) => (
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
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{m.line}</span>
              <code className="truncate font-mono text-[11px]">{m.text}</code>
            </button>
          ))}
        </div>
      ))}
      {truncated && <Note text={t('detail.files.searchTruncated')} />}
    </div>
  );
}

/**
 * Commit history for one file, following renames.
 *
 * Selecting a commit shows the diff it introduced. The request uses that
 * entry's `path_at_commit`, not the file's current path: past a rename the
 * current name does not exist at that commit, and asking for it would fail
 * rather than silently render an empty diff.
 */
export function FileHistoryPanel({ repoId, path }: { repoId: string; path: string }) {
  const { t } = useTranslation('repositories');
  const [selected, setSelected] = useState<FileLogEntry | null>(null);

  const { data: history, isLoading } = useQuery<FileLogEntry[]>({
    queryKey: ['repository', repoId, 'file-history', path],
    queryFn: () =>
      api.get<FileLogEntry[]>(
        `/repositories/${repoId}/files/history?path=${encodeURIComponent(path)}`,
      ),
    enabled: !!repoId && !!path,
  });

  const { data: diff, isLoading: diffLoading } = useQuery<GitFileDiff>({
    queryKey: ['repository', repoId, 'file-history-diff', selected?.hash, selected?.path_at_commit],
    queryFn: () =>
      api.get<GitFileDiff>(
        `/repositories/${repoId}/files/history/${selected!.hash}/diff?path=${encodeURIComponent(
          selected!.path_at_commit,
        )}`,
      ),
    enabled: !!selected,
  });

  if (selected) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-1.5">
          <button
            type="button"
            onClick={() => setSelected(null)}
            className="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <ChevronLeft className="h-3.5 w-3.5" />
            {t('detail.files.historyBack')}
          </button>
          <code className="font-mono text-xs text-info">{selected.hash.slice(0, 8)}</code>
          <span className="truncate text-xs text-muted-foreground">{selected.message}</span>
        </div>
        <div className="min-h-0 flex-1">
          {diffLoading ? (
            <Empty text={t('detail.files.historyLoading')} />
          ) : diff ? (
            // Comments are not wired: they anchor to a workspace worktree, and
            // this is a bare repository view. No callbacks = read-only viewer.
            <DiffViewer fileDiff={diff} repoName="" mode="unified" />
          ) : (
            <Empty text={t('detail.files.historyDiffEmpty')} />
          )}
        </div>
      </div>
    );
  }

  if (isLoading) return <Empty text={t('detail.files.historyLoading')} />;
  if (!history || history.length === 0) return <Empty text={t('detail.files.historyEmpty')} />;

  return (
    <div className="h-full overflow-auto p-1.5">
      {history.map((e) => (
        <button
          key={e.hash}
          type="button"
          onClick={() => setSelected(e)}
          className="flex w-full flex-col gap-0.5 rounded px-2 py-1.5 text-left hover:bg-accent"
        >
          <div className="flex items-center gap-2">
            <GitCommitHorizontal className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate text-xs font-medium">{e.message}</span>
          </div>
          <div className="flex items-center gap-2 pl-5 text-[10px] text-muted-foreground">
            <code className="font-mono">{e.hash.slice(0, 8)}</code>
            <span className="truncate">{e.author}</span>
            <span className="shrink-0">{formatDate(e.date)}</span>
          </div>
          {/* Surfacing the old name makes a rename visible instead of a gap. */}
          {e.path_at_commit !== path && (
            <div className="truncate pl-5 text-[10px] text-warning">
              {t('detail.files.historyRenamed', { path: e.path_at_commit })}
            </div>
          )}
        </button>
      ))}
    </div>
  );
}

/** Renders the git ISO-ish date without pulling in a date library. */
function formatDate(raw: string): string {
  const d = new Date(raw);
  return Number.isNaN(d.getTime()) ? raw : d.toLocaleString();
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
