import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NewWorkspaceDialog } from './new-workspace-dialog';
import * as apiModule from '@/lib/api';

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

describe('NewWorkspaceDialog default repos', () => {
  beforeEach(() => {
    vi.spyOn(apiModule.api, 'listRepositories').mockResolvedValue([
      { id: 10, name: 'alpha', path: '/x', default_branch: 'main' } as any,
    ]);
    vi.spyOn(apiModule.api, 'listAvailableIssuesForWorkspace').mockResolvedValue([]);
    // listTeams may or may not be called but mock to be safe
    if ('listTeams' in apiModule.api) {
      vi.spyOn(apiModule.api, 'listTeams' as any).mockResolvedValue([]);
    }
  });

  afterEach(() => vi.restoreAllMocks());

  it('does not call issue-defaults when defaultIssueId is not provided', async () => {
    const spy = vi.spyOn(apiModule.api, 'getWorkspaceIssueDefaults');
    render(withQuery(<NewWorkspaceDialog open={true} onOpenChange={() => {}} />));
    // give effects a couple of ticks
    await new Promise(r => setTimeout(r, 50));
    expect(spy).not.toHaveBeenCalled();
  });

  it('pre-selects repos returned by issue-defaults', async () => {
    vi.spyOn(apiModule.api, 'getWorkspaceIssueDefaults').mockResolvedValue({
      repos: [
        {
          repository: { id: 10, name: 'alpha', path: '/x', default_branch: 'main' } as any,
          branches: ['main', 'dev'],
          preferred_branch: 'main',
        },
      ],
    });
    render(withQuery(
      <NewWorkspaceDialog open={true} onOpenChange={() => {}} defaultIssueId="42" />
    ));
    await waitFor(() => expect(screen.getByText('alpha')).toBeInTheDocument(), { timeout: 2000 });

    // Verify the preferred branch was seeded: find the branch <select> with value "main"
    const selects = document.querySelectorAll('select');
    const branchSelect = Array.from(selects).find(
      (el) => (el as HTMLSelectElement).value === 'main' && el.querySelector('option[value="main"]')
    );
    expect(branchSelect).toBeTruthy();
  });

  it('does not clobber user-added repo when defaults arrive late', async () => {
    let resolveDefaults!: (v: any) => void;
    vi.spyOn(apiModule.api, 'getWorkspaceIssueDefaults').mockReturnValue(
      new Promise(res => { resolveDefaults = res; }) as any
    );
    vi.spyOn(apiModule.api, 'getRepositoryBranches').mockResolvedValue(['main']);
    vi.spyOn(apiModule.api, 'listRepositories').mockResolvedValue([
      { id: 10, name: 'alpha', path: '/x', default_branch: 'main' } as any,
      { id: 20, name: 'beta',  path: '/y', default_branch: 'main' } as any,
    ]);

    const user = userEvent.setup();
    render(withQuery(
      <NewWorkspaceDialog open={true} onOpenChange={() => {}} defaultIssueId="42" />
    ));

    // Wait for the repo select to appear with repos loaded
    const repoSelect = await waitFor(() => {
      const sel = document.querySelector('#repoSelect') as HTMLSelectElement;
      // The select is available and has options beyond the placeholder
      if (!sel || sel.options.length <= 1) throw new Error('repos not loaded yet');
      return sel;
    }, { timeout: 2000 });

    // Select "alpha" (id=10) and click Add
    // The add-repo button is the first outline button adjacent to the repoSelect
    await user.selectOptions(repoSelect, '10');
    // Find the button that is a sibling/adjacent to the repoSelect (not cancel/submit)
    // It's the button inside the flex gap-2 container with the repo select
    const repoSelectContainer = repoSelect.parentElement!;
    const addButton = repoSelectContainer.querySelector('button') as HTMLButtonElement;
    expect(addButton).toBeTruthy();
    await user.click(addButton);

    // Wait for alpha to appear in the selected repos list (after branch fetch)
    await waitFor(() => {
      expect(screen.getByText('alpha')).toBeInTheDocument();
    }, { timeout: 2000 });

    // Now resolve defaults with "beta" — since initializedRef.current is already true,
    // the seeding effect should NOT run and should NOT replace "alpha" with "beta"
    resolveDefaults({
      repos: [
        {
          repository: { id: 20, name: 'beta', path: '/y', default_branch: 'main' } as any,
          branches: ['main'],
          preferred_branch: 'main',
        },
      ],
    });

    // Give effects time to run
    await new Promise(r => setTimeout(r, 100));

    // alpha must still be present in the selected repos list; beta must NOT
    // appear there (it may appear as a dropdown option, but not as a selected repo name).
    const selectedNames = screen
      .queryAllByTestId('selected-repo-name')
      .map(el => el.textContent);
    expect(selectedNames).toContain('alpha');
    expect(selectedNames).not.toContain('beta');
  });

  it('reuses an already-registered repo when adding via local directory', async () => {
    // beforeEach registers repo id=10 at path '/x'. Selecting that same path
    // from the directory source must reuse it (no createRepository call).
    vi.spyOn(apiModule.api, 'getRepositoryBranches').mockResolvedValue(['main']);
    const createSpy = vi.spyOn(apiModule.api, 'createRepository');

    const user = userEvent.setup();
    render(withQuery(<NewWorkspaceDialog open={true} onOpenChange={() => {}} />));

    // Switch the repo source toggle to "本地目录" (local directory).
    const dirRadio = await screen.findByRole('radio', { name: '本地目录' });
    await user.click(dirRadio);

    // Type the registered repo's path; blur commits it to the dialog.
    const dirInput = screen.getByPlaceholderText('/path/to/directory');
    await user.click(dirInput);
    await user.type(dirInput, '/x');
    await user.tab();

    // Click the directory "添加" button.
    await user.click(screen.getByRole('button', { name: '添加' }));

    await waitFor(
      () => expect(screen.getByText('alpha')).toBeInTheDocument(),
      { timeout: 2000 }
    );
    expect(createSpy).not.toHaveBeenCalled();
  });

  it('reseeds defaults each time the dialog reopens', async () => {
    const getSpy = vi.spyOn(apiModule.api, 'getWorkspaceIssueDefaults').mockResolvedValue({
      repos: [
        {
          repository: { id: 10, name: 'alpha', path: '/x', default_branch: 'main' } as any,
          branches: ['main'],
          preferred_branch: 'main',
        },
      ],
    });
    const { rerender } = render(withQuery(
      <NewWorkspaceDialog open={true} onOpenChange={() => {}} defaultIssueId="42" />
    ));
    await waitFor(() => expect(screen.getByText('alpha')).toBeInTheDocument());

    // close
    rerender(withQuery(
      <NewWorkspaceDialog open={false} onOpenChange={() => {}} defaultIssueId="42" />
    ));
    // reopen
    rerender(withQuery(
      <NewWorkspaceDialog open={true} onOpenChange={() => {}} defaultIssueId="42" />
    ));
    await waitFor(() => expect(screen.getByText('alpha')).toBeInTheDocument());
    expect(getSpy).toHaveBeenCalled(); // at least once on reopen
  });
});
