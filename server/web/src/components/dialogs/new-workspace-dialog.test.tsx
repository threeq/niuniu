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

const projects = [
  { id: 7, name: 'Proj A' },
  { id: 8, name: 'Proj B' },
] as any;

const issues = [
  { id: 42, title: 'Fix login', project_id: 7, project_name: 'Proj A', lifecycle_status: 'todo' },
] as any;

const inheritRepos = {
  repos: [
    {
      repository: { id: 10, name: 'alpha', path: '/x', default_branch: 'main' },
      branches: ['main'],
      preferred_branch: 'main',
    },
  ],
} as any;

/**
 * New flow: a workspace is ALWAYS bound to an issue. The dialog asks for
 * owner → project → issue; with no issue picked, the typed name becomes a new
 * issue's title. Repositories/scenes are inherited from the project server-side
 * (the dialog no longer offers a repo picker).
 */
describe('NewWorkspaceDialog (issue-bound flow)', () => {
  beforeEach(() => {
    vi.spyOn(apiModule.api, 'get').mockImplementation((path: string) => {
      if (path === '/projects') return Promise.resolve(projects);
      return Promise.resolve([]);
    });
    vi.spyOn(apiModule.api, 'listAvailableIssuesForWorkspace').mockResolvedValue(issues);
    vi.spyOn(apiModule.api, 'getProjectWorkspaceDefaults').mockResolvedValue(inheritRepos);
    vi.spyOn(apiModule.api, 'createWorkspace').mockResolvedValue({} as any);
  });

  afterEach(() => vi.restoreAllMocks());

  async function pickProject(projectId: string) {
    const select = document.querySelector('#projectSelect') as HTMLSelectElement;
    await waitFor(() => expect(select.options.length).toBeGreaterThan(1));
    const user = userEvent.setup();
    await user.selectOptions(select, projectId);
    return user;
  }

  it('requires a project before creating', async () => {
    render(withQuery(<NewWorkspaceDialog open={true} onOpenChange={() => {}} />));
    const submit = await screen.findByRole('button', { name: '创建' });
    expect(submit).toBeDisabled();
  });

  it('creates with project_id + name when no issue is picked (name becomes the new issue title)', async () => {
    render(withQuery(<NewWorkspaceDialog open={true} onOpenChange={() => {}} />));
    const user = await pickProject('7');

    // Inherited defaults are shown read-only.
    await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());

    await user.type(screen.getByPlaceholderText(/工作空间名称|Workspace name/i), '订单服务重构');
    await user.click(screen.getByRole('button', { name: '创建' }));

    await waitFor(() => expect(apiModule.api.createWorkspace).toHaveBeenCalledTimes(1));
    const payload = vi.mocked(apiModule.api.createWorkspace).mock.calls[0][0] as any;
    expect(payload.project_id).toBe(7);
    expect(payload.name).toBe('订单服务重构');
    // No issue picked → the server creates one; no issue_id in the payload.
    expect(payload.issue_id).toBeUndefined();
    // Repositories are inherited, never hand-picked here.
    expect(payload.repos).toEqual([]);
  });

  it('creates bound to the picked issue, and fills the name from its title', async () => {
    render(withQuery(<NewWorkspaceDialog open={true} onOpenChange={() => {}} />));
    const user = await pickProject('7');

    // Open the issue picker and select #42.
    await user.click(screen.getByRole('button', { name: /选择问题|Select.*issue/i }));
    await user.click(await screen.findByText('Fix login'));

    const nameInput = screen.getByPlaceholderText(/工作空间名称|Workspace name/i) as HTMLInputElement;
    expect(nameInput.value).toBe('Fix login');

    await user.click(screen.getByRole('button', { name: '创建' }));
    await waitFor(() => expect(apiModule.api.createWorkspace).toHaveBeenCalledTimes(1));
    const payload = vi.mocked(apiModule.api.createWorkspace).mock.calls[0][0] as any;
    expect(payload.project_id).toBe(7);
    expect(payload.issue_id).toBe(42);
  });

  it('clears a picked issue when the project changes', async () => {
    render(withQuery(<NewWorkspaceDialog open={true} onOpenChange={() => {}} />));
    const user = await pickProject('7');

    await user.click(screen.getByRole('button', { name: /选择问题|Select.*issue/i }));
    await user.click(await screen.findByText('Fix login'));
    expect(screen.getByText(/#42 Fix login/)).toBeInTheDocument();

    const select = document.querySelector('#projectSelect') as HTMLSelectElement;
    await user.selectOptions(select, '8');
    // Issue belongs to project 7 — it must not survive a project switch.
    await waitFor(() => expect(screen.queryByText(/#42 Fix login/)).not.toBeInTheDocument());
  });
});
