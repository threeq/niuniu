import { http, HttpResponse } from 'msw';

// Types based on the backend API
interface Project {
  id: number;
  name: string;
  description: string;
  created_at: string;
  updated_at: string;
}

interface Workspace {
  id: number;
  project_id: number;
  name: string;
  path: string;
  created_at: string;
  updated_at: string;
}

interface Repository {
  id: number;
  name: string;
  url: string;
  created_at: string;
  updated_at: string;
}

interface WorkspaceRepository {
  id: number;
  workspace_id: number;
  repository_id: number;
  branch: string;
  created_at: string;
  updated_at: string;
}

// Mock data
const mockProjects: Project[] = [
  {
    id: 1,
    name: 'Test Project 1',
    description: 'A test project for development',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 2,
    name: 'Test Project 2',
    description: 'Another test project',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
];

const mockWorkspaces: Workspace[] = [
  {
    id: 1,
    project_id: 1,
    name: 'workspace-1',
    path: '/tmp/niuniu/workspaces/workspace-1',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
];

const mockRepositories: Repository[] = [
  {
    id: 1,
    name: 'test-repo',
    url: 'https://github.com/test/repo.git',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
];

const mockWorkspaceRepositories: WorkspaceRepository[] = [
  {
    id: 1,
    workspace_id: 1,
    repository_id: 1,
    branch: 'main',
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
];

// MSW handlers
export const handlers = [
  // Projects API
  http.get('/api/projects', () => {
    return HttpResponse.json({
      success: true,
      data: mockProjects,
    });
  }),

  http.post('/api/projects', async ({ request }) => {
    const body = (await request.json()) as { name: string; description?: string };
    const newProject: Project = {
      id: mockProjects.length + 1,
      name: body.name,
      description: body.description || '',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockProjects.push(newProject);
    return HttpResponse.json({
      success: true,
      data: newProject,
    });
  }),

  http.get('/api/projects/:id', ({ params }) => {
    const project = mockProjects.find((p) => p.id === Number(params.id));
    if (!project) {
      return HttpResponse.json(
        { success: false, error: 'Project not found' },
        { status: 404 }
      );
    }
    return HttpResponse.json({
      success: true,
      data: project,
    });
  }),

  // Workspaces API
  http.get('/api/workspaces', () => {
    return HttpResponse.json({
      success: true,
      data: mockWorkspaces,
    });
  }),

  http.post('/api/workspaces', async ({ request }) => {
    const body = (await request.json()) as { project_id: number; name: string; path: string };
    const newWorkspace: Workspace = {
      id: mockWorkspaces.length + 1,
      project_id: body.project_id,
      name: body.name,
      path: body.path,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockWorkspaces.push(newWorkspace);
    return HttpResponse.json({
      success: true,
      data: newWorkspace,
    });
  }),

  http.get('/api/workspaces/:id', ({ params }) => {
    const workspace = mockWorkspaces.find((w) => w.id === Number(params.id));
    if (!workspace) {
      return HttpResponse.json(
        { success: false, error: 'Workspace not found' },
        { status: 404 }
      );
    }
    return HttpResponse.json({
      success: true,
      data: workspace,
    });
  }),

  // Repositories API
  http.get('/api/repositories', () => {
    return HttpResponse.json({
      success: true,
      data: mockRepositories,
    });
  }),

  http.post('/api/repositories', async ({ request }) => {
    const body = (await request.json()) as { name: string; url: string };
    const newRepo: Repository = {
      id: mockRepositories.length + 1,
      name: body.name,
      url: body.url,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockRepositories.push(newRepo);
    return HttpResponse.json({
      success: true,
      data: newRepo,
    });
  }),

  // Workspace Repositories API
  http.get('/api/workspaces/:workspaceId/repositories', ({ params }) => {
    const workspaceRepos = mockWorkspaceRepositories.filter(
      (wr) => wr.workspace_id === Number(params.workspaceId)
    );
    return HttpResponse.json({
      success: true,
      data: workspaceRepos,
    });
  }),

  http.post('/api/workspaces/:workspaceId/repositories', async ({ request, params }) => {
    const body = (await request.json()) as { repository_id: number; branch?: string };
    const newWorkspaceRepo: WorkspaceRepository = {
      id: mockWorkspaceRepositories.length + 1,
      workspace_id: Number(params.workspaceId),
      repository_id: body.repository_id,
      branch: body.branch || 'main',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockWorkspaceRepositories.push(newWorkspaceRepo);
    return HttpResponse.json({
      success: true,
      data: newWorkspaceRepo,
    });
  }),
];
