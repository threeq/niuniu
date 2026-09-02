/**
 * The bridge between shiki's color-based output and the design system's
 * `--syntax-*` tokens.
 *
 * THE PROBLEM
 * -----------
 * Shiki themes assign literal colors, and `codeToTokens` hands back a
 * `color: '#rrggbb'` per token. But docs/design-system.md §2.5.1 forbids
 * hardcoded colors: syntax colors must come from `--syntax-keyword` /
 * `-string` / `-comment` / `-number`, which are HSL triples that flip between
 * light and dark themes. A shiki theme cannot reference a CSS variable — it
 * only speaks hex.
 *
 * THE APPROACH
 * ------------
 * Use hex purely as an *identifier*, never as a color. The theme paints each
 * scope one of four sentinel values that are never rendered; the renderer looks
 * the sentinel up in {@link TOKEN_CLASS} and emits the corresponding Tailwind
 * class, which resolves to the CSS variable at paint time.
 *
 * So the four categories the old regex highlighter recognized are preserved
 * exactly — same tokens, same variables, same light/dark switching — while what
 * *decides* a token's category becomes a real TextMate grammar instead of one
 * shared 70-word keyword list. Nothing here needs to change when the palette
 * changes; `index.css` remains the single source of color truth.
 */

import type { ThemeRegistrationRaw } from 'shiki/core';

/**
 * Sentinel foregrounds. Arbitrary, only required to be distinct and to never
 * collide with a real color — they are compared, never painted.
 */
export const SENTINEL = {
  keyword: '#000001',
  string: '#000002',
  comment: '#000003',
  number: '#000004',
} as const;

/** Sentinel → the design-token utility class that actually colors the token. */
export const TOKEN_CLASS: Record<string, string> = {
  [SENTINEL.keyword]: 'text-syntax-keyword',
  [SENTINEL.string]: 'text-syntax-string',
  // Italic matches what both retired highlighters did for comments.
  [SENTINEL.comment]: 'text-syntax-comment italic',
  [SENTINEL.number]: 'text-syntax-number',
};

/**
 * Plain foreground: everything the four categories don't claim. It must differ
 * from all four sentinels, and it deliberately maps to no class — such tokens
 * inherit `text-foreground` from the code cell, exactly as unmatched text did
 * under the regex highlighter.
 */
const PLAIN = '#000000';

/**
 * The theme itself.
 *
 * Scope selection follows the same principle the old highlighter stated: color
 * the *lexical* categories that read well in any language, and leave the rest
 * alone. Deliberately NOT colored: identifiers, function names, types,
 * properties, punctuation. A VSCode theme paints those too, but doing so here
 * would need new design tokens and would change how existing code looks —
 * out of scope for a change whose whole point is that the colors do not move.
 */
export const SYNTAX_THEME: ThemeRegistrationRaw = {
  name: 'niuniu-tokens',
  type: 'light',
  fg: PLAIN,
  // Transparent: the surface owns its own background (`bg-card`, plus the
  // add/delete diff tints). A theme background would paint over them.
  bg: 'transparent',
  settings: [
    // Baseline, so any scope not named below lands on plain foreground rather
    // than inheriting a category from a broader rule.
    { settings: { foreground: PLAIN } },

    {
      scope: ['comment', 'punctuation.definition.comment', 'string.comment'],
      settings: { foreground: SENTINEL.comment },
    },
    {
      scope: [
        'string',
        'string.regexp',
        'constant.other.symbol',
        'punctuation.definition.string',
        'meta.embedded.assembly',
      ],
      settings: { foreground: SENTINEL.string },
    },
    {
      scope: [
        'constant.numeric',
        'constant.language',
        'constant.character',
        'constant.character.escape',
      ],
      settings: { foreground: SENTINEL.number },
    },
    {
      scope: [
        'keyword',
        'storage',
        'storage.type',
        'storage.modifier',
        'variable.language',
        'support.type.primitive',
        // JSX/TSX element names and their angle brackets — the case the old
        // highlighter had no concept of at all.
        'entity.name.tag',
        'punctuation.definition.tag',
      ],
      settings: { foreground: SENTINEL.keyword },
    },
    // Arithmetic/comparison operators are punctuation, not keywords: coloring
    // `+` and `=` would be louder than the old output. Word-like operators
    // (`new`, `in`, `is`, Python's `and`/`or`/`not`) stay keywords — the old
    // KEYWORDS set had them too.
    { scope: ['keyword.operator'], settings: { foreground: PLAIN } },
    {
      scope: [
        'keyword.operator.new',
        'keyword.operator.expression',
        'keyword.operator.word',
        'keyword.operator.logical.python',
      ],
      settings: { foreground: SENTINEL.keyword },
    },
  ],
};

export const SYNTAX_THEME_NAME = SYNTAX_THEME.name!;
