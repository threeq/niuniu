import { useEffect, useMemo, useRef, useState } from 'react';
import { languageForPath, PLAIN_TEXT } from './languages';
import { nextHighlightId, requestHighlight } from './client';
import type { SyntaxLine } from './tokenizer';

/**
 * `useSyntaxHighlight` — the one way UI code gets syntax colors.
 *
 * Hand it a file's text and path; get back a lookup from line index to tokens.
 * Lines not yet tokenized (or in a file that gets no highlighting at all)
 * simply return `undefined`, and the caller renders the raw text — so code is
 * always readable immediately and gains color as chunks arrive, rather than
 * blocking on a highlight pass.
 */

/** Tokens by 0-based line index; `undefined` where not (yet) available. */
export type HighlightMap = (index: number) => SyntaxLine | undefined;

const NO_HIGHLIGHT: HighlightMap = () => undefined;

export interface UseSyntaxHighlightOptions {
  /** Full file text. Callers should pass LF-normalized content. */
  code: string;
  /** Path used to pick the grammar. */
  path: string;
  /** Set false to skip highlighting entirely (e.g. a binary or huge file). */
  enabled?: boolean;
  /**
   * 0-based line indices where the grammar restarts clean. Omit for a plain
   * file; a diff passes one per hunk. See `tokenizeDocument` for why.
   *
   * Compared by value (joined), not identity, so a caller may rebuild the array
   * on every render without retriggering the highlight.
   */
  resets?: number[];
}

export function useSyntaxHighlight({
  code,
  path,
  enabled = true,
  resets,
}: UseSyntaxHighlightOptions): HighlightMap {
  const lang = useMemo(() => languageForPath(path), [path]);
  const active = enabled && lang !== PLAIN_TEXT && code.length > 0;

  // Value-identity for the reset list: the effect below must re-run when the
  // hunk boundaries actually move, not merely because a caller allocated a new
  // array with the same contents.
  const resetKey = resets?.join(',') ?? '';

  // Tokens live in a ref, not state: a large file arrives as many chunks, and
  // storing an ever-growing array in state would re-render the whole surface on
  // each one. Instead chunks accumulate here and a single counter bump tells
  // React that *something* changed — the surface re-reads only its visible rows.
  const linesRef = useRef<SyntaxLine[]>([]);
  const [version, setVersion] = useState(0);

  // One subscription id per hook instance, stable across files.
  const idRef = useRef<number | null>(null);
  idRef.current ??= nextHighlightId();

  useEffect(() => {
    linesRef.current = [];
    setVersion((v) => v + 1);
    if (!active) return;

    const id = idRef.current!;
    const cancel = requestHighlight(
      id,
      lang,
      code,
      {
        onChunk(from, lines) {
          const target = linesRef.current;
          for (let i = 0; i < lines.length; i++) target[from + i] = lines[i];
          setVersion((v) => v + 1);
        },
        onError() {
          // Leave whatever arrived before the failure in place; the rest of the
          // file stays plain. Never blank out code over a highlighting problem.
        },
      },
      resetKey === '' ? undefined : resetKey.split(',').map(Number),
    );
    return cancel;
    // `resets` is tracked through `resetKey`, its by-value identity.
  }, [active, lang, code, resetKey]);

  return useMemo(() => {
    if (!active) return NO_HIGHLIGHT;
    // `version` is the dependency that matters — it changes as chunks land,
    // giving callers a new function identity to re-read through.
    void version;
    return (index: number) => linesRef.current[index];
  }, [active, version]);
}
