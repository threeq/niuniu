/**
 * Syntax highlighting — the single implementation for the whole app.
 *
 * Replaces the two hand-rolled regex highlighters this module was created to
 * retire (`lib/syntax-highlight.tsx`, used by the diff and file views, and
 * `lib/simple-highlight.tsx`, used by attachment previews). Those shared one
 * ~70-word keyword list across every language, had no grammar and no notion of
 * file type, and disagreed with each other on the same code — one of them also
 * hardcoded colors in violation of docs/design-system.md §2.5.1.
 *
 * What replaces them:
 *   - real TextMate grammars (shiki, same source as VSCode), picked per file
 *   - colors resolved from the `--syntax-*` design tokens (see `theme.ts`)
 *   - tokenization off the main thread, chunked and progressive
 *   - unknown extensions degrade to plain text, never to an error
 */

export { useSyntaxHighlight } from './use-syntax-highlight';
export type { HighlightMap, UseSyntaxHighlightOptions } from './use-syntax-highlight';
export { renderTokens } from './render-tokens';
export { HighlightedCode } from './highlighted-code';
export type { HighlightedCodeProps } from './highlighted-code';
export { useDiffHighlight } from './use-diff-highlight';
export type { DiffHighlightMap } from './use-diff-highlight';
export { languageForPath, isPlainText, PLAIN_TEXT } from './languages';
export type { SyntaxToken, SyntaxLine } from './tokenizer';
