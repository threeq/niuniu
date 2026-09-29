import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, it, expect, vi } from 'vitest';

import { VideoProductPanel } from './video-product-panel';
import { ApiError } from '@/lib/api';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import type { VideoChange, VideoProduct, VideoProjectResponse } from '@/types/api';

// The panel talks to the frozen video-project contract through videoProjectApi;
// stub every call so the tests assert on request shapes without a network.
const getMock = vi.fn((_workspaceId: string) => Promise.resolve(emptyResponse));
const saveMock = vi.fn((_workspaceId: string, _key: string, _body: unknown) => Promise.resolve(storyboardProduct));
const createChangeMock = vi.fn((_workspaceId: string, _body: unknown) => Promise.resolve(pendingChange));
const dispatchMock = vi.fn((_workspaceId: string, _changeId: string) => Promise.resolve({
  change: { ...pendingChange, status: 'dispatched', dispatched_to_issue: 12 },
  issue_id: 12,
  issue_title: 'wave3 分镜',
}));

vi.mock('@/lib/api', () => ({
  videoProjectApi: {
    get: (workspaceId: string) => getMock(workspaceId),
    saveProduct: (workspaceId: string, key: string, body: unknown) => saveMock(workspaceId, key, body),
    createChange: (workspaceId: string, body: unknown) => createChangeMock(workspaceId, body),
    dispatchChange: (workspaceId: string, changeId: string) => dispatchMock(workspaceId, changeId),
  },
  // Mirrors the real ApiError signature (status, message, body) so the panel's
  // 409 branch and the tests share one class identity.
  ApiError: class ApiError extends Error {
    status: number;
    body: unknown;
    constructor(status: number, message: string, body: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.body = body;
    }
  },
}));
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));
vi.mock('@/lib/workspace-file-url', () => ({
  getFileContentUrl: (id: string, path: string, mode?: string) =>
    `/api/workspaces/${id}/file-content?path=${path}&mode=${mode}`,
}));

import { toast } from 'sonner';

const storyboardProduct: VideoProduct = {
  key: 'storyboard',
  file: 'video-project/storyboard.json',
  kind: 'json',
  present: true,
  revision: 3,
  review_status: 'in-review',
  data: {
    title: 'AI 咖啡短剧',
    aspect_ratio: '9:16',
    fps: 30,
    resolution: '720x1280',
    review_status: 'in-review',
    revision: 3,
    shots: [
      {
        id: '01',
        duration_sec: 3.5,
        transition: 'cut',
        action: '主角推门入座，环顾',
        narration: '城市醒了，咖啡也醒了。',
        subtitle: '城市醒了',
        visual: {
          type: 'video',
          tier: 'L3',
          prompt: '暖色调咖啡馆，中景缓推',
          candidates: ['shots/01-a.mp4', 'shots/01-b.mp4'],
          selected: 'shots/01-b.mp4',
          asset: 'shots/01-b.mp4',
        },
        tts: { voice: 'v1', asset: 'assets/tts-01.mp3' },
      },
    ],
  },
};

const briefProduct: VideoProduct = {
  key: 'brief',
  file: 'video-project/directorial-brief.md',
  kind: 'markdown',
  present: true,
  revision: 0,
  review_status: '',
  data: '基调：温暖克制。',
};

const pendingChange: VideoChange = {
  id: 'chg-20260927-001',
  target: 'storyboard.json',
  kind: 'annotation',
  content: '把外套改成风衣',
  status: 'pending',
  dispatched_to_issue: 0,
  created_at: '2026-09-27T10:00:00Z',
};

const emptyResponse: VideoProjectResponse = {
  exists: false,
  products: [],
  changes: [],
  outputs: [],
  shards: { quotes: 0, qc: 0 },
};

const response: VideoProjectResponse = {
  exists: true,
  products: [briefProduct, storyboardProduct],
  changes: [],
  outputs: [{ path: 'video-project/output/final.mp4', size: 12345678, modified_at: '2026-09-27T10:00:00Z' }],
  shards: { quotes: 3, qc: 1 },
};

// The panel uses useQuery/useQueryClient (cache updates after save / dispatch),
// so it must render under a QueryClientProvider.
function renderPanel(workspaceId = '7') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <VideoProductPanel workspaceId={workspaceId} />
    </QueryClientProvider>,
  );
}

