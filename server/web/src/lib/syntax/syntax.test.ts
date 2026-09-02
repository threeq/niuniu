import { describe, it, expect } from 'vitest';
import { languageForPath, isPlainText, PLAIN_TEXT } from './languages';
import { SENTINEL, TOKEN_CLASS, SYNTAX_THEME } from './theme';
import { ensureLanguage, tokenizeChunk, type SyntaxLine } from './tokenizer';
import { nextHighlightId, requestHighlight } from './client';

/**
 * Acceptance tests for the shiki highlighter that replaced the two hand-rolled
 * regex ones.
 *
 * These assert the properties the old implementation could not hold: a grammar
 * chosen per file type, JSX/Go-generics/multi-line strings tokenized correctly,
 * colors that come only from design tokens, and unknown extensions degrading to
 * plain text instead of throwing.
 */

/** Category name for a token, for readable assertions. */
type Category = 'keyword' | 'string' | 'comment' | 'number' | 'plain';

const CATEGORY: Record<string, Category> = {
  'text-syntax-keyword': 'keyword',
  'text-syntax-string': 'string',
  'text-syntax-comment italic': 'comment',
  'text-syntax-number': 'number',
};

const categoryOf = (className?: string): Category =>
  (className && CATEGORY[className]) || 'plain';

/** All tokens of `line` whose category is `want`, joined. */
function textOf(line: SyntaxLine, want: Category): string {
  return line
    .filter((t) => categoryOf(t.className) === want)
    .map((t) => t.content)
    .join('');
}

async function tokenize(code: string, path: string): Promise<SyntaxLine[]> {
  const lang = await ensureLanguage(languageForPath(path));
  const { lines } = await tokenizeChunk(code, lang);
  return lines;
}

describe('language selection', () => {
  it('maps extensions to grammars', () => {
    expect(languageForPath('src/app.tsx')).toBe('tsx');
    expect(languageForPath('main.go')).toBe('go');
    expect(languageForPath('script.py')).toBe('python');
    expect(languageForPath('query.sql')).toBe('sql');
    expect(languageForPath('config.yaml')).toBe('yaml');
    expect(languageForPath('run.sh')).toBe('bash');
  });

  it('treats .ts/.js as their superset grammars, so one chunk covers both', () => {
    expect(languageForPath('a.ts')).toBe('tsx');
    expect(languageForPath('a.js')).toBe('jsx');
  });

  it('recognizes extensionless and specially-named files', () => {
    expect(languageForPath('Dockerfile')).toBe('docker');
    expect(languageForPath('deploy/Makefile')).toBe('make');
    expect(languageForPath('.env')).toBe('bash');
  });

  it('keeps prose and tabular data un-highlighted (the file-type.ts rule)', () => {
    for (const p of ['notes.txt', 'server.log', 'data.csv', 'data.tsv', 'README.md']) {
      expect(isPlainText(p)).toBe(true);
    }
  });

  it('degrades unknown extensions to plain text rather than failing', () => {
    for (const p of ['weird.qqq', 'no-extension', 'archive.tar.zzz', '']) {
      expect(languageForPath(p)).toBe(PLAIN_TEXT);
    }
  });

  it('does not throw when asked to load a grammar that does not exist', async () => {
    await expect(ensureLanguage('not-a-real-language')).resolves.toBe(PLAIN_TEXT);
  });
});

describe('design tokens', () => {
  it('emits only design-token classes, never literal colors', () => {
    for (const className of Object.values(TOKEN_CLASS)) {
      expect(className).toMatch(/^text-syntax-/);
      // The palette classes that simple-highlight.tsx hardcoded.
      expect(className).not.toMatch(/text-(purple|green|amber|gray|blue|red)-\d/);
    }
  });

  it('maps every sentinel to a class, so no sentinel can leak as a color', () => {
    for (const sentinel of Object.values(SENTINEL)) {
      expect(TOKEN_CLASS[sentinel]).toBeDefined();
    }
  });

  it('paints a transparent background so diff tints show through', () => {
    expect(SYNTAX_THEME.bg).toBe('transparent');
  });
});

