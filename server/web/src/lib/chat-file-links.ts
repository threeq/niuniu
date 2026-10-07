/**
 * chat-file-links
 * ---------------
 * Detection + normalization of workspace file references that appear inside
 * agent chat output, so the UI can turn them into "open the file" links.
 *
 * Three concerns live here:
 *  1. MATCH   — find path-looking substrings in plain text (`matchFileRefs`)
 *               and validate a single candidate (`looksLikeFilePath`).
 *  2. PARSE   — split a trailing `:NNN` line number off a raw reference
 *               (`parseFileRef`).
 *  3. RESOLVE — normalize a raw reference (repo-relative, bare filename,
 *               absolute host path with `/` or `\` separators) into a
 *               workspace-relative path, which is the only form the
 *               `/api/workspaces/:id/file-content` endpoint accepts
 *               (`toWorkspaceRelative`).
 *
 * The detector is deliberately conservative: it only fires on candidates whose
 * final segment carries a KNOWN file extension, so ordinary words, versions
 * (`v1.2.3`), commands (`pnpm dev`) and numbers never become links. A missed
 * path costs a click; a wrong match pollutes every message.
 *
 * No look-behind regexes anywhere — the markdown pipeline in this codebase
 * must keep parsing on older WKWebViews (see rehype-linkify.ts).
 */

import tlds from 'tlds';

/** Extensions that make a candidate worth linking. Curated breadth: code,
 *  docs, data, config, media, design — everything the file preview can render
 *  plus common plain-text/code types. Lowercase; candidates are matched
 *  case-insensitively. */
const KNOWN_EXTENSIONS = new Set([
  // Code
  'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'go', 'py', 'rs', 'rb', 'php',
  // Go workspace files: go.mod / go.sum are ubiquitous in Go repo output.
  'mod', 'sum',
  'java', 'kt', 'kts', 'swift', 'scala', 'c', 'h', 'cc', 'cpp', 'hpp', 'cs',
  'm', 'mm', 'vue', 'svelte', 'astro', 'dart', 'lua', 'pl', 'ex', 'exs',
  'zig', 'nim', 'hs', 'erl', 'clj', 'groovy', 'gradle',
  // Web / style
  'css', 'scss', 'sass', 'less', 'styl', 'html', 'htm', 'json', 'jsonc',
  'json5', 'yaml', 'yml', 'toml', 'xml', 'proto', 'graphql', 'gql',
  // Shell / config
  'sh', 'bash', 'zsh', 'fish', 'ps1', 'bat', 'cmd', 'env', 'ini', 'conf',
  'cfg', 'properties', 'lock', 'gitignore', 'gitattributes',
  'dockerfile', 'makefile', 'bazel', 'bzl', 'cmake', 'mk',
  // Docs / data
  'md', 'mdx', 'txt', 'rst', 'adoc', 'pdf', 'csv', 'tsv', 'xlsx', 'xls',
  'docx', 'doc', 'pptx', 'ppt',
  // DB / infra
  'sql', 'prisma', 'tf', 'tfvars', 'hcl',
  // Media / design
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif', 'ico', 'mp4',
  'mov', 'webm', 'mkv', 'avi', 'mp3', 'wav', 'flv', 'm3u8', 'drawio',
  'excalidraw',
]);

/** Characters allowed inside one path segment. Excludes whitespace, quotes,
 *  brackets, punctuation and CJK — a path containing any of those stops the
 *  candidate at the boundary instead. */
const SEGMENT = '[A-Za-z0-9_\\-.@+]+';
const PATH_CANDIDATE = new RegExp(
  // Optional Windows drive prefix ("C:") and optional leading separator,
  // then segments joined by `/` or `\`.
  '(?:[A-Za-z]:)?(?:\\\\|/)?' +
    SEGMENT +
    '(?:[\\\\/]' + SEGMENT + ')*',
  'g',
);

// Maximum plausible line number — keeps `:42` useful while refusing longer
// digit runs that are almost certainly not line refs.
const MAX_LINE = 99999;

/** A candidate found in text: `raw` is the exact substring (goes into the
 *  href), `index` locates it, `path`/`line` are the parsed form. */
export interface FileRefMatch {
  raw: string;
  index: number;
  /** Path part of the raw match (line number stripped). */
  path: string;
  /** 1-based line number from a trailing `:NNN` suffix, when present. */
  line?: number;
}

/** A raw file reference split into its path and optional `:line` suffix. */
export interface ParsedFileRef {
  path: string;
  line?: number;
}

/**
 * Strip a trailing `:NNN` line suffix from a raw reference.
 * Windows drive colons are safe: `C:\x\a.ts` doesn't end in `:digits`.
 */
function splitLineSuffix(raw: string): ParsedFileRef {
  const m = raw.match(/^(.*):(\d{1,6})$/);
  if (m) {
    const line = Number(m[2]);
    if (line >= 1 && line <= MAX_LINE && looksLikeFilePath(m[1])) {
      return { path: m[1], line };
    }
  }
  return { path: raw };
}

/** Domain-shape guard: `first.second(…)` with a plausible TLD — rejects
 *  scheme-less URLs like `example.com/docs/a.html` that would otherwise look
 *  like paths. Uses the `tlds` package (already shipped for rehype-linkify). */
const DOMAIN_SHAPE = /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$/;

function looksLikeDomain(candidate: string): boolean {
  if (!DOMAIN_SHAPE.test(candidate)) return false;
  const tld = candidate.slice(candidate.lastIndexOf('.') + 1).toLowerCase();
  return (tlds as unknown as string[]).includes(tld);
}

/** The last dot starts the extension. Dotfiles (`.env`, `.gitignore`) carry
 *  the name itself after the dot, which the known-set check handles. */
