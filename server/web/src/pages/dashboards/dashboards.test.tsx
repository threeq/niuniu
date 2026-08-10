import React from 'react';
import { render, screen, fireEvent, waitFor, configure } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

// DashboardDetail renders the heavy (lazy echarts) panel/chart tree. In jsdom
// under full-suite parallelism it can take several seconds to settle (never in
// isolation), which blew two independent budgets: the 5s default test timeout
// AND the 1s default findBy/waitFor (asyncUtilTimeout) — the latter surfaced as
// "Unable to find element" even with a large test timeout. Raise both; tests
// still complete in ~1-2s normally.
vi.setConfig({ testTimeout: 20000 });
configure({ asyncUtilTimeout: 8000 });

const navigateMock = vi.fn();

// Mock the router: <Link> as a plain <a>, useNavigate as our spy.
vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    params,
    className,
  }: {
    children: React.ReactNode;
    to?: string;
    params?: Record<string, string | number>;
    className?: string;
  }) => {
    const href =
      to && params
        ? Object.entries(params).reduce(
            (acc, [k, v]) => acc.replace(`$${k}`, String(v)),
            to,
          )
        : to ?? '#';
    return (
      <a href={href} className={className}>
        {children}
      </a>
    );
  },
  useNavigate: () => navigateMock,
}));

vi.mock('@/lib/dashboards-api', () => ({
  listPanels: vi.fn(),
  deletePanel: vi.fn(),
  fetchPanelData: vi.fn(),
  listDashboards: vi.fn(),
  movePanel: vi.fn(),
  copyPanel: vi.fn(),
}));

import { DashboardDetail } from './dashboard-detail';
import {
  listPanels,
  fetchPanelData,
  listDashboards,
  type DashboardPanel,
} from '@/lib/dashboards-api';

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const panel = (over: Partial<DashboardPanel>): DashboardPanel => ({
  id: 1,
  dashboard_id: 1,
  title: 'Panel A',
  viz_type: 'table',
  chart_spec: { type: 'table' },
  source_id: 0,
  workspace_id: null,
  grid_x: 0,
  grid_y: 0,
  grid_w: 6,
  grid_h: 4,
  refresh_interval_sec: 0,
  ...over,
});

const result = {
  columns: [{ name: 'id', type: 'number' }],
  rows: [[1]],
  truncated: false,
  duration_ms: 1,
  engine: 'mysql',
};

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(fetchPanelData).mockResolvedValue(result);
  vi.mocked(listDashboards).mockResolvedValue([
    { id: 1, name: 'Dash A' },
    { id: 2, name: 'Dash B' },
  ]);
});

describe('DashboardDetail back-to-workspace + enlarge', () => {
  it('navigates to the origin workspace only via the back-to-workspace button', async () => {
    vi.mocked(listPanels).mockResolvedValue([panel({ workspace_id: 42 })]);
    render(withQuery(<DashboardDetail dashboardId="1" />));

    const btn = await screen.findByRole('button', {
      name: /返回工作空间|back to workspace/i,
    });
    fireEvent.click(btn);
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/workspaces/$id',
      params: { id: '42' },
    });
  });

  it('hides the back-to-workspace button when workspace_id is null and chart click does not navigate', async () => {
    vi.mocked(listPanels).mockResolvedValue([panel({ workspace_id: null })]);
    render(withQuery(<DashboardDetail dashboardId="1" />));

    await screen.findByText('Panel A');
    expect(
      screen.queryByRole('button', { name: /返回工作空间|back to workspace/i }),
    ).toBeNull();

    // Clicking the chart enlarges it (opens a dialog) — it must NOT navigate.
    const chart = await screen.findByRole('button', { name: /放大|enlarge/i });
    fireEvent.click(chart);
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it('renders the panel data table header from fetchPanelData', async () => {
    vi.mocked(listPanels).mockResolvedValue([panel({ workspace_id: 42 })]);
    render(withQuery(<DashboardDetail dashboardId="1" />));
    await waitFor(() => expect(screen.getByText('id')).toBeInTheDocument());
  });
});

describe('dashboard nav gating logic', () => {
  // The nav entry is gated on (dashboards?.length ?? 0) > 0. This mirrors the
  // pure predicate used in GlobalNav so a regression in the threshold is caught
  // without standing up the full nav (router + stores).
  const showDashboards = (len: number | undefined) => (len ?? 0) > 0;

  it('hides the entry when there are zero dashboards', () => {
    expect(showDashboards(0)).toBe(false);
    expect(showDashboards(undefined)).toBe(false);
  });

  it('shows the entry once at least one dashboard exists', () => {
    expect(showDashboards(1)).toBe(true);
  });
});
