import { describe, it, expect } from 'vitest';
import {
  looksLikeFilePath,
  matchFileRefs,
  parseFileRef,
  toWorkspaceRelative,
} from './chat-file-links';

describe('looksLikeFilePath', () => {
  const valid = [
    'src/app.ts',
    'server/web/src/lib/api.ts',
    'README.md',
    '.gitignore',
    'docs/design-system.md',
    'C:\\Users\\me\\proj\\a.go',
    'C:/Users/me/proj/a.go',
    '/home/u/proj/main.rs',
    '.worktrees/niuniu-main/server/go.mod',
    'a.PNG',
    'IMG_20260101.JPG',
    'assets/logo.svg',
    'out.csv:42',
  ];
  for (const c of valid) {
    it(`accepts ${c}`, () => {
      expect(looksLikeFilePath(c)).toBe(true);
    });
  }

  const invalid = [
    '',
    'hello world',
    'pnpm dev',
    'v1.2.3',
    '42',
    'https://example.com/a.html',
    'example.com/docs/a.html',
    'node_modules',
    'some-random-word',
    'app.doesnotexist',
    'foo.ts bar.ts', // space → not a single candidate
  ];
  for (const c of invalid) {
    it(`rejects ${c}`, () => {
      expect(looksLikeFilePath(c)).toBe(false);
    });
  }
});

describe('matchFileRefs', () => {
  it('finds a plain path in prose', () => {
    const refs = matchFileRefs('请看 server/web/src/lib/api.ts 里的实现');
    expect(refs).toHaveLength(1);
    expect(refs[0].raw).toBe('server/web/src/lib/api.ts');
    expect(refs[0].path).toBe('server/web/src/lib/api.ts');
    expect(refs[0].line).toBeUndefined();
  });

  it('finds multiple paths and reports indices', () => {
    const text = 'edit a.ts then b.md done';
    const refs = matchFileRefs(text);
    expect(refs.map((r) => r.raw)).toEqual(['a.ts', 'b.md']);
    expect(text.slice(refs[0].index, refs[0].index + refs[0].raw.length)).toBe('a.ts');
  });

  it('captures a :line suffix', () => {
    const refs = matchFileRefs('see src/app.ts:42 for details');
    expect(refs).toHaveLength(1);
    expect(refs[0].raw).toBe('src/app.ts:42');
    expect(refs[0].path).toBe('src/app.ts');
    expect(refs[0].line).toBe(42);
  });

  it('does not capture a :line suffix followed by more digits junk', () => {
    const refs = matchFileRefs('see src/app.ts:42424242424 for details');
    expect(refs[0].raw).toBe('src/app.ts');
  });

  it('trims sentence punctuation after the path', () => {
    const refs = matchFileRefs('Done in main.go.');
    expect(refs).toHaveLength(1);
    expect(refs[0].raw).toBe('main.go');
  });

  it('ignores URLs and versions', () => {
    expect(matchFileRefs('open https://github.com/a/b/blob/main/x.ts now')).toHaveLength(0);
    expect(matchFileRefs('released v1.2.3 today')).toHaveLength(0);
  });

  it('ignores paths with unknown extensions', () => {
    expect(matchFileRefs('check the README.asdf file')).toHaveLength(0);
  });

  it('finds windows absolute paths', () => {
    const refs = matchFileRefs('file C:\\work\\demo\\main.py saved');
    expect(refs).toHaveLength(1);
    expect(refs[0].raw).toBe('C:\\work\\demo\\main.py');
  });
});

describe('parseFileRef', () => {
  it('splits path and line', () => {
    expect(parseFileRef('src/a.ts:7')).toEqual({ path: 'src/a.ts', line: 7 });
    expect(parseFileRef('src/a.ts')).toEqual({ path: 'src/a.ts' });
  });

  it('returns null for non-paths', () => {
    expect(parseFileRef('just words')).toBeNull();
    expect(parseFileRef('https://x.com/a.js')).toBeNull();
  });

  it('keeps windows drive colon intact', () => {
    expect(parseFileRef('C:\\w\\a.ts')).toEqual({ path: 'C:\\w\\a.ts' });
  });
});

describe('toWorkspaceRelative', () => {
  it('passes relative paths through, normalizing ./ and separators', () => {
    expect(toWorkspaceRelative('src/app.ts')).toBe('src/app.ts');
    expect(toWorkspaceRelative('./src/app.ts')).toBe('src/app.ts');
    expect(toWorkspaceRelative('src\\lib\\api.ts')).toBe('src/lib/api.ts');
  });

  it('strips the workspace prefix from absolute paths', () => {
    expect(
      toWorkspaceRelative('C:\\Users\\u\\.niuniu\\users\\2\\workspaces\\942\\CLAUDE.md', 'C:\\Users\\u\\.niuniu\\users\\2\\workspaces\\942'),
    ).toBe('CLAUDE.md');
    expect(
      toWorkspaceRelative('/home/u/ws/.worktrees/rep/README.md', '/home/u/ws'),
    ).toBe('.worktrees/rep/README.md');
  });

  it('cuts absolute paths at the last .worktrees segment as a fallback', () => {
    expect(
      toWorkspaceRelative('C:\\Users\\u\\.niuniu\\users\\2\\workspaces\\942\\.worktrees\\niuniu-main\\src\\app.ts'),
    ).toBe('.worktrees/niuniu-main/src/app.ts');
    expect(toWorkspaceRelative('/some/other/place/.worktrees/r/a.go')).toBe('.worktrees/r/a.go');
  });

  it('handles mixed separators in the workspace prefix', () => {
    expect(
      toWorkspaceRelative('C:/Users/u/ws942/notes.md', 'C:\\Users\\u\\ws942'),
    ).toBe('notes.md');
  });

  it('leaves unresolvable absolute paths unchanged', () => {
    expect(toWorkspaceRelative('/opt/data/report.pdf')).toBe('/opt/data/report.pdf');
  });
});
