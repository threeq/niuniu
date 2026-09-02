import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse, delay } from 'msw';
import { server } from '@/mocks/server-node';
import { WorkspaceSearchDialog } from './workspace-search-dialog';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';

const WS_ID = '42';

/** Registers both search endpoints. `contentDelay` simulates grep being slow. */
function mockSearch(opts: {
  files?: { path: string; name: string; repo: string; isDir: boolean }[];
  content?: unknown;
  contentStatus?: number;
  contentDelay?: number;
}) {
  server.use(
    http.get('*/workspaces/:id/files', () =>
      HttpResponse.json({ files: opts.files ?? [] }),
    ),
    http.get('*/workspaces/:id/search/content', async () => {
      if (opts.contentDelay) await delay(opts.contentDelay);
      if (opts.contentStatus && opts.contentStatus >= 400) {
        return HttpResponse.json(
          { error: { code: 'SEARCH_ENGINE_MISSING', message: 'no engine' } },
          { status: opts.contentStatus },
        );
      }
      return HttpResponse.json(
        opts.content ?? { engine: 'ripgrep', files: [], totalMatches: 0, truncated: false },
      );
    }),
  );
}

const NAME_HIT = {
  path: '.worktrees/repo1/src/needle-util.ts',
  name: 'needle-util.ts',
  repo: 'repo1',
  isDir: false,
};

const CONTENT_HIT = {
  engine: 'ripgrep',
  totalMatches: 1,
  truncated: false,
  files: [
    {
      path: '.worktrees/repo1/src/other.ts',
      repo: 'repo1',
      matches: [{ line: 17, text: 'const needle = 1;', columns: [[6, 12]] }],
    },
  ],
};

function renderDialog(onOpenChange = vi.fn()) {
  render(
    <WorkspaceSearchDialog workspaceId={WS_ID} open onOpenChange={onOpenChange} />,
  );
  return onOpenChange;
}

beforeEach(() => {
  useWorkspacePanelStore.setState({ contentViewer: {} });
});

describe('WorkspaceSearchDialog', () => {
  // The core acceptance criterion: one input, both kinds of result, no mode
  // decision required from the user before they can start searching.
  it('shows file-name AND file-content results for a single query', async () => {
    mockSearch({ files: [NAME_HIT], content: CONTENT_HIT });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    await waitFor(() => expect(screen.getByText('needle-util.ts')).toBeInTheDocument());
    await waitFor(() =>
      expect(screen.getByText('.worktrees/repo1/src/other.ts')).toBeInTheDocument(),
    );
    // Both group headings are present at once, not one behind a mode toggle.
    expect(screen.getByText('文件名')).toBeInTheDocument();
    expect(screen.getByText('文件内容')).toBeInTheDocument();
  });

  // Name search must not be gated on the (much slower) content search.
  it('renders name results before slow content results arrive', async () => {
    mockSearch({ files: [NAME_HIT], content: CONTENT_HIT, contentDelay: 3000 });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    await waitFor(() => expect(screen.getByText('needle-util.ts')).toBeInTheDocument());
    // Content is still in flight, so its file header is not on screen yet.
    expect(screen.queryByText('.worktrees/repo1/src/other.ts')).not.toBeInTheDocument();
  });

  it('opens the file at the matched line when a content hit is chosen', async () => {
    mockSearch({ files: [], content: CONTENT_HIT });
    const onOpenChange = renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');
    await waitFor(() => expect(screen.getByText('17')).toBeInTheDocument());

    await userEvent.click(screen.getByText('17'));

    const target = useWorkspacePanelStore.getState().contentViewer[WS_ID];
    expect(target).toMatchObject({
      kind: 'file',
      path: '.worktrees/repo1/src/other.ts',
      line: 17,
    });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it('opens the file with no line anchor when a name hit is chosen', async () => {
    mockSearch({ files: [NAME_HIT], content: undefined });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');
    await waitFor(() => expect(screen.getByText('needle-util.ts')).toBeInTheDocument());

    await userEvent.click(screen.getByText('needle-util.ts'));

    const target = useWorkspacePanelStore.getState().contentViewer[WS_ID];
    expect(target).toMatchObject({ kind: 'file', path: NAME_HIT.path });
    expect((target as { line?: number }).line).toBeUndefined();
  });

  // The regression this whole endpoint design guards against: "cannot search"
  // must never be presented as "found nothing".
  it('reports a missing search engine instead of an empty result', async () => {
    mockSearch({ files: [NAME_HIT], contentStatus: 501 });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    await waitFor(() =>
      expect(screen.getByText(/内容搜索不可用/)).toBeInTheDocument(),
    );
    // Name search still works, so the panel is not wholly dead.
    expect(screen.getByText('needle-util.ts')).toBeInTheDocument();
  });

  it('surfaces a truncation notice when the backend cut results short', async () => {
    mockSearch({
      files: [],
      content: { ...CONTENT_HIT, truncated: true, truncatedReason: 'limit' },
    });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    await waitFor(() =>
      expect(screen.getByText(/匹配过多/)).toBeInTheDocument(),
    );
  });

  it('reports a timeout truncation distinctly', async () => {
    mockSearch({
      files: [],
      content: { ...CONTENT_HIT, truncated: true, truncatedReason: 'timeout' },
    });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    await waitFor(() => expect(screen.getByText(/搜索超时/)).toBeInTheDocument());
  });

  it('flags a file whose matches hit the per-file cap', async () => {
    mockSearch({
      files: [],
      content: {
        ...CONTENT_HIT,
        files: [{ ...CONTENT_HIT.files[0], truncated: true }],
      },
    });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    await waitFor(() => expect(screen.getByText(/还有更多匹配/)).toBeInTheDocument());
  });

  // A single character would make grep match nearly everything while telling
  // the user nothing, so content search waits for a second character.
  it('holds content search below the minimum query length', async () => {
    mockSearch({ files: [NAME_HIT], content: CONTENT_HIT });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'n');

    await waitFor(() =>
      expect(screen.getByText(/至少输入 2 个字符/)).toBeInTheDocument(),
    );
  });

  it('shows a hint and no groups before anything is typed', () => {
    mockSearch({});
    renderDialog();
    expect(screen.getByText(/输入即可同时搜索/)).toBeInTheDocument();
    expect(screen.queryByText('文件内容')).not.toBeInTheDocument();
  });

  it('toggles content search options', async () => {
    mockSearch({ files: [], content: CONTENT_HIT });
    renderDialog();

    await userEvent.type(screen.getByRole('textbox'), 'needle');

    const caseBtn = screen.getByRole('button', { name: '区分大小写' });
    expect(caseBtn).toHaveAttribute('aria-pressed', 'false');
    await userEvent.click(caseBtn);
    expect(caseBtn).toHaveAttribute('aria-pressed', 'true');
  });
});
