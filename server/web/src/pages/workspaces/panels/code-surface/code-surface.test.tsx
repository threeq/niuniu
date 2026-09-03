import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { describe, it, expect, beforeAll } from 'vitest';
import i18n from '@/i18n';

import { DiffViewer } from '../diff-viewer';
import { CodeFileView } from '../code-file-view';
import { buildUnifiedRows, buildSplitRows, findRowForLine } from '.';
import type { GitFileDiff, GitDiffLine } from '@/lib/hooks/use-file-diff';

/**
 * Virtualization acceptance tests (#689).
 *
 * The load-bearing assertion is the DOM node count: the old viewers mounted one
 * row per line unconditionally, and their "protection" against huge inputs was
 * either to drop syntax highlighting while still mounting everything
 * (MAX_HIGHLIGHT_LINES) or to show a notice with a *Render anyway* button that
 * then mounted everything (MAX_DIFF_LINES). Asserting `rendered ≪ total` is what
 * actually pins the new behaviour down — a regression to full rendering would
 * still pass every content assertion.
 *
 * jsdom performs no layout, so the surface falls back to a nominal viewport
 * height (see FALLBACK_VIEWPORT_HEIGHT); windowing is therefore exercised, just
 * against an assumed rather than a measured viewport.
 */

const label = (key: string, opts?: Record<string, unknown>) =>
  i18n.t(`panels.changes.diff.${key}`, { ns: 'workspaces', ...opts });

/** A diff of `n` changed lines in one hunk (no context — nothing folds). */
function bigDiff(n: number): GitFileDiff {
  const lines: GitDiffLine[] = [];
  for (let i = 1; i <= n; i++) {
    lines.push({ type: 'delete', content: `old line ${i}`, old_line: i });
    lines.push({ type: 'add', content: `new line ${i}`, new_line: i });
  }
  return {
    path: 'big.ts',
    status: 'modified',
    additions: n,
    deletions: n,
    hunks: [{ old_start: 1, old_count: n, new_start: 1, new_count: n, lines }],
  };
}

const countLines = (c: HTMLElement) => c.querySelectorAll('[data-code-line]').length;

beforeAll(() => {
  // jsdom's Element.prototype.scrollIntoView is not implemented; the anchor
  // path calls it on the un-windowed branch.
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {};
  }
});

describe('virtualized diff rendering', () => {
  it('mounts far fewer rows than the diff has lines', () => {
    const total = 10_000; // 5000 changed pairs
    const { container } = render(
      <DiffViewer fileDiff={bigDiff(total / 2)} repoName="acme" mode="unified" />,
    );

    const rendered = countLines(container);
    // Measured at ~51 rows for a nominal 800px viewport at 20px/line + overscan.
    // The bound is deliberately loose (window size is a tuning detail) but far
    // below `total`, so a regression to full rendering fails loudly.
    expect(rendered).toBeGreaterThan(0);
    expect(rendered).toBeLessThan(200);
  });

  it('renders a 10k-line file without mounting 10k rows', () => {
    const content = Array.from({ length: 10_000 }, (_, i) => `const x${i} = ${i};`).join('\n');
    const { container } = render(
      <CodeFileView content={content} repoName="acme" filePath="huge.ts" />,
    );

    const rendered = countLines(container);
    expect(rendered).toBeGreaterThan(0);
    expect(rendered).toBeLessThan(200);
  });

  it('mounts every row for a short file (windowing stays out of the way)', () => {
    const content = Array.from({ length: 40 }, (_, i) => `line ${i}`).join('\n');
    const { container } = render(
      <CodeFileView content={content} repoName="acme" filePath="small.ts" />,
    );
    expect(countLines(container)).toBe(40);
  });

  it('still highlights a file past the retired MAX_HIGHLIGHT_LINES cutoff', async () => {
    // 8000 lines used to mean: no highlighting AND 8000 DOM rows. Windowing
    // makes tokenizing the visible slice cheap, so highlighting always applies.
    const content = Array.from({ length: 8_000 }, () => 'const x = "hi";').join('\n');
    const { container } = render(
      <CodeFileView content={content} repoName="acme" filePath="big.ts" />,
    );

    // Tokenization is asynchronous (a worker in the browser, the same tokenizer
    // inline under jsdom), so colors arrive a chunk at a time rather than on the
    // first paint. The generous timeout covers loading the TSX grammar.
    await waitFor(
      () => {
        expect(container.querySelector('.text-syntax-keyword')).not.toBeNull();
        expect(container.querySelector('.text-syntax-string')).not.toBeNull();
      },
      { timeout: 15_000 },
    );
  }, 20_000);

  it('renders a large diff directly, with no "render anyway" gate', () => {
    const { container } = render(
      <DiffViewer fileDiff={bigDiff(3_000)} repoName="acme" mode="unified" />,
    );
    // MAX_DIFF_LINES and its escape hatch are gone — along with their i18n keys,
    // so the assertion is that the diff body rendered instead of a notice.
    expect(countLines(container)).toBeGreaterThan(0);
    expect(screen.queryByText(/render anyway/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/仍然渲染/)).not.toBeInTheDocument();
  });

  it('windows the split view too, keeping both halves on one row', () => {
    const { container } = render(
      <DiffViewer fileDiff={bigDiff(2_000)} repoName="acme" mode="split" />,
    );

    const rows = [...container.querySelectorAll('[data-code-line]')];
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(200);
    // Left and right live in the SAME row element — that is the alignment
    // guarantee. Two independently scrolled lists could drift; one row cannot.
    for (const row of rows) {
      expect(row.querySelectorAll('[data-code-gutter]').length).toBe(2);
    }
  });
});

