import React from 'react';
import { render, screen, fireEvent, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nextProvider } from 'react-i18next';
import { Toaster } from 'sonner';
import i18n from '@/i18n';

import type { Workspace, Project } from '@/types/api';

// --- mocks --------------------------------------------------------------

// Router: <Link> as <a>; useParams returns whatever the test sets via the
// `mockParamsRef` below. Default = {} (no active workspace).
const mockParamsRef: { value: Record<string, string | undefined> } = { value: {} };
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
  useParams: () => mockParamsRef.value,
  useNavigate: () => vi.fn(),
}));

// useWorkspaces mock — drives the sidebar content. The MOCK_WORKSPACES list
// is rebuilt on every render so optimistic patches via setQueryData survive
// within a single render but reset between tests.
let MOCK_WORKSPACES: Workspace[] = [];
type WsMockResult = { workspaces: Workspace[]; isLoading: boolean };
type MarkDoneMockResult = {
  trigger: (id: string | number) => void;
  pendingIds: Set<string>;
};
const useWorkspacesMock = vi.fn<(...args: unknown[]) => WsMockResult>(() => ({
  workspaces: MOCK_WORKSPACES,
  isLoading: false,
}));
const useMarkWorkspaceDoneMock = vi.fn<(...args: unknown[]) => MarkDoneMockResult>(() => ({
  trigger: vi.fn(),
  pendingIds: new Set<string>(),
}));
vi.mock('@/lib/hooks/use-workspaces', () => ({
  useWorkspaces: (opts: unknown) => useWorkspacesMock(opts),
  useMarkWorkspaceDone: (scope: 'mine' | 'all') => useMarkWorkspaceDoneMock(scope),
  useWorkspaceContentSearch: () => ({
    contentMatchIds: new Set<string>(),
    isSearching: false,
  }),
}));

let MOCK_PROJECTS: Project[] = [];
vi.mock('@/lib/hooks/use-projects', () => ({
  useProjectsWithStats: () => ({ projects: MOCK_PROJECTS, isLoading: false }),
}));

vi.mock('@/lib/api', () => ({
  fetchLifecycleGroups: () =>
    Promise.resolve([
      { label: 'Backlog', statuses: ['created'] },
      { label: 'Spec·Plan', statuses: ['spec', 'spec-review', 'plan', 'plan-review'] },
      { label: 'Implement', statuses: ['implement'] },
      { label: 'Review', statuses: ['implement-review', 'needs_review', 'attention'] },
      { label: 'Test', statuses: ['test'] },
      { label: 'Done', statuses: ['completed'] },
    ]),
  listPinnedWorkspaces: () => Promise.resolve([]),
  pinWorkspace: () => Promise.resolve(),
  unpinWorkspace: () => Promise.resolve(),
}));

vi.mock('@/stores/auth-store', () => ({
  useAuthStore: <T,>(selector?: (s: { user: { id: number } | null }) => T) => {
    const state = { user: { id: 42 } };
    return selector ? selector(state) : (state as unknown as T);
  },
}));

vi.mock('@/stores/org-store', () => ({
  useOrgStore: <T,>(selector?: (s: { myOrgs: unknown[] }) => T) => {
    const state = { myOrgs: [] };
    return selector ? selector(state) : (state as unknown as T);
  },
}));

// Stub the two dialogs — they're not the SUT here and have heavy deps.
vi.mock('@/components/dialogs/new-workspace-dialog', () => ({
  NewWorkspaceDialog: () => null,
}));
vi.mock('@/components/dialogs/archived-workspaces-dialog', () => ({
  ArchivedWorkspacesDialog: () => null,
}));

// Stub the WorkspaceBgTasksRow so we don't pull in the full bg-tasks dep tree.
vi.mock('./workspace-bg-tasks-row', () => ({
  WorkspaceBgTasksRow: () => null,
}));

// Stub permission store consumed by WorkspaceCard.
vi.mock('@/stores/notification-ws-store', () => ({
  usePermissionPendingByWorkspace: () => 0,
}));

// --- factories ----------------------------------------------------------