function knownExtension(segment: string): boolean {
  const dot = segment.lastIndexOf('.');
  if (dot === -1) return false;
  const ext = segment.slice(dot + 1).toLowerCase();
  return ext.length > 0 && KNOWN_EXTENSIONS.has(ext);
}

/** Sentence-ending periods stick to the regex match (`…src/app.ts.`) and must
 *  be trimmed before the extension check. */
function trimTrailingDots(candidate: string): string {
  let end = candidate.length;
  while (end > 0 && candidate[end - 1] === '.') end--;
  return candidate.slice(0, end);
}

/**
 * Decide whether `candidate` should render as a clickable file link.
 * Requirements: no whitespace (whole-candidate check), known extension on the
 * final segment, no URL scheme, not a domain-shaped host, and at least one
 * real character of path. A trailing `:NNN` line suffix is tolerated and
 * ignored for the shape check (use `parseFileRef` to split it off).
 */
export function looksLikeFilePath(candidate: string): boolean {
  const c = trimTrailingDots(candidate);
  if (c.length === 0 || c.length > 512) return false;
  if (/\s/.test(c)) return false;
  if (c.includes('://')) return false; // any scheme:// URL
  // Tolerate (and drop) a `:NNN` line suffix before the shape checks —
  // otherwise the extension read would see `ts:42` and fail.
  const withoutLine = c.replace(/:(\d{1,6})$/, '');
  // Domain guard on the first segment. Leading separators are stripped first:
  // a candidate carved out of a URL keeps the host's leading `/`
  // (`/github.com/a/b/x.ts`), which would otherwise make `slash === 0` and
  // silently skip this check.
  const noLeading = withoutLine.replace(/^[\\/]+/, '');
  const slash = Math.max(noLeading.indexOf('/'), noLeading.indexOf('\\'));
  if (slash > 0 && looksLikeDomain(noLeading.slice(0, slash))) return false;
  const lastSegStart = Math.max(withoutLine.lastIndexOf('/'), withoutLine.lastIndexOf('\\')) + 1;
  if (!knownExtension(withoutLine.slice(lastSegStart))) return false;
  // A candidate that is ONLY a Windows drive ("C:") has no real content.
  const stripped = withoutLine.replace(/^[A-Za-z]:/, '');
  return stripped.length > 1;
}

/**
 * Find all file references inside a chunk of plain text.
 * The scanner advances past each accepted match; a rejected candidate is
 * simply skipped by the regex engine, so a shorter valid path starting later
 * in the text is still found.
 */
export function matchFileRefs(text: string): FileRefMatch[] {
  if (!text || text.length < 3) return [];
  const out: FileRefMatch[] = [];
  PATH_CANDIDATE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = PATH_CANDIDATE.exec(text)) !== null) {
    if (m[0].length === 0) {
      PATH_CANDIDATE.lastIndex++;
      continue;
    }
    const pathCandidate = trimTrailingDots(m[0]);
    if (!looksLikeFilePath(pathCandidate)) continue;
    // Extend with an optional `:NNN` line suffix right after the path.
    const suffixMatch = /^:(\d{1,6})(?!\d)/.exec(text.slice(m.index + pathCandidate.length));
    const raw = suffixMatch ? pathCandidate + suffixMatch[0] : pathCandidate;
    const ref = splitLineSuffix(raw);
    out.push({ raw, index: m.index, path: ref.path, line: ref.line });
    PATH_CANDIDATE.lastIndex = m.index + raw.length;
  }
  return out;
}

/**
 * Parse a single raw reference (the form stored in a link's href).
 * Returns null when the string isn't plausibly a file reference.
 */
export function parseFileRef(raw: string): ParsedFileRef | null {
  const c = raw.trim();
  if (!looksLikeFilePath(c)) return null;
  return splitLineSuffix(c);
}

/** Normalize separators to forward slashes and collapse duplicate ones,
 *  preserving a UNC-style leading `//`. */
function normalizeSeparators(p: string): string {
  let out = p.replace(/\\+/g, '/');
  if (out.startsWith('//')) {
    out = '//' + out.slice(2).replace(/\/{2,}/g, '/');
  } else {
    out = out.replace(/\/{2,}/g, '/');
  }
  return out;
}

/**
 * Resolve a raw reference to a workspace-relative path (forward slashes, no
 * leading `./`) — the only form the file-content endpoint accepts.
 *
 * Resolution order for absolute paths:
 *  1. under `workspacePath` → strip that prefix;
 *  2. contains a `.worktrees/` segment (every niuniu repo worktree lives at
 *     `<workspace>/.worktrees/<repo>/`) → cut from its LAST occurrence;
 *  3. otherwise pass through unchanged (the viewer will surface the 404).
 *
 * `workspacePath` may be absent (caller without it): resolution then relies on
 * the `.worktrees/` fallback alone.
 */
export function toWorkspaceRelative(rawPath: string, workspacePath?: string): string {
  let p = normalizeSeparators(rawPath.trim());
  if (/^[A-Za-z]:/.test(p) || p.startsWith('/')) {
    if (workspacePath) {
      const ws = normalizeSeparators(workspacePath.trim()).replace(/\/+$/, '');
      if (ws.length > 0 && (p === ws || p.startsWith(ws + '/'))) {
        p = p.slice(ws.length).replace(/^\/+/, '') || '.';
      }
    }
    // Still absolute (different host root, or workspacePath unknown): every
    // niuniu repo worktree sits at `<workspace>/.worktrees/<repo>/`, so cut
    // from the LAST `.worktrees/` occurrence when present.
    const wt = p.lastIndexOf('.worktrees/');
    if (wt > 0) p = p.slice(wt);
  }
  return p.replace(/^\.\/+/, '');
}
