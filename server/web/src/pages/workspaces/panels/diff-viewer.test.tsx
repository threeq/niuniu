import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import i18n from '@/i18n';

import { DiffViewer } from './diff-viewer';
import type { GitFileDiff, GitDiffLine } from '@/lib/hooks/use-file-diff';

// The viewer renders straight from the backend's structured hunks — there is no
// client-side unified-diff parser left. These cover the cases the deleted
// parseUnifiedDiff test used to own, now driven by the backend's FileDiff shape
// (see server/internal/git/diff_parse_test.go for the producing side).

/** The rendered copy for a diff-notice key, in whatever locale tests run under. */
const notice = (key: string, opts?: Record<string, string>) =>
  i18n.t(`panels.changes.diff.${key}`, { ns: 'workspaces', ...opts });

const fd = (over: Partial<GitFileDiff> = {}): GitFileDiff => ({
  path: 's.sh',
  status: 'modified',
  additions: 0,
  deletions: 0,
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

function renderViewer(file: GitFileDiff) {
  return render(<DiffViewer fileDiff={file} repoName="acme" mode="unified" />);
}

describe('DiffViewer', () => {
  it('renders hunk lines with the backend-resolved line numbers', () => {
    const { container } = renderViewer(
      fd({
        additions: 1,
        deletions: 1,
        hunks: [
          hunk([
            { type: 'context', content: 'keep me', old_line: 1, new_line: 1 },
            { type: 'delete', content: 'echo old', old_line: 2 },
            { type: 'add', content: 'echo new', new_line: 2 },
          ]),
        ],
      }),
    );

    // Syntax highlighting splits content across spans, so assert on the row's
    // full text rather than a single text node.
    const rows = [...container.querySelectorAll('tbody tr')].map((r) => r.textContent ?? '');
    expect(rows.some((r) => r.includes('echo old'))).toBe(true);
    expect(rows.some((r) => r.includes('echo new'))).toBe(true);

    // Gutters show the numbers the backend resolved; the viewer derives none.
    // The deleted line carries only an old number, the added line only a new one.
    const gutters = (text: string) =>
      [...container.querySelectorAll('tbody tr')]
        .find((r) => (r.textContent ?? '').includes(text))!
        .querySelectorAll('td');
    expect(gutters('echo old')[0].textContent).toBe('2');
    expect(gutters('echo old')[1].textContent).toBe('');
    expect(gutters('echo new')[0].textContent).toBe('');
    expect(gutters('echo new')[1].textContent).toBe('2');
  });

  it('renders the hunk header, including git\'s section heading', () => {
    renderViewer(
      fd({
        hunks: [
          hunk([{ type: 'context', content: 'x', old_line: 1, new_line: 1 }], {
            old_start: 10,
            old_count: 1,
            new_start: 12,
            new_count: 1,
            header: 'func main()',
          }),
        ],
      }),
    );
    expect(screen.getByText('@@ -10,1 +12,1 @@ func main()')).toBeInTheDocument();
  });

  // Regression: `is_binary` — not `hunks.length === 0` — is the binary signal.
  // The marker used to be dropped by the backend and recovered only by the
  // client parser; now it rides on the structured payload.
  it('shows the binary notice only when is_binary is set', () => {
    renderViewer(fd({ path: 'logo.png', is_binary: true }));
    expect(screen.getByText(notice('binary'))).toBeInTheDocument();
  });

  it('describes a mode-only change as a mode change, not as binary', () => {
    renderViewer(fd({ old_mode: '100644', new_mode: '100755' }));
    expect(
      screen.getByText(notice('modeChange', { old: '100644', new: '100755' })),
    ).toBeInTheDocument();
    expect(screen.queryByText(notice('binary'))).not.toBeInTheDocument();
  });

  it('describes a pure rename as a rename, not as binary', () => {
    renderViewer(fd({ path: 'new.sh', old_path: 'old.sh', status: 'renamed' }));
    expect(screen.getByText(notice('renameOnly'))).toBeInTheDocument();
    expect(screen.queryByText(notice('binary'))).not.toBeInTheDocument();
  });

  it('treats an empty file (no hunks, no modes) as a no-content change, not binary', () => {
    renderViewer(fd({ path: 'empty.txt', status: 'added' }));
    expect(screen.getByText(notice('noContentChange'))).toBeInTheDocument();
    expect(screen.queryByText(notice('binary'))).not.toBeInTheDocument();
  });

  it('renders a no-newline-at-EOF line as ordinary content (marker is not a line)', () => {
    renderViewer(
      fd({
        additions: 1,
        hunks: [hunk([{ type: 'add', content: 'tail', new_line: 1, no_newline: true }])],
      }),
    );
    expect(screen.getByText('tail')).toBeInTheDocument();
    expect(screen.queryByText(/No newline at end of file/)).not.toBeInTheDocument();
  });
});
