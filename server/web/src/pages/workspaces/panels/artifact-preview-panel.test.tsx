import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, it, expect, vi } from 'vitest';

import { ArtifactPreviewPanel, type ArtifactFile } from './artifact-preview-panel';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { toast } from 'sonner';

vi.mock('@/lib/workspace-file-url', () => ({
  getFileContentUrl: (id: string, path: string, mode?: string) => `/api/workspaces/${id}/file-content?path=${path}&mode=${mode}`,
}));

// The remove action calls api.delete; stub it (and toast) so the test asserts on
// the request shape without a real network / toast host.
const deleteMock = vi.fn((_endpoint: string) => Promise.resolve({}));
vi.mock('@/lib/api', () => ({
  api: { delete: (endpoint: string) => deleteMock(endpoint) },
  ApiError: class ApiError extends Error {},
}));
// Download is app-managed (fetch + save + toast): stub the helpers so the test
// asserts the flow without a real network or an anchor click in jsdom.
const fetchProgressMock = vi.fn((_url: string, _onProgress?: (pct: number) => void) =>
  Promise.resolve(new Blob(['x'])),
);
const saveMock = vi.fn();
vi.mock('@/lib/download-file', () => ({
  fetchFileWithProgress: (url: string, onProgress?: (pct: number) => void) =>
    fetchProgressMock(url, onProgress),
  saveBlobToLocal: (blob: Blob, name: string) => saveMock(blob, name),
}));
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const artifacts: ArtifactFile[] = [
  { path: 'report.xlsx', name: 'report.xlsx' },
  { path: 'deck.pptx', name: 'deck.pptx' },
];

// The panel uses useQueryClient (to refresh the manifest after remove), so it
// must render under a QueryClientProvider.
function renderPanel(props: Parameters<typeof ArtifactPreviewPanel>[0]) {
  const client = new QueryClient();
  return render(
    <QueryClientProvider client={client}>
      <ArtifactPreviewPanel {...props} />
    </QueryClientProvider>,
  );
}

describe('ArtifactPreviewPanel', () => {
  it('shows the empty state when there are no artifacts', () => {
    renderPanel({ workspaceId: '7', artifacts: [] });
    expect(screen.getByText('暂无可预览的产物')).toBeInTheDocument();
  });

  it('renders a row per artifact', () => {
    renderPanel({ workspaceId: '7', artifacts });
    expect(screen.getByRole('button', { name: /report\.xlsx/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /deck\.pptx/ })).toBeInTheDocument();
  });

  it('opens the clicked artifact in the content viewer', async () => {
    useWorkspacePanelStore.setState({ contentViewer: {} });
    renderPanel({ workspaceId: '7', artifacts });
    await userEvent.click(screen.getByRole('button', { name: /deck\.pptx/ }));
    expect(useWorkspacePanelStore.getState().contentViewer['7']).toEqual({
      kind: 'file',
      path: 'deck.pptx',
      title: 'deck.pptx',
    });
  });

  it('downloads an artifact through the app-managed flow', async () => {
    renderPanel({ workspaceId: '7', artifacts });
    await userEvent.click(screen.getAllByRole('button', { name: /下载|Download/ })[0]);
    await waitFor(() =>
      expect(fetchProgressMock).toHaveBeenCalledWith(
        '/api/workspaces/7/file-content?path=report.xlsx&mode=raw',
        undefined,
      ),
    );
    await waitFor(() => expect(saveMock).toHaveBeenCalledWith(expect.any(Blob), 'report.xlsx'));
    expect(toast.success).toHaveBeenCalledWith(expect.stringContaining('report.xlsx'));
  });

  it('removes an artifact via the delete endpoint', async () => {
    deleteMock.mockClear();
    renderPanel({ workspaceId: '7', artifacts });
    const removeButtons = screen.getAllByRole('button', { name: /移除|Remove/ });
    await userEvent.click(removeButtons[0]);
    await waitFor(() => expect(deleteMock).toHaveBeenCalledTimes(1));
    expect(deleteMock).toHaveBeenCalledWith('/workspaces/7/artifacts?path=report.xlsx');
  });
});
