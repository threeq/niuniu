import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { http, HttpResponse } from 'msw';
import { describe, it, expect, beforeEach } from 'vitest';
import i18n from '@/i18n';
import { server } from '@/mocks/server-node';

import { ContentViewerPanel } from './content-viewer-panel';
import type { GitDiffLine } from '@/lib/hooks/use-file-diff';

// Files opened from the changes list carry a Diff/File toggle; rich-preview
// formats (markdown, images, pdf, …) must additionally offer the rendered
// Preview tab, while plain code files must not (it would duplicate File).

const label = (key: string) => i18n.t(`contentViewer.${key}`, { ns: 'workspaces' });

const hunk = (lines: GitDiffLine[]) => ({
  old_start: 1,
  old_count: lines.filter((l) => l.type !== 'add').length,
  new_start: 1,
  new_count: lines.filter((l) => l.type !== 'delete').length,
  lines,
});

const diffResponse = (files: { path: string; hunks: ReturnType<typeof hunk>[] }[]) => [
  {
    name: 'acme',
    repository_id: 0, // unresolved group: the viewer renders the inline hunks, no repo-diff fetch
    worktree_path: 'E:/wt/acme',
    base_branch: 'main',
    current_branch: 'dev',
    files: files.map((f) => ({
      path: f.path,
      status: 'modified',
      additions: 1,
      deletions: 0,
      hunks: f.hunks,
    })),
  },
];

function mount(target: { repo: string; path: string }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ContentViewerPanel workspaceId="w1" target={{ kind: 'diff', ...target }} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  server.use(
    http.get('*/api/workspaces/w1/diff', () =>
      HttpResponse.json(
        diffResponse([
          {
            path: 'docs/report.md',
            hunks: [hunk([{ type: 'add', content: '# Report body', new_line: 1 }])],
          },
          {
            path: 'main.go',
            hunks: [hunk([{ type: 'add', content: 'package main', new_line: 1 }])],
          },
        ]),
      ),
    ),
    http.get('*/api/workspaces/w1', () =>
      HttpResponse.json({ id: 'w1', name: 'w', worktrees: [] }),
    ),
    http.get('*/api/workspaces/w1/comments', () => HttpResponse.json([])),
    http.get('*/api/workspaces/w1/file-content', ({ request }) => {
      const path = new URL(request.url).searchParams.get('path') ?? '';
      if (path.endsWith('report.md')) {
        return HttpResponse.text('# Preview Heading\n\nrendered body');
      }
      return HttpResponse.text('package main', { headers: { 'Content-Type': 'text/plain' } });
    }),
  );
});

describe('CodeView preview tab (diff-opened files)', () => {
  it('offers Preview for a markdown file and renders it on click', async () => {
    const user = userEvent.setup();
    mount({ repo: 'acme', path: 'docs/report.md' });

    const previewTab = await screen.findByText(label('viewPreview'));
    expect(await screen.findByText(/Report body/)).toBeInTheDocument();

    await user.click(previewTab);
    expect(await screen.findByText('Preview Heading')).toBeInTheDocument();
    expect(screen.getByText('rendered body')).toBeInTheDocument();
    // The rendered side replaces the diff, it does not append to it.
    expect(screen.queryByText(/Report body/)).not.toBeInTheDocument();
  });

  it('offers no Preview tab for a plain code file', async () => {
    mount({ repo: 'acme', path: 'main.go' });

    expect(await screen.findByText(/package main/)).toBeInTheDocument();
    expect(screen.queryByText(label('viewPreview'))).not.toBeInTheDocument();
  });

  it('falls back to the diff view when Preview is active and the target switches to a code file', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { rerender } = render(
      <QueryClientProvider client={qc}>
        <ContentViewerPanel workspaceId="w1" target={{ kind: 'diff', repo: 'acme', path: 'docs/report.md' }} />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByText(label('viewPreview')));
    expect(await screen.findByText('Preview Heading')).toBeInTheDocument();

    // The panel isn't keyed by path: switching files keeps CodeView mounted with
    // its mode state, so a stale 'preview' on a code file must land on a
    // renderable mode instead of a blank pane.
    rerender(
      <QueryClientProvider client={qc}>
        <ContentViewerPanel workspaceId="w1" target={{ kind: 'diff', repo: 'acme', path: 'main.go' }} />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByText(/package main/)).toBeInTheDocument());
    expect(screen.queryByText('Preview Heading')).not.toBeInTheDocument();
    // The tab strip itself is gone for the code file — the stale mode must not
    // keep highlighting a tab that no longer exists.
    expect(screen.queryByText(label('viewPreview'))).not.toBeInTheDocument();
  });
});
