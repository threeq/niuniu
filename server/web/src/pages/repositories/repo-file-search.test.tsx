import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { it, expect, vi, beforeEach, describe } from 'vitest';
import { RepoSearchBox, RepoSearchResults, FileHistorySidebar } from './repo-file-search';
import { api } from '@/lib/api';
import type { RepoSearchResponse, FileLogEntry } from '@/types/api';

vi.mock('@/lib/api', () => ({ api: { get: vi.fn() } }));

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

beforeEach(() => vi.clearAllMocks());

const searchResponse: RepoSearchResponse = {
  query: 'parse',
  files: [{ path: 'src/parser.go', name: 'parser.go' }],
  filesTruncated: false,
  content: {
    engine: 'ripgrep',
    files: [{ path: 'src/main.go', repo: '', matches: [{ line: 42, text: 'parseDiff(out)' }] }],
    totalMatches: 1,
    truncated: false,
  },
};

describe('RepoSearchResults', () => {
  // The requirement is ONE list, not a mode picker: a file-name hit and a
  // content hit must both be reachable without the user choosing a tab first.
  it('shows file-name and content hits together with no tab switching', async () => {
    vi.mocked(api.get).mockResolvedValue(searchResponse);

    wrap(
      <RepoSearchResults
        repoId="1"
        query="parse"
        onOpenFile={() => {}}
        onBackToFiles={() => {}}
      />,
    );

    // Both halves visible at once.
    await waitFor(() => expect(screen.getByText('parser.go')).toBeInTheDocument());
    expect(screen.getByText('parseDiff(out)')).toBeInTheDocument();

    // No tab controls: the retired UI had clickable "文件名"/"文件内容" buttons.
    // They remain as plain section labels, so assert on ROLE, not text.
    const buttons = screen.getAllByRole('button');
    const labels = buttons.map((b) => b.textContent ?? '');
    expect(labels.some((l) => l.trim() === '文件名')).toBe(false);
    expect(labels.some((l) => l.trim() === '文件内容')).toBe(false);
  });

  it('reports the line number for a content hit so the caller can jump to it', async () => {
    vi.mocked(api.get).mockResolvedValue(searchResponse);
    const onOpenFile = vi.fn();

    wrap(
      <RepoSearchResults repoId="1" query="parse" onOpenFile={onOpenFile} onBackToFiles={() => {}} />,
    );

    await waitFor(() => expect(screen.getByText('parseDiff(out)')).toBeInTheDocument());
    fireEvent.click(screen.getByText('parseDiff(out)'));
    expect(onOpenFile).toHaveBeenCalledWith('src/main.go', 42);
  });

  it('offers a way back to the file list', async () => {
    vi.mocked(api.get).mockResolvedValue(searchResponse);
    const onBackToFiles = vi.fn();

    wrap(
      <RepoSearchResults repoId="1" query="parse" onOpenFile={() => {}} onBackToFiles={onBackToFiles} />,
    );

    fireEvent.click(await screen.findByRole('button', { name: /返回文件列表/ }));
    expect(onBackToFiles).toHaveBeenCalled();
  });

  // A missing ripgrep must not blank the whole panel: file-name hits still stand.
  it('still lists file hits when the content half could not run', async () => {
    vi.mocked(api.get).mockResolvedValue({
      query: 'parse',
      files: [{ path: 'src/parser.go', name: 'parser.go' }],
      filesTruncated: false,
      contentError: 'no_engine',
    } satisfies RepoSearchResponse);

    wrap(
      <RepoSearchResults repoId="1" query="parse" onOpenFile={() => {}} onBackToFiles={() => {}} />,
    );

    await waitFor(() => expect(screen.getByText('parser.go')).toBeInTheDocument());
    expect(screen.getByText(/未安装 ripgrep/)).toBeInTheDocument();
  });
});

describe('RepoSearchBox', () => {
  // Content search spawns a process, so it must fire on submit, not per keystroke.
  it('does not search while typing — only on submit', () => {
    const onSubmit = vi.fn();
    const { container } = wrap(
      <RepoSearchBox value="par" onChange={() => {}} onSubmit={onSubmit} onClear={() => {}} />,
    );

    fireEvent.change(screen.getByPlaceholderText(/搜索文件名或内容/), {
      target: { value: 'parse' },
    });
    expect(onSubmit).not.toHaveBeenCalled();

    fireEvent.submit(container.querySelector('form')!);
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });
});

const history: FileLogEntry[] = [
  {
    hash: 'aaaaaaaabbbbbbbb',
    author: 'Tester',
    date: '2026-01-02T03:04:05+08:00',
    message: 'third: append line',
    path_at_commit: 'new-name.txt',
  },
  {
    hash: 'ccccccccdddddddd',
    author: 'Tester',
    date: '2026-01-01T03:04:05+08:00',
    message: 'first: add old-name',
    path_at_commit: 'old-name.txt',
  },
];

describe('FileHistorySidebar', () => {
  it('raises the picked commit instead of rendering the diff itself', async () => {
    vi.mocked(api.get).mockResolvedValue(history);
    const onSelect = vi.fn();

    wrap(
      <FileHistorySidebar
        repoId="1"
        path="new-name.txt"
        onSelect={onSelect}
        onClose={() => {}}
      />,
    );

    fireEvent.click(await screen.findByText('third: append line'));
    // The parent owns the diff area, so the sidebar only reports the selection.
    expect(onSelect).toHaveBeenCalledWith(history[0]);
  });

  it('can be closed', async () => {
    vi.mocked(api.get).mockResolvedValue(history);
    const onClose = vi.fn();

    wrap(
      <FileHistorySidebar repoId="1" path="new-name.txt" onSelect={() => {}} onClose={onClose} />,
    );

    fireEvent.click(await screen.findByRole('button', { name: /关闭历史/ }));
    expect(onClose).toHaveBeenCalled();
  });

  // A rename is the case where "line 42" style reasoning silently breaks, so the
  // old name must be visible rather than implied.
  it('surfaces the old name for a pre-rename commit', async () => {
    vi.mocked(api.get).mockResolvedValue(history);

    wrap(
      <FileHistorySidebar repoId="1" path="new-name.txt" onSelect={() => {}} onClose={() => {}} />,
    );

    expect(await screen.findByText(/old-name\.txt/)).toBeInTheDocument();
  });
});
