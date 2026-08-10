import { describe, it, expect } from 'vitest';
import { blockKeyFor, deriveBlockKeys } from './chat-block-key';

describe('chat-block-key', () => {
  it('keeps the bare messageId for the first block of a turn', () => {
    expect(blockKeyFor('M', 0)).toBe('M');
    expect(blockKeyFor('M', 1)).toBe('M#1');
    expect(blockKeyFor('M', 2)).toBe('M#2');
  });

  it('gives each block of a multi-block turn a distinct key', () => {
    // One assistant turn: text -> tool -> text -> done, all sharing messageId M.
    const events = [
      { id: 'e1', messageId: 'M' }, // first text  -> M
      { id: 'e2', messageId: 'M' }, // tool_use    -> M#1
      { id: 'e3', messageId: 'M' }, // second text -> M#2
      { id: 'e4', messageId: 'M' }, // done        -> M#3
    ];
    const keys = deriveBlockKeys(events);
    expect(keys.get('e1')).toBe('M');
    expect(keys.get('e2')).toBe('M#1');
    expect(keys.get('e3')).toBe('M#2');
    expect(keys.get('e4')).toBe('M#3');
    // The crux of the bug fix: sibling blocks of one turn never collide, so
    // pinning one block can't mark the others.
    const distinct = new Set(keys.values());
    expect(distinct.size).toBe(4);
  });

  it('numbers each messageId independently and stably by order', () => {
    const events = [
      { id: 'a1', messageId: 'A' },
      { id: 'b1', messageId: 'B' },
      { id: 'a2', messageId: 'A' },
      { id: 'b2', messageId: 'B' },
    ];
    const keys = deriveBlockKeys(events);
    expect(keys.get('a1')).toBe('A');
    expect(keys.get('a2')).toBe('A#1');
    expect(keys.get('b1')).toBe('B');
    expect(keys.get('b2')).toBe('B#1');
  });

  it('is backward compatible: a single-block turn maps to the bare messageId', () => {
    const keys = deriveBlockKeys([{ id: 'u1', messageId: 'user-123' }]);
    expect(keys.get('u1')).toBe('user-123');
  });
});
