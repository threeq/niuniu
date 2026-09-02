import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi } from 'vitest';
import i18n from '@/i18n';

import { DiffViewer } from './diff-viewer';
import type { GitFileDiff, GitDiffLine } from '@/lib/hooks/use-file-diff';
import type { WorkspaceComment } from '@/types/api';

// The review-loop UI (#689 wave 4). What these cover is the distinction the
// backend earned in wave 1 and the UI had no way to express: a comment is
// either still anchored, moved with its content, or its anchor is GONE — and
// the third case must never render as the first. Plus the two orthogonal
// states (delivered vs judged) that used to be conflated into one badge.

const tr = (key: string, opts?: Record<string, unknown>) =>
  i18n.t(`panels.changes.comments.${key}`, { ns: 'workspaces', ...opts });

const fd = (over: Partial<GitFileDiff> = {}): GitFileDiff => ({
  path: 'app.go',
  status: 'modified',
  additions: 1,
  deletions: 1,
  hunks: [],
  ...over,
});

const hunk = (lines: GitDiffLine[], over = {}) => ({
  old_start: 1,
  old_count: lines.filter((l) => l.type !== 'add').length,
  new_start: 1,
  new_count: lines.filter((l) => l.type !== 'delete').length,
  lines,
  ...over,
});

const comment = (over: Partial<WorkspaceComment> = {}): WorkspaceComment => ({
  id: 1,
  workspace_id: 1,
  repo: 'acme',
  file_path: 'app.go',
  line_number: 2,
  content: 'this needs a nil check',
  created_at: '2026-01-01T00:00:00Z',
  ...over,
});

/** A diff with one context line, one deletion and one addition. */
const simpleDiff = () =>
  fd({
    hunks: [
      hunk([
        { type: 'context', content: 'package main', old_line: 1, new_line: 1 },
        { type: 'delete', content: 'var old = 1', old_line: 2 },
        { type: 'add', content: 'var fresh = 2', new_line: 2 },
      ]),
    ],
  });

function renderViewer(
  comments: WorkspaceComment[],
  handlers: Partial<{
    onQueue: (t: unknown, c: string) => Promise<void>;
    onSend: (t: unknown, c: string) => Promise<void>;
    onSetResolved: (id: number, r: boolean) => Promise<void>;
  }> = {},
  file: GitFileDiff = simpleDiff(),
) {
  const onQueue = handlers.onQueue ?? vi.fn().mockResolvedValue(undefined);
  const onSend = handlers.onSend ?? vi.fn().mockResolvedValue(undefined);
  const onSetResolved = handlers.onSetResolved ?? vi.fn().mockResolvedValue(undefined);
  const utils = render(
    <DiffViewer
      fileDiff={file}
      repoName="acme"
      mode="unified"
      comments={comments}
      onQueueComment={onQueue as never}
      onSendComment={onSend as never}
      onSetResolved={onSetResolved as never}
    />,
  );
  return { ...utils, onQueue, onSend, onSetResolved };
}

