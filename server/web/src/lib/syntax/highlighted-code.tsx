import { useMemo } from 'react';
import { cn } from '@/lib/utils';
import { useSyntaxHighlight } from './use-syntax-highlight';
import { renderTokens } from './render-tokens';

/**
 * A read-only block of highlighted code.
 *
 * The simple counterpart to `CodeSurface`: no windowing, no gutter
 * affordances, no comments — for previews that show a bounded snippet
 * (attachment previews, and anywhere else a short file needs to look right).
 * `CodeSurface` remains the surface for anything scrollable or reviewable.
 *
 * Both go through the same `lib/syntax` tokenizer, which is the point: the
 * previous split had attachment previews on a separate 20-keyword highlighter
 * with hardcoded Tailwind colors, so the same file looked different depending
 * on where you opened it, and the preview violated the design system's
 * no-hardcoded-color rule. Now there is one grammar and one palette.
 */

export interface HighlightedCodeProps {
  code: string;
  /** Used to pick the grammar; unknown types render as plain text. */
  path: string;
  className?: string;
}

export function HighlightedCode({ code, path, className }: HighlightedCodeProps) {
  const normalized = useMemo(() => code.replace(/\r\n?/g, '\n'), [code]);
  const lines = useMemo(() => normalized.split('\n'), [normalized]);
  const highlight = useSyntaxHighlight({ code: normalized, path });

  return (
    <pre className={cn('whitespace-pre-wrap break-all', className)}>
      {lines.map((line, i) => (
        <div key={i}>{renderTokens(highlight(i), line)}</div>
      ))}
    </pre>
  );
}
