import React from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';

// Mock @tanstack/react-router's <Link> as a plain <a> so PriorityCard renders
// without a router context. We only assert role=link textContent and click
// the toggle button, not navigation.
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
}));

import { PriorityZone } from './workspace-sidebar-priority-zone';
import { PRIORITY_KINDS } from './workspace-sidebar-helpers';
import type { Workspace, Project } from '@/types/api';

function makeWs(id: number, status: Workspace['status'], name: string): Workspace {
  return {
    id: String(id),
    issue_id: null,
    name,
    path: `/tmp/ws-${id}`,
    status,
    agent_pid: null,
    agent_status: 'idle',
    changes_count: 0,
    ahead_count: 0,
    cli_type: 'claude',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
}

function renderWithProviders(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>
    </QueryClientProvider>,
  );
}

const emptyProjects: Map<string, Project> = new Map();
const projectKey = (ws: Workspace) => ws.project_name ?? '';
const noopMarkDone = () => {};

// Tests run without an authenticated user, so the component falls back to
// uid=0 — matching the user-namespaced storage key the production code uses
// for the anonymous / single-user branch.
const PREFS_STORAGE_KEY = 'sidebarPriority:u0';

describe('PriorityZone', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('renders nothing when workspace list is empty', () => {
    const { container } = renderWithProviders(
      <PriorityZone
        workspaces={[]}
        projectsByKey={emptyProjects}
        projectKeyForWs={projectKey}
        activeWorkspaceId={undefined}
        onMarkDone={noopMarkDone}
        pendingIds={new Set()}
      />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders cards sorted by id descending', () => {
    const wss = [
      makeWs(10, 'needs_review', 'older'),
      makeWs(50, 'attention', 'newest'),
      makeWs(30, 'created', 'middle'),
    ];
    renderWithProviders(
      <PriorityZone
        workspaces={wss}
        projectsByKey={emptyProjects}
        projectKeyForWs={projectKey}
        activeWorkspaceId={undefined}
        onMarkDone={noopMarkDone}
        pendingIds={new Set()}
      />,
    );
    const links = screen.getAllByRole('link');
    expect(links).toHaveLength(3);
    expect(links[0].textContent).toContain('newest');
    expect(links[1].textContent).toContain('middle');
    expect(links[2].textContent).toContain('older');
  });

  it('caps to 3 items by default and shows expand toggle when >3', () => {
    const wss = Array.from({ length: 5 }, (_, i) =>
      makeWs(i + 1, 'needs_review', `ws-${i}`),
    );
    renderWithProviders(
      <PriorityZone
        workspaces={wss}
        projectsByKey={emptyProjects}
        projectKeyForWs={projectKey}
        activeWorkspaceId={undefined}
        onMarkDone={noopMarkDone}
        pendingIds={new Set()}
      />,
    );
    expect(screen.getAllByRole('link')).toHaveLength(3);
    expect(screen.getByText(/展开全部|Show all/i)).toBeInTheDocument();
  });

  it('expands to show all when toggle clicked', () => {
    const wss = Array.from({ length: 5 }, (_, i) =>
      makeWs(i + 1, 'needs_review', `ws-${i}`),
    );
    renderWithProviders(
      <PriorityZone
        workspaces={wss}
        projectsByKey={emptyProjects}
        projectKeyForWs={projectKey}
        activeWorkspaceId={undefined}
        onMarkDone={noopMarkDone}
        pendingIds={new Set()}
      />,
    );
    fireEvent.click(screen.getByText(/展开全部|Show all/i));
    expect(screen.getAllByRole('link')).toHaveLength(5);
    expect(screen.getByText(/收起|Collapse/i)).toBeInTheDocument();
  });

  describe('status filter pills', () => {
    function findPill(label: RegExp): HTMLButtonElement {
      // Pill buttons render label + count as separate spans; rely on
      // aria-pressed presence to disambiguate from the zone-collapse toggle.
      const buttons = screen.getAllByRole('button');
      const pill = buttons.find(
        (b) => b.hasAttribute('aria-pressed') && label.test(b.textContent ?? ''),
      );
      if (!pill) throw new Error(`pill matching ${label} not found`);
      return pill as HTMLButtonElement;
    }

    it('renders four filter pills with per-kind counts', () => {
      const wss = [
        makeWs(1, 'created', 'a'),
        makeWs(2, 'needs_review', 'b'),
        makeWs(3, 'needs_review', 'c'),
        makeWs(4, 'attention', 'd'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      // 4 status pills + zone-collapse toggle = 5 buttons minimum. Each pill
      // carries aria-pressed=true by default (all enabled at boot).
      // Filter pills carry visible text (label + count); the per-card pin
      // toggles also use aria-pressed but are icon-only (empty text), so
      // require non-empty text to select just the pills.
      const pills = screen
        .getAllByRole('button')
        .filter((b) => b.hasAttribute('aria-pressed') && (b.textContent ?? '').trim() !== '');
      expect(pills).toHaveLength(4);
      pills.forEach((p) => expect(p).toHaveAttribute('aria-pressed', 'true'));

      // Counts: not_started=1, needs_review=2, running=0, attention=1.
      expect(findPill(/未开始|Not started/i).textContent).toMatch(/1$/);
      expect(findPill(/待审核|Awaiting review/i).textContent).toMatch(/2$/);
      expect(findPill(/运行中|Running/i).textContent).toMatch(/0$/);
      expect(findPill(/需关注|Needs attention/i).textContent).toMatch(/1$/);
    });

    it('clicking a pill deselects its kind and hides those cards', () => {
      const wss = [
        makeWs(1, 'needs_review', 'review-one'),
        makeWs(2, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      expect(screen.getAllByRole('link')).toHaveLength(2);

      fireEvent.click(findPill(/待审核|Awaiting review/i));
      const links = screen.getAllByRole('link');
      expect(links).toHaveLength(1);
      expect(links[0].textContent).toContain('urgent-one');
      expect(findPill(/待审核|Awaiting review/i)).toHaveAttribute('aria-pressed', 'false');
    });

    it('clicking the only enabled pill resets the filter to all kinds', () => {
      // One workspace per kind so every pill is interactable. Pre-set
      // expanded=true so the 3-card visible cap doesn't shadow the 4th card.
      localStorage.setItem(
        PREFS_STORAGE_KEY,
        JSON.stringify({
          expanded: true,
          zoneCollapsed: false,
          enabledKinds: [...PRIORITY_KINDS],
        }),
      );
      const wss = [
        makeWs(1, 'created', 'pending-one'),
        makeWs(2, 'needs_review', 'review-one'),
        makeWs(3, 'running', 'running-one'),
        makeWs(4, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      expect(screen.getAllByRole('link')).toHaveLength(4);

      // Disable three → leave only attention.
      fireEvent.click(findPill(/未开始|Not started/i));
      fireEvent.click(findPill(/待审核|Awaiting review/i));
      fireEvent.click(findPill(/运行中|Running/i));
      expect(screen.getAllByRole('link')).toHaveLength(1);

      // Click the only-active pill → reset, all four cards back, every pill pressed.
      fireEvent.click(findPill(/需关注|Needs attention/i));
      expect(screen.getAllByRole('link')).toHaveLength(4);
      // Filter pills carry visible text (label + count); the per-card pin
      // toggles also use aria-pressed but are icon-only (empty text), so
      // require non-empty text to select just the pills.
      const pills = screen
        .getAllByRole('button')
        .filter((b) => b.hasAttribute('aria-pressed') && (b.textContent ?? '').trim() !== '');
      pills.forEach((p) => expect(p).toHaveAttribute('aria-pressed', 'true'));
    });

    it('shows empty-state copy when the filter excludes every workspace', () => {
      const wss = [makeWs(1, 'needs_review', 'only-review')];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      // Disable needs_review only — the rest have 0 items, so result is empty.
      fireEvent.click(findPill(/待审核|Awaiting review/i));
      expect(screen.queryAllByRole('link')).toHaveLength(0);
      expect(
        screen.getByText(/当前筛选下没有工作空间|No workspaces match the current filter/i),
      ).toBeInTheDocument();
    });

    it('restores the selected filter from localStorage on mount', () => {
      localStorage.setItem(
        PREFS_STORAGE_KEY,
        JSON.stringify({
          expanded: false,
          zoneCollapsed: false,
          enabledKinds: ['attention'],
        }),
      );
      const wss = [
        makeWs(1, 'needs_review', 'review-one'),
        makeWs(2, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      const links = screen.getAllByRole('link');
      expect(links).toHaveLength(1);
      expect(links[0].textContent).toContain('urgent-one');
    });

    it('persists the updated selection to localStorage on toggle', () => {
      const wss = [
        makeWs(1, 'needs_review', 'review-one'),
        makeWs(2, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );

      fireEvent.click(findPill(/待审核|Awaiting review/i));
      const raw = localStorage.getItem(PREFS_STORAGE_KEY);
      expect(raw).not.toBeNull();
      const parsed = JSON.parse(raw!) as { enabledKinds: string[] };
      expect(parsed.enabledKinds).not.toContain('needs_review');
      expect(parsed.enabledKinds).toEqual(
        expect.arrayContaining(['not_started', 'running', 'attention']),
      );
    });

    it('falls back to all-selected when localStorage payload is malformed', () => {
      localStorage.setItem(PREFS_STORAGE_KEY, '{not json');
      const wss = [
        makeWs(1, 'needs_review', 'review-one'),
        makeWs(2, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      // All four pills should be enabled and both cards visible.
      // Filter pills carry visible text (label + count); the per-card pin
      // toggles also use aria-pressed but are icon-only (empty text), so
      // require non-empty text to select just the pills.
      const pills = screen
        .getAllByRole('button')
        .filter((b) => b.hasAttribute('aria-pressed') && (b.textContent ?? '').trim() !== '');
      expect(pills).toHaveLength(4);
      pills.forEach((p) => expect(p).toHaveAttribute('aria-pressed', 'true'));
      expect(screen.getAllByRole('link')).toHaveLength(2);
    });

    it('a pill with count=0 is aria-disabled and ignores clicks', () => {
      // No running workspaces — the "运行中 0" pill should be a no-op.
      const wss = [
        makeWs(1, 'needs_review', 'review-one'),
        makeWs(2, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      const runningPill = findPill(/运行中|Running/i);
      expect(runningPill).toHaveAttribute('aria-disabled', 'true');
      fireEvent.click(runningPill);
      // State unchanged: still aria-pressed=true and still 2 cards visible.
      expect(runningPill).toHaveAttribute('aria-pressed', 'true');
      expect(screen.getAllByRole('link')).toHaveLength(2);
      // Nothing was written to localStorage (the click was a no-op).
      expect(localStorage.getItem(PREFS_STORAGE_KEY)).toBeNull();
    });

    it('hides the pill row when the whole zone is collapsed', () => {
      localStorage.setItem(
        PREFS_STORAGE_KEY,
        JSON.stringify({
          expanded: false,
          zoneCollapsed: true,
          enabledKinds: [...PRIORITY_KINDS],
        }),
      );
      const wss = [
        makeWs(1, 'needs_review', 'review-one'),
        makeWs(2, 'attention', 'urgent-one'),
      ];
      renderWithProviders(
        <PriorityZone
          workspaces={wss}
          projectsByKey={emptyProjects}
          projectKeyForWs={projectKey}
          activeWorkspaceId={undefined}
          onMarkDone={noopMarkDone}
          pendingIds={new Set()}
        />,
      );
      // Header still visible; pills + cards are not.
      expect(screen.getByText(/需要我关注|Needs my attention/i)).toBeInTheDocument();
      // Filter pills carry visible text (label + count); the per-card pin
      // toggles also use aria-pressed but are icon-only (empty text), so
      // require non-empty text to select just the pills.
      const pills = screen
        .getAllByRole('button')
        .filter((b) => b.hasAttribute('aria-pressed') && (b.textContent ?? '').trim() !== '');
      expect(pills).toHaveLength(0);
      expect(screen.queryAllByRole('link')).toHaveLength(0);
    });
  });
});