describe('review comment anchoring', () => {
  it('renders a current comment inline at its line, with no drift marker', () => {
    renderViewer([
      comment({
        anchor: { status: 'current', side: 'new', original_line: 2, effective_line: 2 },
      }),
    ]);
    expect(screen.getByText('this needs a nil check')).toBeInTheDocument();
    expect(screen.queryByText(tr('anchor.outdated'))).not.toBeInTheDocument();
    expect(screen.queryByText(tr('anchor.relocated'))).not.toBeInTheDocument();
  });

  it('renders a relocated comment at its EFFECTIVE line, not where it was written', () => {
    const file = fd({
      hunks: [
        hunk([
          { type: 'add', content: 'import "fmt"', new_line: 1 },
          { type: 'context', content: 'package main', old_line: 1, new_line: 2 },
          { type: 'context', content: 'var x = 1', old_line: 2, new_line: 3 },
        ]),
      ],
    });
    renderViewer(
      [
        comment({
          line_number: 2,
          anchor: { status: 'relocated', side: 'new', original_line: 2, effective_line: 3 },
        }),
      ],
      {},
      file,
    );
    expect(screen.getByText(tr('anchor.relocated'))).toBeInTheDocument();
    // The metadata must name line 3 (where it is now), not line 2 (where it
    // was written) — showing the original would be the silent-drift bug.
    expect(
      screen.getByText(tr('lineMeta', { line: 3, repo: 'acme', path: 'app.go' })),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(tr('lineMeta', { line: 2, repo: 'acme', path: 'app.go' })),
    ).not.toBeInTheDocument();
  });

  it('lifts an outdated comment out of the code and marks it, instead of pinning it to a line', async () => {
    const user = userEvent.setup();
    const { container } = renderViewer([
      comment({
        content: 'this loop is O(n^2)',
        line_number: 2,
        anchor: {
          status: 'outdated',
          side: 'new',
          original_line: 2,
          context: { before: ['package main'], line: 'var old = 1', after: [] },
        },
      }),
    ]);

    // Not attached to any code row — that is the whole point: line 2 now holds
    // unrelated content and decorating it would look verified.
    const rows = container.querySelectorAll('[data-code-line]');
    for (const row of rows) {
      expect(within(row as HTMLElement).queryByText('this loop is O(n^2)')).toBeNull();
    }

    // It surfaces in the banner instead, with an explicit marker.
    const banner = await screen.findByRole('button', {
      name: new RegExp(tr('anchor.outdatedBanner', { count: 1 })),
    });
    await user.click(banner);
    expect(await screen.findByText('this loop is O(n^2)')).toBeInTheDocument();
    expect(await screen.findByText(tr('anchor.outdated'))).toBeInTheDocument();
  });

  it('expands an outdated comment to show the snapshot of what the line said at the time', async () => {
    const user = userEvent.setup();
    renderViewer([
      comment({
        anchor: {
          status: 'outdated',
          side: 'new',
          original_line: 2,
          context: { before: ['package main'], line: 'var old = 1', after: ['func main() {'] },
        },
      }),
    ]);
    await user.click(
      await screen.findByRole('button', {
        name: new RegExp(tr('anchor.outdatedBanner', { count: 1 })),
      }),
    );
    const toggle = await screen.findByRole('button', { name: tr('anchor.showSnapshot') });
    // The snapshot is collapsed until asked for, then shows the original source.
    expect(screen.queryByText('var old = 1')).not.toBeInTheDocument();
    await user.click(toggle);
    expect(await screen.findByText('var old = 1')).toBeInTheDocument();
    expect(await screen.findByText(tr('anchor.thenLabel'))).toBeInTheDocument();
  });

  it('shows then/now side by side when a relocated anchor landed in changed code', async () => {
    const user = userEvent.setup();
    renderViewer([
      comment({
        anchor: {
          status: 'relocated',
          side: 'new',
          original_line: 2,
          effective_line: 2,
          context: { before: [], line: 'var old = 1', after: [] },
          current: { before: [], line: 'var fresh = 2', after: [] },
        },
      }),
    ]);
    await user.click(await screen.findByRole('button', { name: tr('anchor.compare') }));
    expect(await screen.findByText(tr('anchor.thenLabel'))).toBeInTheDocument();
    expect(await screen.findByText(tr('anchor.nowLabel'))).toBeInTheDocument();
  });
});

