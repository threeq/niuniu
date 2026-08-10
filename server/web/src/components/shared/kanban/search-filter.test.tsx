import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { SearchFilter, type FilterState } from './search-filter';
import type { Issue, Label } from '@/types/api';

const wrap = (ui: React.ReactElement) => (
  <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>
);

function makeIssue(over: Partial<Issue>): Issue {
  return {
    id: 1,
    column_id: 1,
    project_id: 1,
    title: 'Issue',
    description: null,
    position: 0,
    assignees: [],
    labels: [],
    ...over,
  } as Issue;
}

function makeLabel(over: Partial<Label>): Label {
  return {
    id: 1,
    project_id: 1,
    name: 'bug',
    color: '#ff0000',
    description: '',
    created_at: '',
    created_by: 1,
    ...over,
  };
}

describe('SearchFilter label dimension', () => {
  it('filters issues down to those carrying a selected label', () => {
    const labelBug = makeLabel({ id: 10, name: 'bug' });
    const labelDoc = makeLabel({ id: 20, name: 'docs' });
    const withBug = makeIssue({ id: 1, title: 'has bug', labels: [labelBug] });
    const withDoc = makeIssue({ id: 2, title: 'has doc', labels: [labelDoc] });
    const issues = [withBug, withDoc];

    let last: { filtered: Issue[]; state: FilterState } | null = null;
    const onFilterChange = vi.fn((filtered: Issue[], state: FilterState) => {
      last = { filtered, state };
    });

    render(
      wrap(
        <SearchFilter
          issues={issues}
          labels={[labelBug, labelDoc]}
          onFilterChange={onFilterChange}
        />
      )
    );

    // Open the label popover and select "bug".
    fireEvent.click(screen.getByLabelText(i18n.t('projects:kanban.search.labelLabel')));
    fireEvent.click(screen.getByText('bug'));

    expect(last).not.toBeNull();
    expect(last!.state.labelIds).toEqual([10]);
    expect(last!.filtered.map((i) => i.id)).toEqual([1]);
  });

  it('hides sub-issues by default and reveals them when 全部 is toggled', () => {
    const top = makeIssue({ id: 1, title: 'top', parent_issue_id: null });
    const child = makeIssue({ id: 2, title: 'child', parent_issue_id: 1 });

    let last: { filtered: Issue[]; state: FilterState } | null = null;
    const onFilterChange = vi.fn((filtered: Issue[], state: FilterState) => {
      last = { filtered, state };
    });

    render(wrap(<SearchFilter issues={[top, child]} onFilterChange={onFilterChange} />));

    // Default: sub-issue (id 2) is hidden, only the top-level issue shows.
    expect(last).not.toBeNull();
    expect(last!.state.showSubIssues).toBe(false);
    expect(last!.filtered.map((i) => i.id)).toEqual([1]);

    // Toggle "全部" — sub-issues become visible.
    fireEvent.click(screen.getByText(i18n.t('projects:kanban.search.showAll')));
    expect(last!.state.showSubIssues).toBe(true);
    expect(last!.filtered.map((i) => i.id)).toEqual([1, 2]);
  });

  it('filters issues by priority', () => {
    const a = makeIssue({ id: 1, title: 'a', priority: 0 });
    const b = makeIssue({ id: 2, title: 'b', priority: 3 });

    let last: { filtered: Issue[] } | null = null;
    const onFilterChange = vi.fn((filtered: Issue[]) => {
      last = { filtered };
    });

    render(wrap(<SearchFilter issues={[a, b]} onFilterChange={onFilterChange} />));

    fireEvent.change(
      screen.getByLabelText(i18n.t('projects:kanban.search.priorityLabel')),
      { target: { value: 'critical' } }
    );

    expect(last).not.toBeNull();
    expect(last!.filtered.map((i) => i.id)).toEqual([2]);
  });
});
