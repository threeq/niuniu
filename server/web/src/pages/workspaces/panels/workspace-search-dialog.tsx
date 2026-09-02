import { useCallback, useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, CaseSensitive, File, Loader2, Regex, WholeWord } from 'lucide-react';
import { Command, CommandEmpty, CommandGroup, CommandItem, CommandList } from '@/components/ui/command';
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog';
import { cn } from '@/lib/utils';
import { useWorkspaceSearch } from '@/lib/hooks/use-workspace-search';
import { splitByColumns } from '@/lib/hooks/use-workspace-search-shortcut';
import { contentTargetForPath, useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import type { ContentSearchFile, ContentSearchMatch } from '@/types/api';

interface WorkspaceSearchDialogProps {
  workspaceId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * The workspace's single search entry point. One input fans out to BOTH
 * file-name (fuzzy) and file-content (grep) search, showing each group as it
 * arrives — the user never has to decide which kind of search they want before
 * they can start typing.
 *
 * Name results land in ~ms and render immediately; content results follow when
 * grep returns, with their own spinner, so the slow half never blocks the fast
 * half (see use-workspace-search).
 */
export function WorkspaceSearchDialog({
  workspaceId,
  open,
  onOpenChange,
}: WorkspaceSearchDialogProps) {
  const { t } = useTranslation('workspaces');
  const openContentViewer = useWorkspacePanelStore((s) => s.openContentViewer);
  const search = useWorkspaceSearch(workspaceId);
  const { reset } = search;

  // Clear the previous query when the dialog closes so re-opening starts fresh
  // rather than flashing a stale result list.
  useEffect(() => {
    if (!open) reset();
  }, [open, reset]);

  const openAt = useCallback(
    (path: string, line?: number) => {
      openContentViewer(workspaceId, contentTargetForPath(path, undefined, line));
      onOpenChange(false);
    },
    [openContentViewer, workspaceId, onOpenChange],
  );

  const hasQuery = search.query.trim().length > 0;
  const showEmpty =
    hasQuery &&
    !search.nameLoading &&
    !search.contentLoading &&
    search.nameResults.length === 0 &&
    search.contentResults.length === 0 &&
    !search.contentError;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="overflow-hidden p-0 sm:max-w-2xl">
        <DialogTitle className="sr-only">{t('search.title')}</DialogTitle>
        <DialogDescription className="sr-only">{t('search.description')}</DialogDescription>

        {/* shouldFilter={false}: both result sets are already filtered by the
            backend (fuzzy match / grep). cmdk's own scoring would re-filter and
            silently drop legitimate content hits whose file path doesn't
            contain the query. */}
        <Command shouldFilter={false} loop>
          <SearchInput
            value={search.query}
            onChange={search.setQuery}
            placeholder={t('search.placeholder')}
          />

          <ContentOptionsBar
            options={search.options}
            onChange={search.setOptions}
            disabled={!hasQuery}
          />

          <CommandList className="max-h-[60vh]">
            {!hasQuery && (
              <div className="px-4 py-8 text-center text-xs text-muted-foreground">
                {t('search.hint')}
              </div>
            )}

            {showEmpty && <CommandEmpty>{t('search.noResults')}</CommandEmpty>}

            {/* --- file names: fast, so it renders first --- */}
            {hasQuery && (
              <CommandGroup
                heading={
                  <GroupHeading
                    label={t('search.filesHeading')}
                    count={search.nameResults.length}
                    loading={search.nameLoading}
                  />
                }
              >
                {search.nameResults.map((f) => (
                  <CommandItem
                    key={`name:${f.path}`}
                    value={`name:${f.path}`}
                    onSelect={() => openAt(f.path)}
                    className="gap-2"
                  >
                    <File className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                    <span className="shrink-0 font-mono text-xs text-foreground">{f.name}</span>
                    <span className="truncate font-mono text-[11px] text-muted-foreground">
                      {f.path}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            )}

            {/* --- file contents: slower, arrives after --- */}
            {hasQuery && (
              <CommandGroup
                heading={
                  <GroupHeading
                    label={t('search.contentHeading')}
                    count={search.contentTotal}
                    loading={search.contentLoading}
                  />
                }
              >
                <ContentStatus search={search} />
                {search.contentResults.map((file) => (
                  <ContentFileMatches key={`content:${file.path}`} file={file} onOpen={openAt} />
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  );
}

/** The search input. Hand-rolled rather than reusing CommandInput because the
 *  results are backend-filtered, so cmdk's internal value binding is bypassed. */
function SearchInput({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  return (
    <div className="flex items-center gap-2 border-b border-border px-3">
      <input
        autoFocus
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-11 w-full bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
        // cmdk owns ArrowUp/ArrowDown/Enter for list navigation; letting the
        // input handle them too would move the caret and the selection at once.
        onKeyDown={(e) => {
          if (e.key === 'ArrowUp' || e.key === 'ArrowDown') e.preventDefault();
        }}
      />
    </div>
  );
}

/** Case / whole-word / regex toggles. These only affect content search — name
 *  search is always fuzzy — so the bar is labelled as such. */
function ContentOptionsBar({
  options,
  onChange,
  disabled,
}: {
  options: { caseSensitive?: boolean; wholeWord?: boolean; regex?: boolean };
  onChange: (next: { caseSensitive?: boolean; wholeWord?: boolean; regex?: boolean }) => void;
  disabled: boolean;
}) {
  const { t } = useTranslation('workspaces');
  const toggles = [
    { key: 'caseSensitive' as const, icon: CaseSensitive, label: t('search.optionCase') },
    { key: 'wholeWord' as const, icon: WholeWord, label: t('search.optionWord') },
    { key: 'regex' as const, icon: Regex, label: t('search.optionRegex') },
  ];

  return (
    <div className="flex items-center gap-1 border-b border-border px-3 py-1.5">
      <span className="mr-1 text-[11px] text-muted-foreground">{t('search.optionsLabel')}</span>
      {toggles.map(({ key, icon: Icon, label }) => (
        <button
          key={key}
          type="button"
          disabled={disabled}
          aria-pressed={!!options[key]}
          title={label}
          aria-label={label}
          onClick={() => onChange({ ...options, [key]: !options[key] })}
          className={cn(
            'rounded p-1 transition-colors disabled:opacity-40',
            options[key]
              ? 'bg-brand-soft text-brand'
              : 'text-muted-foreground hover:bg-accent hover:text-foreground',
          )}
        >
          <Icon className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      ))}
    </div>
  );
}

function GroupHeading({
  label,
  count,
  loading,
}: {
  label: string;
  count: number;
  loading: boolean;
}) {
  return (
    <span className="flex items-center gap-1.5">
      {label}
      {loading ? (
        <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" aria-hidden="true" />
      ) : (
        count > 0 && <span className="text-muted-foreground tabular-nums">{count}</span>
      )}
    </span>
  );
}

/**
 * Content-search status line: errors, truncation notices and the
 * below-minimum-length hint.
 *
 * A failed or unavailable content search MUST be visible here. Rendering
 * nothing would make "this host has no grep" indistinguishable from "there are
 * no matches", which is exactly the confusion this panel exists to avoid.
 */
function ContentStatus({ search }: { search: ReturnType<typeof useWorkspaceSearch> }) {
  const { t } = useTranslation('workspaces');

  if (search.contentBelowMinLength) {
    return <StatusRow tone="muted">{t('search.contentMinLength')}</StatusRow>;
  }

  if (search.contentError) {
    const { kind, message } = search.contentError;
    if (kind === 'engine-missing') {
      return <StatusRow tone="warning">{t('search.engineMissing')}</StatusRow>;
    }
    if (kind === 'invalid-query') {
      return <StatusRow tone="warning">{t('search.invalidQuery')}</StatusRow>;
    }
    return <StatusRow tone="warning">{t('search.contentFailed', { message })}</StatusRow>;
  }

  if (search.contentTruncated) {
    return (
      <StatusRow tone="muted">
        {search.contentTruncatedReason === 'timeout'
          ? t('search.truncatedTimeout')
          : t('search.truncatedLimit')}
      </StatusRow>
    );
  }

  return null;
}

function StatusRow({
  tone,
  children,
}: {
  tone: 'muted' | 'warning';
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        'flex items-center gap-1.5 px-2 py-1.5 text-[11px]',
        tone === 'warning' ? 'text-warning' : 'text-muted-foreground',
      )}
    >
      {tone === 'warning' && <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden="true" />}
      <span>{children}</span>
    </div>
  );
}

/** One file's content matches: a path header followed by one row per hit. */
function ContentFileMatches({
  file,
  onOpen,
}: {
  file: ContentSearchFile;
  onOpen: (path: string, line: number) => void;
}) {
  const { t } = useTranslation('workspaces');
  return (
    <>
      <div className="flex items-center gap-1.5 px-2 pb-0.5 pt-2 font-mono text-[11px] text-muted-foreground">
        <span className="truncate">{file.path}</span>
        {file.truncated && (
          <span className="shrink-0 text-warning">{t('search.fileTruncated')}</span>
        )}
      </div>
      {file.matches.map((m) => (
        <CommandItem
          key={`${file.path}:${m.line}`}
          value={`content:${file.path}:${m.line}`}
          onSelect={() => onOpen(file.path, m.line)}
          className="items-start gap-2"
        >
          <span className="w-10 shrink-0 pt-px text-right font-mono text-[11px] tabular-nums text-muted-foreground">
            {m.line}
          </span>
          <MatchLine match={m} />
        </CommandItem>
      ))}
    </>
  );
}

/** A match line with its hit spans highlighted, plus surrounding context. */
function MatchLine({ match }: { match: ContentSearchMatch }) {
  const segments = useMemo(() => splitByColumns(match.text, match.columns), [match]);
  return (
    <span className="min-w-0 flex-1 font-mono text-[11px] leading-4">
      {match.before?.map((line, i) => (
        <span key={`b${i}`} className="block truncate text-muted-foreground">
          {line || '​'}
        </span>
      ))}
      <span className="block truncate text-foreground">
        {segments.map((seg, i) =>
          seg.hit ? (
            <mark key={i} className="rounded-sm bg-brand-soft px-0.5 text-brand">
              {seg.text}
            </mark>
          ) : (
            <span key={i}>{seg.text}</span>
          ),
        )}
        {match.lineTruncated && <span className="text-muted-foreground">…</span>}
      </span>
      {match.after?.map((line, i) => (
        <span key={`a${i}`} className="block truncate text-muted-foreground">
          {line || '​'}
        </span>
      ))}
    </span>
  );
}