describe('review verdict vs delivery', () => {
  it('shows delivery and verdict as two independent badges', () => {
    // Sent to the agent but NOT judged — the exact state that used to read as
    // "handled" and vanish from the queue.
    renderViewer([
      comment({
        sent_to_agent: true,
        resolved: false,
        anchor: { status: 'current', side: 'new', original_line: 2, effective_line: 2 },
      }),
    ]);
    expect(screen.getByText(tr('sent'))).toBeInTheDocument();
    expect(screen.getByText(tr('verdict.open'))).toBeInTheDocument();
    expect(screen.queryByText(tr('verdict.resolved'))).not.toBeInTheDocument();
  });

  it('a resolved comment that was never sent still reads as resolved', () => {
    renderViewer([
      comment({
        sent_to_agent: false,
        resolved: true,
        anchor: { status: 'current', side: 'new', original_line: 2, effective_line: 2 },
      }),
    ]);
    expect(screen.getByText(tr('pending'))).toBeInTheDocument();
    expect(screen.getByText(tr('verdict.resolved'))).toBeInTheDocument();
  });

  it('toggles the verdict without touching delivery', async () => {
    const user = userEvent.setup();
    const onSetResolved = vi.fn().mockResolvedValue(undefined);
    const { onSend } = renderViewer(
      [
        comment({
          id: 77,
          resolved: false,
          anchor: { status: 'current', side: 'new', original_line: 2, effective_line: 2 },
        }),
      ],
      { onSetResolved },
    );
    await user.click(await screen.findByTitle(tr('verdict.resolveHint')));
    expect(onSetResolved).toHaveBeenCalledWith(77, true);
    expect(onSend).not.toHaveBeenCalled();
  });

  it('reopens a resolved comment', async () => {
    const user = userEvent.setup();
    const onSetResolved = vi.fn().mockResolvedValue(undefined);
    renderViewer(
      [
        comment({
          id: 88,
          resolved: true,
          anchor: { status: 'current', side: 'new', original_line: 2, effective_line: 2 },
        }),
      ],
      { onSetResolved },
    );
    await user.click(await screen.findByTitle(tr('verdict.reopenHint')));
    expect(onSetResolved).toHaveBeenCalledWith(88, false);
  });
});

describe('deleted-line (old side) comments', () => {
  it('creates an old-side comment carrying the snapshot the server cannot take', async () => {
    const user = userEvent.setup();
    const onQueue = vi.fn().mockResolvedValue(undefined);
    const { container } = renderViewer([], { onQueue });

    // The deletion row's gutter carries the "+" affordance. Without old-side
    // anchoring this button did not exist at all.
    const rows = [...container.querySelectorAll('[data-code-line]')];
    const delRow = rows.find((r) => within(r as HTMLElement).queryByText('var old = 1'));
    expect(delRow).toBeTruthy();
    const addBtn = (delRow as HTMLElement).querySelector('[data-code-gutter] button');
    expect(addBtn).toBeTruthy();
    await user.click(addBtn as HTMLElement);

    expect(await screen.findByText(tr('composingOldSide', { line: 2 }))).toBeInTheDocument();
    await user.type(await screen.findByRole('textbox'), 'do not delete this');
    await user.click(await screen.findByRole('button', { name: tr('queue') }));

    expect(onQueue).toHaveBeenCalledTimes(1);
    const [target, content] = onQueue.mock.calls[0];
    expect(content).toBe('do not delete this');
    expect(target).toMatchObject({ side: 'old', line: 2 });
    // The snapshot is what anchors it; without one the comment would be
    // reported outdated the moment it was created.
    expect(target.context).toMatchObject({ line: 'var old = 1' });
    expect(target.context.before).toEqual(['package main']);
  });

  it('renders an old-side comment on the deleted line, marked as such', () => {
    const { container } = renderViewer([
      comment({
        content: 'this was load-bearing',
        line_number: 2,
        side: 'old',
        anchor: {
          status: 'current',
          side: 'old',
          original_line: 2,
          effective_line: 2,
          context: { before: [], line: 'var old = 1', after: [] },
        },
      }),
    ]);
    expect(screen.getByText('this was load-bearing')).toBeInTheDocument();
    expect(screen.getByText(tr('oldSide'))).toBeInTheDocument();
    // It hangs off the row holding the deleted line, not the added one.
    const rows = [...container.querySelectorAll('[data-code-line]')];
    const delRow = rows.find((r) => within(r as HTMLElement).queryByText('var old = 1'));
    expect(delRow?.parentElement?.textContent).toContain('this was load-bearing');
  });

  it('keeps old-side and new-side comments on the same integer in separate threads', () => {
    renderViewer([
      comment({
        id: 1,
        content: 'old side note',
        line_number: 2,
        side: 'old',
        anchor: { status: 'current', side: 'old', original_line: 2, effective_line: 2 },
      }),
      comment({
        id: 2,
        content: 'new side note',
        line_number: 2,
        side: 'new',
        anchor: { status: 'current', side: 'new', original_line: 2, effective_line: 2 },
      }),
    ]);
    // Both render, each exactly once — a shared coordinate space would have
    // merged them into one thread showing both twice.
    expect(screen.getAllByText('old side note')).toHaveLength(1);
    expect(screen.getAllByText('new side note')).toHaveLength(1);
  });
});
