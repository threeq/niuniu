import type { ReactNode } from 'react';
import type { SyntaxLine } from './tokenizer';

/**
 * Token → React nodes.
 *
 * Kept apart from the hook that produces tokens so each module has one kind of
 * export (react-refresh requires a file to export either only components or
 * only plain values — the same reason `lib/file-type.ts` is its own module).
 */

/** Keeps a blank line's row at full height instead of collapsing to zero. */
const ZERO_WIDTH_SPACE = '​';

/**
 * Render a tokenized line.
 *
 * `fallback` is the raw text, used whenever tokens are absent — a chunk still
 * in flight, a file with no grammar, or a failed highlight. Code is therefore
 * legible from first paint and gains color as it arrives; highlighting never
 * gates whether the text appears.
 *
 * Every class emitted here comes from `TOKEN_CLASS`, i.e. from the `--syntax-*`
 * design tokens. There is no path through this function that produces a literal
 * color.
 */
export function renderTokens(tokens: SyntaxLine | undefined, fallback: string): ReactNode {
  if (!tokens) return fallback.length > 0 ? fallback : ZERO_WIDTH_SPACE;
  if (tokens.length === 0) return ZERO_WIDTH_SPACE;

  return tokens.map((token, i) =>
    token.className ? (
      <span key={i} className={token.className}>
        {token.content}
      </span>
    ) : (
      token.content
    ),
  );
}
