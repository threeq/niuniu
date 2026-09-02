import { createHighlighterCore, type HighlighterCore } from 'shiki/core';
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript';
import { SYNTAX_THEME, SYNTAX_THEME_NAME, TOKEN_CLASS } from './theme';
import { loadLanguage, PLAIN_TEXT } from './languages';

/**
 * The tokenizer core — one shiki highlighter, grammars loaded on demand.
 *
 * Runs unchanged on the main thread and inside the worker (it imports nothing
 * DOM-shaped), which is what lets the worker be a pure optimization: if
 * `Worker` is unavailable — jsdom under vitest, a locked-down embedding — the
 * same functions are called directly and the output is identical.
 *
 * ENGINE CHOICE
 * -------------
 * `createJavaScriptRegexEngine` compiles Oniguruma patterns to native RegExp
 * instead of shipping the ~500kB WASM engine. Every grammar in
 * `languages.ts` was checked against it and loads with zero conversion
 * warnings, so the fidelity that WASM would buy is not being given up here.
 * `forgiving: true` is belt-and-braces: should a future grammar contain a
 * pattern the converter cannot express, that one pattern is skipped rather
 * than the whole file failing to highlight.
 */

/** One token of a highlighted line. */
export interface SyntaxToken {
  content: string;
  /**
   * Design-token utility class, or `undefined` for plain text (which inherits
   * the code cell's `text-foreground`). Never a literal color — see `theme.ts`.
   */
  className?: string;
}

/** Tokens for one line. An empty array is a blank line. */
export type SyntaxLine = SyntaxToken[];

let highlighterPromise: Promise<HighlighterCore> | null = null;

function getHighlighter(): Promise<HighlighterCore> {
  highlighterPromise ??= createHighlighterCore({
    themes: [SYNTAX_THEME],
    langs: [],
    engine: createJavaScriptRegexEngine({ forgiving: true }),
  });
  return highlighterPromise;
}

/** Grammars already loaded, so a repeat request doesn't re-import. */
const loaded = new Set<string>([PLAIN_TEXT]);
const loading = new Map<string, Promise<void>>();

/**
 * Ensure a grammar is registered. Concurrent requests for the same language
 * share one import — opening three Go files at once must not fetch the Go
 * grammar three times.
 *
 * Resolves to the language actually usable: {@link PLAIN_TEXT} if the grammar
 * is unknown or fails to load. A missing grammar degrades to uncolored code,
 * never to an error — see the unknown-extension requirement.
 */
export async function ensureLanguage(lang: string): Promise<string> {
  if (loaded.has(lang)) return lang;

  let pending = loading.get(lang);
  if (!pending) {
    pending = (async () => {
      const mod = await loadLanguage(lang);
      if (!mod) return;
      const hl = await getHighlighter();
      await hl.loadLanguage(mod as Parameters<HighlighterCore['loadLanguage']>[0]);
      loaded.add(lang);
    })().finally(() => loading.delete(lang));
    loading.set(lang, pending);
  }

  try {
    await pending;
  } catch {
    // Fall through to plain text: a grammar that fails to load is a degraded
    // rendering, not a broken view.
  }
  return loaded.has(lang) ? lang : PLAIN_TEXT;
}

/**
 * Opaque continuation of the grammar's stack across a chunk boundary. Held by
 * the caller and passed back to the next {@link tokenizeChunk} call; treated as
 * a token, never inspected.
 */
export type GrammarState = unknown;

export interface ChunkResult {
  lines: SyntaxLine[];
  /** Feed to the next chunk of the same file to continue mid-construct. */
  state: GrammarState;
}

/**
 * Tokenize a run of consecutive lines, resuming from `state`.
 *
 * WHY CHUNKS AND STATE, NOT WHOLE FILES OR SINGLE LINES
 * -----------------------------------------------------
 * TextMate grammars are stateful: whether a line sits inside a template
 * literal or a block comment depends on every line before it. That leaves
 * three options, and only one is both correct and fast:
 *
 *   - Whole file at once — correct, but ~155µs/line measured, so an 11k-line
 *     file is a 1.7s stall. Unacceptable even off the main thread, because the
 *     viewport waits on all of it.
 *   - Each line alone — fast, but this is precisely the old highlighter's bug:
 *     line 2 of a multi-line string is tokenized as if it were code.
 *   - Chunks carrying `grammarState` — this. Verified byte-identical to
 *     whole-file output while letting the first chunk paint immediately.
 *
 * `state` is `undefined` for the first chunk of a file (start of the grammar).
 */
export async function tokenizeChunk(
  code: string,
  lang: string,
  state?: GrammarState,
): Promise<ChunkResult> {
  const hl = await getHighlighter();
  const result = hl.codeToTokens(code, {
    lang,
    theme: SYNTAX_THEME_NAME,
    ...(state ? { grammarState: state as never } : {}),
  });

  const lines: SyntaxLine[] = result.tokens.map((line) =>
    line.map((token) => {
      const className = token.color ? TOKEN_CLASS[token.color.toLowerCase()] : undefined;
      return className ? { content: token.content, className } : { content: token.content };
    }),
  );

  return { lines, state: result.grammarState };
}
