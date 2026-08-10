import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { QuickActionsPopup } from './quick-actions-popup';
import { useQuickActionsStore } from '@/stores/quick-actions-store';
import { useOrgStore } from '@/stores/org-store';
import { useAuthStore } from '@/stores/auth-store';

vi.mock('@/stores/quick-actions-store');
vi.mock('@/stores/org-store');
vi.mock('@/stores/auth-store');

const setupStores = (opts: {
  actions: Array<{ id: number; label: string; content: string; auto_send: boolean; position: number; owner?: { type: string; id: number; name?: string } }>;
  myOrgs: Array<{ id: number; name: string; slug: string; description: string; created_at: string; updated_at: string }>;
  userId: number;
  sceneActions?: Array<{ slug: string; label: string; content: string; auto_send: boolean; source: 'scene' }>;
}) => {
  vi.mocked(useQuickActionsStore).mockReturnValue({
    actions: opts.actions,
    sceneActions: opts.sceneActions ?? [],
    fetchActions: vi.fn(),
    loading: false,
    addAction: vi.fn(),
    updateAction: vi.fn(),
    removeAction: vi.fn(),
    setLocalOrder: vi.fn(),
    persistOrder: vi.fn(),
  } as ReturnType<typeof useQuickActionsStore>);
  vi.mocked(useOrgStore).mockImplementation((sel: any) => sel({ myOrgs: opts.myOrgs }));
  vi.mocked(useAuthStore).mockImplementation((sel: any) => sel({ user: { id: opts.userId, username: 'u', role: 'admin' } }));
};

beforeEach(() => vi.clearAllMocks());

describe('QuickActionsPopup grouping', () => {
  it('renders group headers in Personal-then-myOrgs order', async () => {
    setupStores({
      userId: 7,
      myOrgs: [
        { id: 10, name: 'OrgA', slug: 'a', description: '', created_at: '', updated_at: '' },
        { id: 20, name: 'OrgB', slug: 'b', description: '', created_at: '', updated_at: '' },
      ],
      actions: [
        { id: 1, label: 'b1', content: '', auto_send: false, position: 0, owner: { type: 'org', id: 20, name: 'OrgB' } },
        { id: 2, label: 'p1', content: '', auto_send: false, position: 1, owner: { type: 'user', id: 7 } },
        { id: 3, label: 'a1', content: '', auto_send: false, position: 2, owner: { type: 'org', id: 10, name: 'OrgA' } },
      ],
    });
    render(<QuickActionsPopup onApply={() => {}} onAutoSend={() => {}} onManage={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: /快捷|Quick/ }));
    const headers = await screen.findAllByTestId('owner-group-header');
    // i18n test-setup uses zh-CN; groupPersonal translates to "个人"
    expect(headers.map((h) => h.textContent)).toEqual(['个人', 'OrgA', 'OrgB']);
  });

  it('renders flat list when user has no orgs', async () => {
    setupStores({
      userId: 7,
      myOrgs: [],
      actions: [
        { id: 1, label: 'p1', content: '', auto_send: false, position: 0, owner: { type: 'user', id: 7 } },
        { id: 2, label: 'p2', content: '', auto_send: false, position: 1, owner: { type: 'user', id: 7 } },
      ],
    });
    render(<QuickActionsPopup onApply={() => {}} onAutoSend={() => {}} onManage={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: /快捷|Quick/ }));
    expect(screen.queryByTestId('owner-group-header')).toBeNull();
  });
});

describe('QuickActionsPopup search', () => {
  it('filters rows by label and content (case-insensitive)', async () => {
    setupStores({
      userId: 7,
      myOrgs: [],
      actions: [
        { id: 1, label: 'Deploy', content: 'run deploy.sh', auto_send: false, position: 0, owner: { type: 'user', id: 7 } },
        { id: 2, label: 'Review', content: 'review the diff', auto_send: false, position: 1, owner: { type: 'user', id: 7 } },
      ],
    });
    render(<QuickActionsPopup onApply={() => {}} onAutoSend={() => {}} onManage={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: /快捷|Quick/ }));

    expect(screen.getByText('Deploy')).toBeInTheDocument();
    expect(screen.getByText('Review')).toBeInTheDocument();

    // Match by label.
    fireEvent.change(screen.getByPlaceholderText(/搜索|Search/), { target: { value: 'depl' } });
    expect(screen.getByText('Deploy')).toBeInTheDocument();
    expect(screen.queryByText('Review')).toBeNull();

    // Match by content (the other row's content), case-insensitive.
    fireEvent.change(screen.getByPlaceholderText(/搜索|Search/), { target: { value: 'DIFF' } });
    expect(screen.queryByText('Deploy')).toBeNull();
    expect(screen.getByText('Review')).toBeInTheDocument();

    // No match → search-empty message, manage button still present.
    fireEvent.change(screen.getByPlaceholderText(/搜索|Search/), { target: { value: 'zzz' } });
    expect(screen.queryByText('Deploy')).toBeNull();
    expect(screen.queryByText('Review')).toBeNull();
  });

  it('renders scene-provided actions in their own group and applies on click', async () => {
    const onApply = vi.fn();
    setupStores({
      userId: 7,
      myOrgs: [],
      actions: [
        { id: 1, label: 'mine', content: 'personal one', auto_send: false, position: 0, owner: { type: 'user', id: 7 } },
      ],
      sceneActions: [
        { slug: 'summarize-research', label: '汇总调研发现', content: 'do the summary', auto_send: false, source: 'scene' },
      ],
    });
    render(<QuickActionsPopup onApply={onApply} onAutoSend={() => {}} onManage={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: /快捷|Quick/ }));

    // Scene group header is present and distinct from the personal action.
    expect(screen.getByTestId('scene-group-header')).toBeInTheDocument();
    const sceneRow = screen.getByText('汇总调研发现');
    expect(sceneRow).toBeInTheDocument();

    // Clicking a scene action applies its content (scene actions never auto-send).
    fireEvent.click(sceneRow);
    expect(onApply).toHaveBeenCalledWith('do the summary');
  });

  it('exposes the manage action as an icon button with an accessible label', async () => {
    const onManage = vi.fn();
    setupStores({
      userId: 7,
      myOrgs: [],
      actions: [
        { id: 1, label: 'p1', content: '', auto_send: false, position: 0, owner: { type: 'user', id: 7 } },
      ],
    });
    render(<QuickActionsPopup onApply={() => {}} onAutoSend={() => {}} onManage={onManage} />);
    fireEvent.click(screen.getByRole('button', { name: /快捷|Quick/ }));
    fireEvent.click(screen.getByRole('button', { name: /管理快捷操作|Manage quick actions/ }));
    expect(onManage).toHaveBeenCalledTimes(1);
  });
});