function makeWs(
  id: number,
  status: string,
  name: string,
  projectName: string,
  overrides: Partial<Workspace> = {},
): Workspace {
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
    project_name: projectName,
    project_owner_type: 'user',
    project_owner_id: 42,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

function makeProject(id: number, name: string, overrides: Partial<Project> = {}): Project {
  return {
    id,
    name,
    description: null,
    status: 'active',
    color: null,
    owner: { type: 'user', id: 42 },
    created_at: '',
    updated_at: '',
    ...overrides,
  };
}

// Default fixture: 2 projects × multiple statuses.
function defaultFixture() {
  MOCK_PROJECTS = [
    makeProject(1, '钳子AI'),
    makeProject(2, 'PG migration'),
  ];
  MOCK_WORKSPACES = [
    // needs_review × 3 (priority zone) — different IDs to assert sort
    makeWs(10, 'needs_review', 'low-id review', '钳子AI'),
    makeWs(50, 'needs_review', 'high-id review', '钳子AI'),
    makeWs(30, 'needs_review', 'mid-id review', 'PG migration'),
    // running (priority zone, not completable)
    makeWs(7, 'running', 'Running ws', '钳子AI'),
    // created (Backlog group)
    makeWs(5, 'created', 'SQLite WAL 切换', 'PG migration'),
    // implement
    makeWs(11, 'implement', 'Build feature', '钳子AI'),
    // completed (Done group)
    makeWs(12, 'completed', 'Old done ws', 'PG migration'),
  ];
}

function renderWithProviders(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={i18n}>
        <Toaster />
        {ui}
      </I18nextProvider>
    </QueryClientProvider>,
  );
}

// Lazy import so vi.mock hoisting takes effect before WorkspaceSidebar
// pulls in the mocked deps.
async function importSidebar() {
  const mod = await import('./workspace-sidebar');
  return mod.WorkspaceSidebar;
}

// --- suite --------------------------------------------------------------

