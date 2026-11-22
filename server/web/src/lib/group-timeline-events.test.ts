import { describe, expect, it } from 'vitest';
import { groupTimelineEvents, reuseUnchangedGroups } from './group-timeline-events';
import type { TimelineEvent } from '@/pages/workspaces/panels/chat-message';

const ev = (id: string, type: string): TimelineEvent => ({
  id,
  messageId: id,
  type,
  role: 'assistant',
  content: '',
});

describe('groupTimelineEvents', () => {
  it('groups consecutive tool uses and lets text break the group', () => {
    const a = ev('a', 'tool_use');
    const b = ev('b', 'tool_use');
    const t = ev('t', 'text');
    const c = ev('c', 'tool_use');

    const grouped = groupTimelineEvents([a, b, t, c]);
    expect(grouped).toHaveLength(3);
    expect(grouped[0]).toEqual([a, b]);
    expect(grouped[1]).toBe(t);
    // A lone tool use stays unwrapped.
    expect(grouped[2]).toBe(c);
  });

  it('skips tool_result events (rendered inside their tool card)', () => {
    const a = ev('a', 'tool_use');
    const r = ev('r', 'tool_result');
    expect(groupTimelineEvents([a, r])).toEqual([a]);
  });
});

describe('reuseUnchangedGroups', () => {
  it('reuses the previous array when member events are unchanged', () => {
    const a = ev('a', 'tool_use');
    const b = ev('b', 'tool_use');
    const prev = groupTimelineEvents([a, b]);
    const next = groupTimelineEvents([a, b]);

    // groupTimelineEvents allocated a fresh array; the identity must survive.
    expect(next[0]).not.toBe(prev[0]);
    expect(reuseUnchangedGroups(prev, next)[0]).toBe(prev[0]);
  });

  it('replaces only the group whose members changed', () => {
    const a = ev('a', 'tool_use');
    const b = ev('b', 'tool_use');
    const x = ev('x', 'tool_use');
    const prev = groupTimelineEvents([a, b, ev('t', 'text'), x]);

    // One member of the first group is updated (new object identity).
    const b2 = { ...b, toolInput: '{"path":"a.ts"}' };
    const next = groupTimelineEvents([a, b2, ev('t2', 'text'), x]);
    const merged = reuseUnchangedGroups(prev, next);

    expect(merged[0]).not.toBe(prev[0]);
    expect((merged[0] as TimelineEvent[])[1]).toBe(b2);
    // [group, text, lone tool use] — the trailing single tool use is still
    // the same object.
    expect(merged).toHaveLength(3);
    expect(merged[2]).toBe(x);
  });
});