describe('row building', () => {
  /** A hunk with a long unchanged run in the middle, so one gap forms. */
  const withGap = (): GitFileDiff => {
    const lines: GitDiffLine[] = [{ type: 'add', content: 'first', new_line: 1 }];
    for (let i = 2; i <= 41; i++) {
      lines.push({ type: 'context', content: `ctx ${i}`, old_line: i, new_line: i });
    }
    lines.push({ type: 'add', content: 'last', new_line: 42 });
    return {
      path: 'g.ts',
      status: 'modified',
      additions: 2,
      deletions: 0,
      hunks: [{ old_start: 1, old_count: 40, new_start: 1, new_count: 42, lines }],
    };
  };

  it('folds a long context run and expands it on demand', () => {
    const file = withGap();
    const collapsed = buildUnifiedRows(file, new Set());
    const gap = collapsed.find((r) => r.kind === 'gap');
    expect(gap).toBeDefined();

    const expanded = buildUnifiedRows(file, new Set([gap!.key]));
    expect(expanded.some((r) => r.kind === 'gap')).toBe(false);
    expect(expanded.length).toBeGreaterThan(collapsed.length);
  });

  it('keeps line anchoring correct after a gap expands', () => {
    const file = withGap();
    const collapsed = buildUnifiedRows(file, new Set());
    const gap = collapsed.find((r) => r.kind === 'gap')!;

    // A line inside the fold has no row while collapsed — reporting -1 rather
    // than a guessed index is what stops an anchor jump landing arbitrarily.
    expect(findRowForLine(collapsed, 20)).toBe(-1);

    const expanded = buildUnifiedRows(file, new Set([gap.key]));
    const idx = findRowForLine(expanded, 20);
    expect(idx).toBeGreaterThanOrEqual(0);
    const row = expanded[idx];
    expect(row.kind).toBe('line');
    if (row.kind === 'line') {
      expect(row.cells.some((c) => c.line?.content === 'ctx 20')).toBe(true);
    }
  });

  it('disableCollapse renders every line (full-file view)', () => {
    const rows = buildUnifiedRows(withGap(), new Set(), true);
    expect(rows.some((r) => r.kind === 'gap')).toBe(false);
    expect(rows.filter((r) => r.kind === 'line')).toHaveLength(42);
  });

  it('pairs a modification onto one split row and pads the shorter side', () => {
    const file: GitFileDiff = {
      path: 'p.ts',
      status: 'modified',
      additions: 2,
      deletions: 1,
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 2,
          lines: [
            { type: 'delete', content: 'gone', old_line: 1 },
            { type: 'add', content: 'kept', new_line: 1 },
            { type: 'add', content: 'extra', new_line: 2 },
          ],
        },
      ],
    };
    const rows = buildSplitRows(file, new Set()).filter((r) => r.kind === 'line');
    expect(rows).toHaveLength(2);

    if (rows[0].kind === 'line') {
      expect(rows[0].cells[0].line?.content).toBe('gone');
      expect(rows[0].cells[1].line?.content).toBe('kept');
    }
    // The unmatched add has no old-side counterpart: a pad cell, not a line.
    if (rows[1].kind === 'line') {
      expect(rows[1].cells[0].line).toBeNull();
      expect(rows[1].cells[1].line?.content).toBe('extra');
    }
  });

  it('anchors every commentable row on the new-side number only', () => {
    const file = bigDiff(3);
    for (const rows of [buildUnifiedRows(file, new Set()), buildSplitRows(file, new Set())]) {
      for (const row of rows) {
        if (row.kind !== 'line' || row.anchor == null) continue;
        // A pure deletion has no new number, so it must carry no anchor.
        expect(row.cells.some((c) => c.line?.new_line === row.anchor)).toBe(true);
      }
    }
  });
});

describe('gap expansion through the viewer', () => {
  it('reveals the folded lines when the expand button is clicked', () => {
    const lines: GitDiffLine[] = [{ type: 'add', content: 'first', new_line: 1 }];
    for (let i = 2; i <= 41; i++) {
      lines.push({ type: 'context', content: `ctx ${i}`, old_line: i, new_line: i });
    }
    const file: GitFileDiff = {
      path: 'g.ts',
      status: 'modified',
      additions: 1,
      deletions: 0,
      hunks: [{ old_start: 1, old_count: 40, new_start: 1, new_count: 41, lines }],
    };

    const { container } = render(<DiffViewer fileDiff={file} repoName="acme" mode="unified" />);
    const before = countLines(container);

    const button = screen.getByRole('button', { name: /\d+/ });
    fireEvent.click(button);

    expect(countLines(container)).toBeGreaterThan(before);
    expect(screen.queryByRole('button', { name: label('expandLines', { count: 34 }) })).toBeNull();
  });
});
