import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from '@/lib/api';
import type {
  ContentSearchFile,
  ContentSearchOptions,
  WorkspaceFileHit,
} from '@/types/api';

/** File-name results come back in ~ms; content results can take seconds. Each
 *  side debounces on its own clock so a fast filename list is never held back
 *  waiting for grep. */
const NAME_DEBOUNCE_MS = 120;
const CONTENT_DEBOUNCE_MS = 280;

/** The dedicated search panel is a browsing surface, so it asks for more rows
 *  than the chat "@ file" popup's default (backend ceiling is 300). */
const NAME_RESULT_LIMIT = 200;

/** Content search shells out to a grep process per request; a single character
 *  would match nearly everything while telling the user almost nothing. Name
 *  search stays live from the first keystroke. */
const MIN_CONTENT_QUERY_LEN = 2;

/** Why a content search produced no usable result. Distinguishing these matters:
 *  "no engine" and "search failed" must never render like "nothing found". */
export type ContentSearchErrorKind = 'engine-missing' | 'invalid-query' | 'failed';

export interface ContentSearchError {
  kind: ContentSearchErrorKind;
  message: string;
}

export interface WorkspaceSearchState {
  query: string;
  setQuery: (q: string) => void;

  options: ContentSearchOptions;
  setOptions: (next: ContentSearchOptions) => void;

  /** File-name hits (fuzzy). */
  nameResults: WorkspaceFileHit[];
  nameLoading: boolean;

  /** Content hits, grouped by file. */
  contentResults: ContentSearchFile[];
  contentLoading: boolean;
  contentTotal: number;
  /** Backend cut the results short — the UI must say so. */
  contentTruncated: boolean;
  contentTruncatedReason?: 'limit' | 'timeout';
  contentError: ContentSearchError | null;
  /** Content search is idle because the query is below the minimum length. */
  contentBelowMinLength: boolean;

  reset: () => void;
}

/**
 * Drives the unified search panel: one query string fans out to BOTH the
 * file-name endpoint and the content-grep endpoint, with independent debounces,
 * loading flags and result slots.
 *
 * The two searches are deliberately not merged into a single await — content
 * search is one to two orders of magnitude slower, and blocking the (near
 * instant) filename list on it would make the whole panel feel broken.
 */
export function useWorkspaceSearch(workspaceId: string): WorkspaceSearchState {
  const [query, setQuery] = useState('');
  const [options, setOptions] = useState<ContentSearchOptions>({});

  const [nameResults, setNameResults] = useState<WorkspaceFileHit[]>([]);
  const [nameLoading, setNameLoading] = useState(false);

  const [contentResults, setContentResults] = useState<ContentSearchFile[]>([]);
  const [contentLoading, setContentLoading] = useState(false);
  const [contentTotal, setContentTotal] = useState(0);
  const [contentTruncated, setContentTruncated] = useState(false);
  const [contentTruncatedReason, setContentTruncatedReason] = useState<'limit' | 'timeout'>();
  const [contentError, setContentError] = useState<ContentSearchError | null>(null);

  const trimmed = query.trim();
  const contentBelowMinLength = trimmed.length > 0 && trimmed.length < MIN_CONTENT_QUERY_LEN;

  // Monotonic request ids: responses can arrive out of order (a slow "e" landing
  // after a fast "error"), and a stale one must never overwrite fresher results.
  const nameSeq = useRef(0);
  const contentSeq = useRef(0);

  const reset = useCallback(() => {
    // Bump both sequences so any in-flight response is discarded on arrival.
    nameSeq.current++;
    contentSeq.current++;
    setQuery('');
    setNameResults([]);
    setNameLoading(false);
    setContentResults([]);
    setContentLoading(false);
    setContentTotal(0);
    setContentTruncated(false);
    setContentTruncatedReason(undefined);
    setContentError(null);
  }, []);

  // --- file-name search ---------------------------------------------------
  useEffect(() => {
    if (!trimmed) {
      nameSeq.current++;
      setNameResults([]);
      setNameLoading(false);
      return;
    }

    const seq = ++nameSeq.current;
    setNameLoading(true);

    const timer = setTimeout(async () => {
      try {
        const res = await api.searchWorkspaceFiles(workspaceId, trimmed, NAME_RESULT_LIMIT);
        if (seq !== nameSeq.current) return;
        setNameResults((res.files ?? []).filter((f) => !f.isDir));
      } catch {
        if (seq !== nameSeq.current) return;
        // Name search has no dedicated error surface: it either lists files or
        // it doesn't, and the content half carries the visible failure state.
        setNameResults([]);
      } finally {
        if (seq === nameSeq.current) setNameLoading(false);
      }
    }, NAME_DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [workspaceId, trimmed]);

  // --- content search -----------------------------------------------------
  useEffect(() => {
    if (!trimmed || trimmed.length < MIN_CONTENT_QUERY_LEN) {
      contentSeq.current++;
      setContentResults([]);
      setContentLoading(false);
      setContentTotal(0);
      setContentTruncated(false);
      setContentTruncatedReason(undefined);
      setContentError(null);
      return;
    }

    const seq = ++contentSeq.current;
    setContentLoading(true);

    const timer = setTimeout(async () => {
      try {
        const res = await api.searchWorkspaceContent(workspaceId, trimmed, options);
        if (seq !== contentSeq.current) return;
        setContentResults(res.files ?? []);
        setContentTotal(res.totalMatches ?? 0);
        setContentTruncated(!!res.truncated);
        setContentTruncatedReason(res.truncatedReason);
        setContentError(null);
      } catch (err) {
        if (seq !== contentSeq.current) return;
        setContentResults([]);
        setContentTotal(0);
        setContentTruncated(false);
        setContentTruncatedReason(undefined);
        setContentError(classifyContentError(err));
      } finally {
        if (seq === contentSeq.current) setContentLoading(false);
      }
    }, CONTENT_DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [workspaceId, trimmed, options]);

  return {
    query,
    setQuery,
    options,
    setOptions,
    nameResults,
    nameLoading,
    contentResults,
    contentLoading,
    contentTotal,
    contentTruncated,
    contentTruncatedReason,
    contentError,
    contentBelowMinLength,
    reset,
  };
}

/** Maps a failed content request to a user-actionable kind. The 501 case (no
 *  ripgrep and no git on the host) gets its own kind so the panel can explain
 *  that the host cannot search, rather than implying an empty result. */
export function classifyContentError(err: unknown): ContentSearchError {
  if (err instanceof ApiError) {
    const code = (err.body as { error?: { code?: string } } | undefined)?.error?.code;
    if (err.status === 501 || code === 'SEARCH_ENGINE_MISSING') {
      return { kind: 'engine-missing', message: err.message };
    }
    if (err.status === 400) {
      // Almost always a malformed regex the user is still typing.
      return { kind: 'invalid-query', message: err.message };
    }
    return { kind: 'failed', message: err.message };
  }
  return { kind: 'failed', message: err instanceof Error ? err.message : String(err) };
}
