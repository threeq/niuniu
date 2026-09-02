import { describe, it, expect } from 'vitest';
import { isPending, effectiveLine, isOutdated } from './use-workspace-comments';
import type { WorkspaceComment } from '@/types/api';

// The two orthogonal states this wave separated:
//   sent_to_agent — delivery. Did the agent receive it?
//   resolved      — the review verdict. Did a human judge it addressed?
// Conflating them is the bug; these pin the places the distinction matters.

const c = (over: Partial<WorkspaceComment> = {}): WorkspaceComment => ({
  id: 1,
  workspace_id: 1,
  repo: 'acme',
  file_path: 'app.go',
  line_number: 10,
  content: 'fix this',
  created_at: '2026-01-01T00:00:00Z',
  ...over,
});

describe('isPending (the send queue)', () => {
  it('queues an unsent, unjudged comment', () => {
    expect(isPending(c({ sent_to_agent: false, resolved: false }))).toBe(true);
  });

  it('drops a comment already delivered', () => {
    expect(isPending(c({ sent_to_agent: true, resolved: false }))).toBe(false);
  });

  it('drops a RESOLVED comment even though it was never sent', () => {
    // The load-bearing case. A reviewer who writes a note and then decides it is
    // fine must not have it swept into a batch send — that would push the agent
    // to re-fix something a human explicitly closed.
    expect(isPending(c({ sent_to_agent: false, resolved: true }))).toBe(false);
  });

  it('treats a missing sent_to_agent as unsent', () => {
    expect(isPending(c())).toBe(true);
  });
});

describe('effectiveLine (where a comment may render)', () => {
  it('uses the anchor line for a current anchor', () => {
    expect(
      effectiveLine(c({ anchor: { status: 'current', side: 'new', effective_line: 10 } })),
    ).toBe(10);
  });

  it('follows a relocated anchor rather than the line it was written against', () => {
    expect(
      effectiveLine(
        c({
          line_number: 10,
          anchor: { status: 'relocated', side: 'new', original_line: 10, effective_line: 42 },
        }),
      ),
    ).toBe(42);
  });

  it('refuses to place an outdated comment at ANY line', () => {
    // Returning the original line here is precisely the silent drift the whole
    // anchoring mechanism exists to prevent.
    expect(
      effectiveLine(
        c({ line_number: 10, anchor: { status: 'outdated', side: 'new', original_line: 10 } }),
      ),
    ).toBeNull();
  });

  it('falls back to the stored line when no anchor was resolved', () => {
    // An optimistic create has no anchor yet; it should still render.
    expect(effectiveLine(c({ line_number: 7 }))).toBe(7);
  });
});

describe('isOutdated', () => {
  it('is true only for the outdated status', () => {
    expect(isOutdated(c({ anchor: { status: 'outdated', side: 'new' } }))).toBe(true);
    expect(isOutdated(c({ anchor: { status: 'relocated', side: 'new' } }))).toBe(false);
    expect(isOutdated(c({ anchor: { status: 'current', side: 'new' } }))).toBe(false);
    expect(isOutdated(c())).toBe(false);
  });
});