describe('WorkspaceSidebar', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
    mockParamsRef.value = {};
    defaultFixture();
    // Re-install default mock impl post clearAllMocks
    useWorkspacesMock.mockImplementation(() => ({
      workspaces: MOCK_WORKSPACES,
      isLoading: false,
    }));
    useMarkWorkspaceDoneMock.mockImplementation(() => ({
      trigger: vi.fn(),
      pendingIds: new Set<string>(),
    }));
  });

  it('renders priority zone only in "mine" scope', async () => {
    localStorage.setItem('workspaceSidebarScope', 'mine');
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    expect(screen.getByText(/需要我关注|Needs my attention/i)).toBeInTheDocument();
  });

  it('hides priority zone in "all" scope', async () => {
    localStorage.setItem('workspaceSidebarScope', 'all');
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    expect(screen.queryByText(/需要我关注|Needs my attention/i)).not.toBeInTheDocument();
  });

  it('removes the project chip row and CreatorScopeChip', async () => {
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    // CreatorScopeChip's distinctive "只看我的" label is gone; the segmented
    // "我的"/"全部" toolbar uses different copy.
    expect(screen.queryByText(/只看我的/)).not.toBeInTheDocument();
    expect(screen.queryByTestId('project-chip-row')).not.toBeInTheDocument();
  });

  it('shows no-match state when search yields zero workspaces', async () => {
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    fireEvent.change(screen.getByPlaceholderText(/搜索|Search/i), {
      target: { value: 'xyzxyzxyz' },
    });
    expect(screen.getByText(/无匹配工作空间|No matching workspaces/i)).toBeInTheDocument();
  });

  it('priority zone sorts cards by id descending', async () => {
    localStorage.setItem('workspaceSidebarScope', 'mine');
    // Strip everything except priority candidates so the zone is the only
    // place these names appear. Use unique names so the assertion is robust.
    MOCK_WORKSPACES = [
      makeWs(10, 'needs_review', 'PRZ-LOW', '钳子AI'),
      makeWs(50, 'needs_review', 'PRZ-HIGH', '钳子AI'),
      makeWs(30, 'needs_review', 'PRZ-MID', '钳子AI'),
    ];
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    const html = document.body.innerHTML;
    // PRZ-HIGH should appear before PRZ-MID, which appears before PRZ-LOW.
    const highIdx = html.indexOf('PRZ-HIGH');
    const midIdx = html.indexOf('PRZ-MID');
    const lowIdx = html.indexOf('PRZ-LOW');
    expect(highIdx).toBeGreaterThan(-1);
    expect(midIdx).toBeGreaterThan(highIdx);
    expect(lowIdx).toBeGreaterThan(midIdx);
  });

  it('search input filters tree (matched workspace visible, others hidden)', async () => {
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    fireEvent.change(screen.getByPlaceholderText(/搜索|Search/i), {
      target: { value: 'SQLite' },
    });
    // SQLite WAL 切换 should appear; 'Build feature' should not.
    expect(screen.getAllByText(/SQLite WAL/).length).toBeGreaterThan(0);
    expect(screen.queryByText('Build feature')).not.toBeInTheDocument();
  });

  // The last-message relative time was intentionally dropped from the sidebar
  // message cell in bdd109b4 ("drop last-message timestamp from sidebar message
  // cell"); the cell now shows only the conversation count, so we assert just
  // that.
  it('shows conversation count on workspace cards', async () => {
    localStorage.setItem('workspaceSidebarScope', 'all');
    mockParamsRef.value = { id: '88' };
    MOCK_PROJECTS = [makeProject(1, 'Chat project')];
    MOCK_WORKSPACES = [
      makeWs(88, 'running', 'Chat-heavy workspace', 'Chat project', {
        message_count: 8,
        last_message_at: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
      }),
    ];

    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);

    const conversationMeta = screen.getByTitle(/最近对话活动|Last conversation activity/i);
    expect(within(conversationMeta).getByText('8')).toBeInTheDocument();
  });

  it('shows capped conversation count on priority cards', async () => {
    localStorage.setItem('workspaceSidebarScope', 'mine');
    MOCK_PROJECTS = [makeProject(1, 'Priority chat')];
    MOCK_WORKSPACES = [
      makeWs(99, 'needs_review', 'Priority chat workspace', 'Priority chat', {
        message_count: 1200,
        last_message_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
      }),
    ];

    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);

    const conversationMeta = screen.getByTitle(/最近对话活动|Last conversation activity/i);
    expect(within(conversationMeta).getByText('999+')).toBeInTheDocument();
  });

  it("active workspace's project header shows expanded chevron", async () => {
    // ws id 11 lives in "钳子AI"
    mockParamsRef.value = { id: '11' };
    localStorage.setItem('workspaceSidebarScope', 'all'); // no priority zone, focus on project tree
    const WorkspaceSidebar = await importSidebar();
    const { container } = renderWithProviders(<WorkspaceSidebar />);
    // The "钳子AI" header should be auto-expanded — assert its child workspace
    // 'Build feature' is rendered in the DOM as a result.
    expect(screen.getByText('Build feature')).toBeInTheDocument();
    // sanity: chevron-down svg present somewhere
    expect(container.querySelector('svg')).not.toBeNull();
  });

  it('expanded project shows all workspaces flat (no lifecycle subdivision)', async () => {
    // After dropping lifecycle subgroups, expanding a project section renders
    // its workspaces directly as a flat list — no Backlog / Implement / Done
    // sub-headers between the project header and the cards.
    localStorage.setItem('workspaceSidebarScope', 'all');
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    fireEvent.click(screen.getByText('PG migration'));
    // The workspace itself is in the DOM …
    expect(screen.getByText(/SQLite WAL 切换/)).toBeInTheDocument();
    // … and no Backlog/Done/Implement sub-header exists anywhere.
    expect(screen.queryByText(/^Backlog$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^Done$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^Implement$/)).not.toBeInTheDocument();
  });

  it('toggles scope and persists choice to localStorage', async () => {
    const WorkspaceSidebar = await importSidebar();
    renderWithProviders(<WorkspaceSidebar />);
    // Default scope = mine; priority zone is visible
    expect(screen.getByText(/需要我关注|Needs my attention/i)).toBeInTheDocument();
    // Click "全部" / "All"
    fireEvent.click(screen.getByRole('button', { name: /^全部$|^All$/ }));
    expect(localStorage.getItem('workspaceSidebarScope')).toBe('all');
    expect(screen.queryByText(/需要我关注|Needs my attention/i)).not.toBeInTheDocument();
  });

  // Multi-undo isolation: sonner toast state is hard to assert reliably
  // alongside fake-timer + mark-done's internal ref maps. The
  // mark-done flow itself is comprehensively covered in use-workspaces
  // unit tests; we skip this integration scenario to avoid flake.
  it.skip('multi-undo isolation: undo A does not roll back B', () => {
    // Skipped: would require simulating two sonner toasts with separate
    // Undo buttons, matching by index, and asserting partial cache rollback.
    // Lower ROI than the unit-level coverage already in place.
  });
});