describe('VideoProductPanel', () => {
  it('shows the empty state when the workspace has no video project', async () => {
    getMock.mockResolvedValue(emptyResponse);
    renderPanel();
    expect(await screen.findByText(/该工作空间尚无视频项目/)).toBeInTheDocument();
  });

  it('renders the product tree and storyboard shot cards', async () => {
    getMock.mockResolvedValue(response);
    renderPanel();

    // Product tree: all six products, the storyboard one carries revision + badge.
    expect(await screen.findByRole('button', { name: /导演阐述/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /分镜/ })).toBeInTheDocument();
    expect(screen.getAllByText('v3').length).toBeGreaterThan(0);
    expect(screen.getAllByText('评审中').length).toBeGreaterThan(0);

    // Storyboard card: number / duration / action / narration / subtitle / prompt.
    expect(screen.getByText('#01')).toBeInTheDocument();
    expect(screen.getByLabelText('时长 #01')).toHaveValue('3.5');
    expect(screen.getByLabelText('画面 #01')).toHaveValue('主角推门入座，环顾');
    expect(screen.getByLabelText('台词 #01')).toHaveValue('城市醒了，咖啡也醒了。');
    expect(screen.getByLabelText('字幕 #01')).toHaveValue('城市醒了');
    expect(screen.getByLabelText('画面 Prompt #01')).toHaveValue('暖色调咖啡馆，中景缓推');
    expect(screen.getByText('共 1 镜')).toBeInTheDocument();

    // Candidates + selected asset render as buttons.
    expect(screen.getAllByRole('button', { name: /01-a\.mp4/ }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole('button', { name: /01-b\.mp4/ }).length).toBeGreaterThan(0);

    // Shard counts come straight from the aggregate response.
    expect(screen.getByText('报价 3 · 质检 1')).toBeInTheDocument();
  });

  it('opens a candidate asset and an output in the content viewer with workspace-relative paths', async () => {
    getMock.mockResolvedValue(response);
    useWorkspacePanelStore.setState({ contentViewer: {} });
    renderPanel();

    await userEvent.click((await screen.findAllByRole('button', { name: /01-a\.mp4/ }))[0]);
    expect(useWorkspacePanelStore.getState().contentViewer['7']).toEqual({
      kind: 'file',
      path: 'video-project/shots/01-a.mp4',
      title: '01-a.mp4',
    });

    await userEvent.click(screen.getByRole('button', { name: /final\.mp4/ }));
    expect(useWorkspacePanelStore.getState().contentViewer['7']).toEqual({
      kind: 'file',
      path: 'video-project/output/final.mp4',
      title: 'final.mp4',
    });
  });

  it('saves a shot field edit through PUT with expected_revision and refreshes from the response', async () => {
    getMock.mockResolvedValue(response);
    saveMock.mockResolvedValue({
      ...storyboardProduct,
      revision: 4,
      data: {
        ...(storyboardProduct.data as Record<string, unknown>),
        revision: 4,
      },
    });
    renderPanel();

    const narration = await screen.findByLabelText('台词 #01');
    await userEvent.clear(narration);
    await userEvent.type(narration, '改过的台词');
    await userEvent.click(screen.getByRole('button', { name: /保存/ }));

    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    expect(saveMock).toHaveBeenCalledWith('7', 'storyboard', expect.objectContaining({ expected_revision: 3 }));
    const body = saveMock.mock.calls[0][2] as { content: string; expected_revision: number };
    const parsed = JSON.parse(body.content) as { narration?: unknown; shots: { narration: string }[] };
    expect(parsed.shots[0].narration).toBe('改过的台词');
    // The edit must land on the shot, not leak onto the product root.
    expect(parsed.narration).toBeUndefined();

    // The returned product replaces the cached one — revision badge moves to v4.
    await waitFor(() => expect(screen.getAllByText('v4').length).toBeGreaterThan(0));
    expect(toast.success).toHaveBeenCalled();
  });

  it('surfaces a 409 as a "regenerated, refresh and retry" conflict', async () => {
    getMock.mockResolvedValue(response);
    saveMock.mockRejectedValue(new ApiError(409, 'revision mismatch', null));
    renderPanel();

    const narration = await screen.findByLabelText('台词 #01');
    await userEvent.type(narration, '再来一条');
    await userEvent.click(screen.getByRole('button', { name: /保存/ }));

    expect(await screen.findByText('已被重新生成，请刷新后重试')).toBeInTheDocument();
    expect(toast.error).toHaveBeenCalledWith('已被重新生成，请刷新后重试');
  });

  it('adds a note as a pending change and dispatches it back to a regenerated issue', async () => {
    getMock.mockResolvedValue(response);
    createChangeMock.mockResolvedValue(pendingChange);
    renderPanel();

    // The note composer targets the selected product's file by default.
    expect(await screen.findByLabelText('目标')).toHaveValue('storyboard.json');

    await userEvent.type(screen.getByLabelText(/写下修改意见/), '把外套改成风衣');
    await userEvent.click(screen.getByRole('button', { name: /写备注/ }));
    await waitFor(() =>
      expect(createChangeMock).toHaveBeenCalledWith('7', {
        target: 'storyboard.json',
        kind: 'annotation',
        content: '把外套改成风衣',
      }),
    );

    // The returned change lands in the list as pending, with a dispatch action.
    expect(await screen.findByText('待送回')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /送回重生成/ }));
    await waitFor(() => expect(dispatchMock).toHaveBeenCalledWith('7', 'chg-20260927-001'));

    // dispatch returns the updated change + routed issue: state flips to
    // dispatched and the toast names the issue.
    expect(toast.success).toHaveBeenCalledWith('已路由到 issue：wave3 分镜');
    expect(await screen.findByText('已送回')).toBeInTheDocument();
    expect(screen.getByText('#12')).toBeInTheDocument();
  });

  it('keeps the change pending when dispatch is refused (blocked issue → 400)', async () => {
    getMock.mockResolvedValue(response);
    createChangeMock.mockResolvedValue(pendingChange);
    // The backend refuses a blocked route with 400 + a Chinese message and
    // leaves the change file untouched — the row must stay pending.
    dispatchMock.mockRejectedValue(new ApiError(400, '送回重生成被阻塞：issue 已阻塞', null));
    renderPanel();

    await userEvent.type(await screen.findByLabelText(/写下修改意见/), '把外套改成风衣');
    await userEvent.click(screen.getByRole('button', { name: /写备注/ }));
    await userEvent.click(await screen.findByRole('button', { name: /送回重生成/ }));

    await waitFor(() => expect(dispatchMock).toHaveBeenCalledWith('7', 'chg-20260927-001'));
    expect(toast.error).toHaveBeenCalledWith('送回重生成被阻塞：issue 已阻塞');
    expect(screen.queryByText('已送回')).not.toBeInTheDocument();
    expect(screen.queryByText('#12')).not.toBeInTheDocument();
    expect(await screen.findByText('待送回')).toBeInTheDocument();
  });

  it('does not claim a routing success when the response leaves the change pending', async () => {
    getMock.mockResolvedValue(response);
    createChangeMock.mockResolvedValue(pendingChange);
    // Blocked issue: the backend deliberately keeps the change pending on disk
    // and echoes it back unchanged — the UI must follow the response, not
    // optimistically flip to "dispatched".
    dispatchMock.mockResolvedValue({ change: pendingChange, issue_id: 0, issue_title: '' });
    renderPanel();

    await userEvent.type(await screen.findByLabelText(/写下修改意见/), '把外套改成风衣');
    await userEvent.click(screen.getByRole('button', { name: /写备注/ }));
    await userEvent.click(await screen.findByRole('button', { name: /送回重生成/ }));

    await waitFor(() => expect(dispatchMock).toHaveBeenCalledWith('7', 'chg-20260927-001'));
    expect(toast.warning).toHaveBeenCalledWith('issue 处于阻塞状态，修改意见仍为待送回');
    expect(toast.success).not.toHaveBeenCalledWith(expect.stringContaining('已路由到 issue'));

    // Still pending: the row keeps its dispatch action and names no issue.
    expect(await screen.findByText('待送回')).toBeInTheDocument();
    expect(screen.queryByText('已送回')).not.toBeInTheDocument();
    expect(screen.queryByText('#12')).not.toBeInTheDocument();
  });
});
