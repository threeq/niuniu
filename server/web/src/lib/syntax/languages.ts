import { extOf, NO_HIGHLIGHT_EXTS } from '@/lib/file-type';

/**
 * Which grammars ship, and how a file path picks one.
 *
 * WHY A CURATED LIST
 * ------------------
 * Shiki's full bundle carries ~200 grammars (several MB). Only the languages
 * this codebase and its users actually read are registered, and each is a
 * separate dynamic `import()` — so opening a Go file downloads the Go grammar
 * and nothing else. The set below was chosen from the actual extension census
 * of this repository (`git ls-files`, by count):
 *
 *   svg 1748 · go 980 · png 767 · js 448 · tsx 385 · xml 363 · ts 295 ·
 *   md 137 · json 109 · sql 77 · txt 61 · yaml 58 · py 31 · rs 18 · html 11 ·
 *   sh 8 · mjs 7 · css 7 · yml 3 · toml/mod/sum …
 *
 * plus java and c, which are absent here but common in the user repositories
 * the workspace viewer opens, and cheap (4-10kB gzip each).
 *
 * DELIBERATELY EXCLUDED: C++. Its grammar embeds 12 sub-grammars (regexp,
 * glsl, the C preprocessor…) and costs 54kB gzip on its own — more than every
 * other non-JS grammar combined — for a language this repository does not
 * contain. `.cpp`/`.hpp` therefore fall back to the C grammar, which covers
 * most of the syntax at a fraction of the size. Revisit if C++ files start
 * showing up in practice.
 *
 * The three JS-family grammars dominate the remaining cost (~16kB gzip each)
 * because TextMate's JS grammar is genuinely large. They are still separate
 * chunks, so a session that only reads Go never pays for them.
 */

/** The language used when nothing matches. Shiki resolves it without a grammar. */
export const PLAIN_TEXT = 'plaintext';

/**
 * Extension → shiki language id. Only ids present here are ever requested, so
 * the dynamic-import switch below stays exhaustive by construction.
 */
const EXT_TO_LANG: Record<string, string> = {
  // TypeScript / JavaScript. `.ts`/`.js` map to the tsx/jsx grammars on
  // purpose: they are supersets, so a plain .ts file highlights identically
  // while one fewer grammar chunk gets shipped.
  ts: 'tsx', tsx: 'tsx', mts: 'tsx', cts: 'tsx',
  js: 'jsx', jsx: 'jsx', mjs: 'jsx', cjs: 'jsx',

  go: 'go',
  py: 'python', pyi: 'python',
  rs: 'rust',
  java: 'java',
  c: 'c', h: 'c',
  // C++ maps to the C grammar on purpose — see the note above.
  cc: 'c', cpp: 'c', cxx: 'c', hpp: 'c', hh: 'c',

  sql: 'sql',
  json: 'json', jsonc: 'json',
  yaml: 'yaml', yml: 'yaml',
  toml: 'toml',
  xml: 'xml', svg: 'xml',
  html: 'html', htm: 'html',
  css: 'css', scss: 'css', less: 'css',
  sh: 'bash', bash: 'bash', zsh: 'bash', ksh: 'bash',
  md: 'markdown', markdown: 'markdown',
};

/**
 * Extensionless / specially-named files. Matched on the whole lowercased
 * basename, since `extOf('Dockerfile')` yields the name itself and `go.mod`'s
 * "extension" is `mod`.
 *
 * `go.mod` / `go.sum` have no shiki grammar (there is no Go-module TextMate
 * grammar in the bundle) and are listed explicitly so they resolve to plain
 * text by intent rather than by falling through the extension table.
 */
const FILENAME_TO_LANG: Record<string, string> = {
  dockerfile: 'docker',
  makefile: 'make',
  'go.mod': PLAIN_TEXT,
  'go.sum': PLAIN_TEXT,
  '.gitignore': PLAIN_TEXT,
  '.env': 'bash',
};

/**
 * Pick a grammar for a path.
 *
 * Returns {@link PLAIN_TEXT} rather than throwing for anything unrecognized —
 * an unknown extension is the normal case (a repository can contain any file),
 * not an error condition. It also returns plain text for
 * {@link NO_HIGHLIGHT_EXTS}, preserving the existing rule that prose and
 * tabular data (txt/log/csv/tsv/md) read better untokenized.
 */
export function languageForPath(path: string): string {
  const base = (path.split(/[\\/]/).pop() ?? path).toLowerCase();

  const byName = FILENAME_TO_LANG[base];
  if (byName) return byName;

  const ext = extOf(base);
  if (NO_HIGHLIGHT_EXTS.has(ext)) return PLAIN_TEXT;

  return EXT_TO_LANG[ext] ?? PLAIN_TEXT;
}

/** True when the path gets no tokenization at all — lets callers skip the work. */
export function isPlainText(path: string): boolean {
  return languageForPath(path) === PLAIN_TEXT;
}

/**
 * Load one grammar.
 *
 * A literal `switch` of static specifiers rather than a computed
 * `import(\`shiki/langs/${id}.mjs\`)`: a template specifier makes the bundler
 * emit a chunk for every file in that directory — the entire ~200-grammar
 * bundle, which is exactly what this module exists to avoid.
 */
export async function loadLanguage(id: string): Promise<unknown | null> {
  switch (id) {
    case 'tsx': return import('shiki/langs/tsx.mjs');
    case 'jsx': return import('shiki/langs/jsx.mjs');
    case 'go': return import('shiki/langs/go.mjs');
    case 'python': return import('shiki/langs/python.mjs');
    case 'rust': return import('shiki/langs/rust.mjs');
    case 'java': return import('shiki/langs/java.mjs');
    case 'c': return import('shiki/langs/c.mjs');
    case 'sql': return import('shiki/langs/sql.mjs');
    case 'json': return import('shiki/langs/json.mjs');
    case 'yaml': return import('shiki/langs/yaml.mjs');
    case 'toml': return import('shiki/langs/toml.mjs');
    case 'xml': return import('shiki/langs/xml.mjs');
    case 'html': return import('shiki/langs/html.mjs');
    case 'css': return import('shiki/langs/css.mjs');
    case 'bash': return import('shiki/langs/bash.mjs');
    case 'markdown': return import('shiki/langs/markdown.mjs');
    case 'docker': return import('shiki/langs/docker.mjs');
    case 'make': return import('shiki/langs/make.mjs');
    // Includes PLAIN_TEXT, which shiki resolves with no grammar at all.
    default: return null;
  }
}
