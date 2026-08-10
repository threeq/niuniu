import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { QuickActionsDialog } from './quick-actions-dialog';
import { useQuickActionsStore } from '@/stores/quick-actions-store';
import { useOrgStore } from '@/stores/org-store';
import { useAuthStore } from '@/stores/auth-store';

vi.mock('@/stores/quick-actions-store');
vi.mock('@/stores/org-store');
vi.mock('@/stores/auth-store');

const baseAction = (over: Partial<{ id: number; label: string; owner: { type: string; id: number; name?: string } }>) => ({
  id: over.id!,
  label: over.label ?? `a${over.id}`,
  content: 'c',
  auto_send: false,
  position: over.id! - 1,
  owner: over.owner!,
});

const setupStores = (opts: {
  actions: ReturnType<typeof baseAction>[];
  myOrgs: Array<{ id: number; name: string; slug: string; description: string; created_at: string; updated_at: string }>;
  userId: number;
  setLocalOrder?: (xs: ReturnType<typeof baseAction>[]) => void;
}) => {
  vi.mocked(useQuickActionsStore).mockReturnValue({
    actions: opts.actions,
    fetchActions: vi.fn(),
    loading: false,
    addAction: vi.fn(),
    updateAction: vi.fn(),
    removeAction: vi.fn(),
    setLocalOrder: opts.setLocalOrder ?? vi.fn(),
    persistOrder: vi.fn(),
  } as ReturnType<typeof useQuickActionsStore>);
  vi.mocked(useOrgStore).mockImplementation((sel: any) => sel({ myOrgs: opts.myOrgs }));
  vi.mocked(useAuthStore).mockImplementation((sel: any) => sel({ user: { id: opts.userId, username: 'u', role: 'admin' } }));
};

beforeEach(() => vi.clearAllMocks());

describe('QuickActionsDialog grouping', () => {
  it('renders group headers and no per-row OwnerBadge', () => {
    setupStores({
      userId: 7,
      myOrgs: [{ id: 10, name: 'OrgA', slug: 'a', description: '', created_at: '', updated_at: '' }],
      actions: [
        baseAction({ id: 1, label: 'p1', owner: { type: 'user', id: 7 } }),
        baseAction({ id: 2, label: 'a1', owner: { type: 'org', id: 10, name: 'OrgA' } }),
      ],
    });
    render(<QuickActionsDialog open={true} onOpenChange={() => {}} />);
    const headers = screen.getAllByTestId('owner-group-header');
    // Test setup forces zh-CN; groupPersonal => "个人"
    expect(headers.map((h) => h.textContent)).toEqual(['个人', 'OrgA']);
    // No OwnerBadge inside a row — the OwnerBadge component renders Building2/User icon + label;
    // assert that the only places "OrgA" appears are the group header(s), not duplicated under p1.
    const orgaMentions = screen.getAllByText('OrgA');
    expect(orgaMentions.length).toBe(1);
  });

  it('within-group drag reorders only that group; cross-group drag is no-op', () => {
    const setLocalOrder = vi.fn();
    setupStores({
      userId: 7,
      myOrgs: [{ id: 10, name: 'OrgA', slug: 'a', description: '', created_at: '', updated_at: '' }],
      actions: [
        baseAction({ id: 1, label: 'p1', owner: { type: 'user', id: 7 } }),
        baseAction({ id: 2, label: 'p2', owner: { type: 'user', id: 7 } }),
        baseAction({ id: 3, label: 'a1', owner: { type: 'org', id: 10, name: 'OrgA' } }),
      ],
      setLocalOrder,
    });
    render(<QuickActionsDialog open={true} onOpenChange={() => {}} />);
    const rows = screen.getAllByTestId(/^qa-row-/); // Personal: p1,p2; Org: a1
    expect(rows.map((r) => r.dataset.testid)).toEqual(['qa-row-1', 'qa-row-2', 'qa-row-3']);

    // Within-group: p2 dragged onto p1 -> [p2, p1, a1]
    fireEvent.dragStart(rows[1]);
    fireEvent.dragOver(rows[0]);
    expect(setLocalOrder).toHaveBeenLastCalledWith(
      expect.arrayContaining([
        expect.objectContaining({ id: 2 }),
        expect.objectContaining({ id: 1 }),
        expect.objectContaining({ id: 3 }),
      ]),
    );
    expect((setLocalOrder.mock.lastCall![0] as ReturnType<typeof baseAction>[]).map((a) => a.id)).toEqual([2, 1, 3]);

    setLocalOrder.mockClear();

    // Cross-group: p1 dragged onto a1 -> no setLocalOrder call
    fireEvent.dragStart(rows[0]);
    fireEvent.dragOver(rows[2]);
    expect(setLocalOrder).not.toHaveBeenCalled();
  });
});
