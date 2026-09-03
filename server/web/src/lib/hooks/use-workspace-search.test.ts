import { describe, expect, it } from 'vitest';
import { splitByColumns } from './use-workspace-search-shortcut';
import { classifyContentError } from './use-workspace-search';
import { ApiError } from '@/lib/api';

describe('splitByColumns', () => {
  it('returns the whole line as one plain segment when there are no columns', () => {
    expect(splitByColumns('hello world')).toEqual([{ text: 'hello world', hit: false }]);
    expect(splitByColumns('hello world', [])).toEqual([{ text: 'hello world', hit: false }]);
  });

  it('splits a single ASCII match into before/hit/after', () => {
    expect(splitByColumns('say hello now', [[4, 9]])).toEqual([
      { text: 'say ', hit: false },
      { text: 'hello', hit: true },
      { text: ' now', hit: false },
    ]);
  });

  it('handles a match at the very start and very end', () => {
    expect(splitByColumns('abc', [[0, 3]])).toEqual([{ text: 'abc', hit: true }]);
    expect(splitByColumns('xabc', [[1, 4]])).toEqual([
      { text: 'x', hit: false },
      { text: 'abc', hit: true },
    ]);
  });

  it('splits multiple matches on one line', () => {
    expect(splitByColumns('a b a', [[0, 1], [4, 5]])).toEqual([
      { text: 'a', hit: true },
      { text: ' b ', hit: false },
      { text: 'a', hit: true },
    ]);
  });

  // The reason offsets are decoded as bytes rather than string indices: this
  // codebase's comments are largely Chinese, so a naive slice would tear
  // multi-byte characters and highlight the wrong span.
  it('treats offsets as BYTES so multi-byte text highlights correctly', () => {
    const text = '中文 needle 中文';
    const byteStart = new TextEncoder().encode('中文 ').length; // 7 bytes, 3 chars
    const byteEnd = byteStart + 'needle'.length;

    const segs = splitByColumns(text, [[byteStart, byteEnd]]);
    expect(segs.find((s) => s.hit)?.text).toBe('needle');
    expect(segs.map((s) => s.text).join('')).toBe(text);
  });

  it('never drops or duplicates characters', () => {
    const text = 'the quick brown fox';
    const segs = splitByColumns(text, [[4, 9], [10, 15]]);
    expect(segs.map((s) => s.text).join('')).toBe(text);
  });

  it('degrades to plain text on malformed ranges instead of throwing', () => {
    // Out-of-bounds, inverted, and overlapping ranges must all be survivable:
    // this runs inside a render, so an exception would blank the panel.
    expect(() => splitByColumns('abc', [[-5, 99]])).not.toThrow();
    expect(splitByColumns('abc', [[2, 1]])).toEqual([{ text: 'abc', hit: false }]);

    const overlap = splitByColumns('abcdef', [[0, 4], [2, 6]]);
    expect(overlap.map((s) => s.text).join('')).toBe('abcdef');
  });

  it('sorts unordered ranges before splitting', () => {
    expect(splitByColumns('a b a', [[4, 5], [0, 1]])).toEqual([
      { text: 'a', hit: true },
      { text: ' b ', hit: false },
      { text: 'a', hit: true },
    ]);
  });
});

describe('classifyContentError', () => {
  // The distinction that matters most: a host with no grep must be reported as
  // "cannot search", never rendered like an empty result set.
  it('maps a 501 to engine-missing', () => {
    const err = new ApiError(501, 'no engine', {
      error: { code: 'SEARCH_ENGINE_MISSING', message: 'no engine' },
    });
    expect(classifyContentError(err).kind).toBe('engine-missing');
  });

  it('maps the SEARCH_ENGINE_MISSING code to engine-missing regardless of status', () => {
    const err = new ApiError(500, 'no engine', {
      error: { code: 'SEARCH_ENGINE_MISSING', message: 'no engine' },
    });
    expect(classifyContentError(err).kind).toBe('engine-missing');
  });

  it('maps a 400 to invalid-query', () => {
    const err = new ApiError(400, 'bad regex', { error: { code: 'BAD_REQUEST' } });
    expect(classifyContentError(err).kind).toBe('invalid-query');
  });

  it('maps other API failures to failed and keeps the message', () => {
    const err = new ApiError(500, 'boom', { error: { code: 'INTERNAL_ERROR' } });
    const got = classifyContentError(err);
    expect(got.kind).toBe('failed');
    expect(got.message).toBe('boom');
  });

  it('handles non-ApiError throwables', () => {
    expect(classifyContentError(new Error('network down'))).toEqual({
      kind: 'failed',
      message: 'network down',
    });
    expect(classifyContentError('weird').kind).toBe('failed');
  });
});
