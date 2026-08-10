import { vi, it, expect, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { WorkspaceProjectionBanner } from './workspace-projection-banner';
import { workspaceSceneApi } from '@/lib/api';
import type { ApplyResult } from '@/types/api';

// The test setup (src/i18n/test-setup.ts) forces zh-CN, so assertions match
// the zh-CN strings under the `banner` key in scenes.json.
vi.mock('@/lib/api', () => ({
  workspaceSceneApi: {
    getProjection: vi.fn(),
    recompute: vi.fn(),
    installPlugins: vi.fn(),
    dismissPlugin: vi.fn(),
  },
}));

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

function baseResult(overrides: Partial<ApplyResult>): ApplyResult {
  return {
    projection: {} as never,
    missing_credentials: [],
    install_failures: [],
    restart_required: false,
    digest: 'd',
    dismissed_plugins: [],
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

it('offers an ignore button on a failed plugin row and calls dismissPlugin', async () => {
  vi.mocked(workspaceSceneApi.dismissPlugin).mockResolvedValue(
    baseResult({ dismissed_plugins: ['document-skills@claude-plugins-official'] }),
  );
  const projection = baseResult({
    install_failures: [
      {
        source: 'document-skills@claude-plugins-official',
        status: 'failed',
        stderr: 'not found in marketplace',
      },
    ],
  });

  wrap(<WorkspaceProjectionBanner workspaceId={42} projection={projection} />);

  // The failure banner renders with an "忽略" (Ignore) button.
  expect(screen.getByTestId('banner-install-failures')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: /忽略/ }));

  await waitFor(() =>
    expect(workspaceSceneApi.dismissPlugin).toHaveBeenCalledWith(
      42,
      'document-skills@claude-plugins-official',
      true,
    ),
  );
});

it('renders a restore section for already-dismissed plugins', async () => {
  vi.mocked(workspaceSceneApi.dismissPlugin).mockResolvedValue(baseResult({}));
  const projection = baseResult({
    dismissed_plugins: ['document-skills@claude-plugins-official'],
  });

  wrap(<WorkspaceProjectionBanner workspaceId={7} projection={projection} />);

  expect(screen.getByTestId('banner-dismissed-plugins')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: /恢复/ }));

  await waitFor(() =>
    expect(workspaceSceneApi.dismissPlugin).toHaveBeenCalledWith(
      7,
      'document-skills@claude-plugins-official',
      false,
    ),
  );
});
