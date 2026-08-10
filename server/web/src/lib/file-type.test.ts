import { describe, expect, it } from 'vitest';
import { isMarkdownFile, isTextLikeFile } from './file-type';

describe('isMarkdownFile', () => {
  it('matches markdown extensions case-insensitively', () => {
    expect(isMarkdownFile('README.md')).toBe(true);
    expect(isMarkdownFile('docs/guide.markdown')).toBe(true);
    expect(isMarkdownFile('CHANGELOG.MD')).toBe(true);
  });

  it('rejects non-markdown files', () => {
    expect(isMarkdownFile('main.go')).toBe(false);
    expect(isMarkdownFile('image.png')).toBe(false);
    expect(isMarkdownFile('notes.txt')).toBe(false);
  });
});

describe('isTextLikeFile vs markdown routing', () => {
  // Markdown must NOT be "text-like": the content viewer routes it to the
  // preview/source toggle instead of the plain commentable code view.
  it('treats markdown as rich (not plain text-like)', () => {
    expect(isTextLikeFile('README.md')).toBe(false);
    expect(isMarkdownFile('README.md')).toBe(true);
  });

  it('treats code/config (incl. json) as text-like so they are commentable', () => {
    expect(isTextLikeFile('main.go')).toBe(true);
    expect(isTextLikeFile('Makefile')).toBe(true);
    expect(isTextLikeFile('go.mod')).toBe(true);
    expect(isTextLikeFile('.mcp.json')).toBe(true);
    expect(isTextLikeFile('tsconfig.json')).toBe(true);
  });
});