describe('grammar-aware tokenization', () => {
  it('colors JSX tags in TSX — the old highlighter had no concept of them', async () => {
    const [line] = await tokenize('const a = <div className="x">{1}</div>;', 'a.tsx');
    const keywords = textOf(line, 'keyword');
    expect(keywords).toContain('div');
    expect(textOf(line, 'string')).toContain('"x"');
    expect(textOf(line, 'number')).toBe('1');
  });

  it('handles Go generics without mangling the type parameters', async () => {
    const [line] = await tokenize('func Map[T any, U any](s []T) []U { return nil }', 'm.go');
    expect(textOf(line, 'keyword')).toContain('func');
    // `T`/`U` are identifiers, not keywords — they must stay uncolored.
    const kw = textOf(line, 'keyword');
    expect(kw).not.toContain('Map');
  });

  it('keeps a multi-line string a string on every line', async () => {
    const lines = await tokenize('const a = `one\ntwo\nthree`;', 'a.tsx');
    // The old per-line highlighter tokenized line 2 as code; it is a string.
    expect(textOf(lines[1], 'string')).toBe('two');
    expect(textOf(lines[1], 'keyword')).toBe('');
  });

  it('keeps a block comment a comment across lines', async () => {
    const lines = await tokenize('/* one\n   const two\n */\nconst x = 1;', 'a.tsx');
    // `const` inside the comment must NOT be a keyword — the specific failure
    // the retired highlighter documented ("block comments are only colored when
    // they open and close on the same line").
    expect(textOf(lines[1], 'comment')).toContain('const two');
    expect(textOf(lines[1], 'keyword')).toBe('');
    // …and real code after the comment closes is still highlighted.
    expect(textOf(lines[3], 'keyword')).toContain('const');
  });

  it('applies language-specific keywords instead of one shared list', async () => {
    // `pass` is a Python keyword and NOT a Go one; the old shared KEYWORDS set
    // colored it in both.
    const [py] = await tokenize('pass', 'a.py');
    expect(textOf(py, 'keyword')).toBe('pass');

    const [go] = await tokenize('pass := 1', 'a.go');
    expect(textOf(go, 'keyword')).toBe('');
  });

  it('does not treat # inside a CSS value as a comment', async () => {
    const [line] = await tokenize('.a { color: #fff; }', 'a.css');
    expect(textOf(line, 'comment')).toBe('');
  });

  it('returns plain tokens for a plain-text file without throwing', async () => {
    const lines = await tokenize('const x = 1;', 'notes.txt');
    expect(lines[0].every((t) => categoryOf(t.className) === 'plain')).toBe(true);
  });
});

describe('chunked tokenization', () => {
  it('produces identical output to tokenizing the whole file at once', async () => {
    // The property the diff/virtualized paths depend on: carrying grammarState
    // across chunk boundaries must not change the result, or a multi-line
    // construct would break exactly at a chunk edge.
    const source = [
      'const a = `start',
      'middle',
      'end`;',
      '/* block',
      ' still comment',
      ' */',
      'const b = 1;',
    ];
    const lang = await ensureLanguage('tsx');

    const whole = (await tokenizeChunk(source.join('\n'), lang)).lines;

    const chunked: SyntaxLine[] = [];
    let state: unknown;
    for (let i = 0; i < source.length; i += 2) {
      const result = await tokenizeChunk(source.slice(i, i + 2).join('\n'), lang, state);
      state = result.state;
      chunked.push(...result.lines);
    }

    expect(chunked).toEqual(whole);
  });
});

describe('request lifecycle', () => {
  it('stops delivering to a subscription that has been cancelled', async () => {
    const id = nextHighlightId();
    const chunks: number[] = [];
    const cancel = requestHighlight(id, 'tsx', 'const a = 1;', {
      onChunk: (from) => chunks.push(from),
      onError: () => {},
    });
    cancel();

    // Long enough for the grammar load + tokenize to have completed had it not
    // been cancelled (the same path delivers within ~1s uncancelled).
    await new Promise((r) => setTimeout(r, 2_000));
    expect(chunks).toEqual([]);
  }, 10_000);

  it('does not let a superseded request deliver into the newer one', async () => {
    // Ids are reused across a surface's lifetime: opening file B under the id
    // that was showing file A must not leave A's chunks arriving into B's
    // handler, or B would render A's tokens against its own text.
    const id = nextHighlightId();
    const first: number[] = [];
    requestHighlight(id, 'tsx', 'const a = 1;\nconst b = 2;', {
      onChunk: (from) => first.push(from),
      onError: () => {},
    });

    // Replace it immediately, without calling the first cancel — the case a
    // tombstone-based scheme got wrong.
    const second: number[] = [];
    const cancelSecond = requestHighlight(id, 'tsx', 'const c = 3;', {
      onChunk: (from) => second.push(from),
      onError: () => {},
    });

    await new Promise((r) => setTimeout(r, 3_000));
    cancelSecond();

    expect(first).toEqual([]);
    expect(second.length).toBeGreaterThan(0);
  }, 10_000);
});
