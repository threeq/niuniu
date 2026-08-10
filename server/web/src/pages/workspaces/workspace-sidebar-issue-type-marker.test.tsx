import React from 'react';
import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { WorkspaceIssueTypeMarker } from './workspace-sidebar-issue-type-marker';

function renderMarker(ui: React.ReactElement) {
  return render(<I18nextProvider i18n={i18n}>{ui}</I18nextProvider>);
}

describe('WorkspaceIssueTypeMarker', () => {
  it('renders an accessible Epic marker for epic issues', () => {
    renderMarker(<WorkspaceIssueTypeMarker issueType="epic" />);
    // The marker exposes an accessible name (the translated label) so sighted
    // users get a tooltip and screen readers announce it.
    const marker = screen.getByRole('img');
    expect(marker).toBeInTheDocument();
    expect(marker.getAttribute('aria-label')).toBeTruthy();
    expect(marker.getAttribute('title')).toBe(marker.getAttribute('aria-label'));
  });

  it('renders an accessible sub-issue marker when the issue has a parent', () => {
    renderMarker(<WorkspaceIssueTypeMarker issueType="task" parentIssueId={42} />);
    const marker = screen.getByRole('img');
    expect(marker).toBeInTheDocument();
    expect(marker.getAttribute('aria-label')).toBeTruthy();
  });

  it('renders both markers for an epic that is itself a child', () => {
    renderMarker(<WorkspaceIssueTypeMarker issueType="epic" parentIssueId={7} />);
    expect(screen.getAllByRole('img')).toHaveLength(2);
  });

  it('renders nothing for a regular top-level task', () => {
    const { container } = renderMarker(
      <WorkspaceIssueTypeMarker issueType="task" parentIssueId={null} />,
    );
    expect(container.firstChild).toBeNull();
    expect(screen.queryByRole('img')).toBeNull();
  });

  it('renders nothing when issue_type is absent and there is no parent', () => {
    const { container } = renderMarker(<WorkspaceIssueTypeMarker issueType={undefined} />);
    expect(container.firstChild).toBeNull();
  });
});
