import { api } from '../client';
import type { Issue, Column } from '../types';

jest.mock('../client', () => ({
  api: {
    get: jest.fn(),
    post: jest.fn(),
    put: jest.fn(),
    delete: jest.fn(),
  },
}));

const mockApi = api as jest.Mocked<typeof api>;

describe('Issue CRUD API calls', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  describe('create issue', () => {
    it('creates issue with required fields', async () => {
      // POST /columns/:id/issues — column id lives in the path, not the body.
      // priority uses backend semantics: 0=low..3=critical.
      const newIssue = {
        title: 'New feature',
        priority: 1, // medium
      };
      const created: Issue = {
        id: 100,
        column_id: 1,
        title: 'New feature',
        priority: 1,
        position: 0,
        lifecycle_status: 'created',
        created_at: '2026-01-01',
        updated_at: '2026-01-01',
      };
      mockApi.post.mockResolvedValue(created);

      const result = await api.post<Issue>('/columns/1/issues', newIssue);
      expect(mockApi.post).toHaveBeenCalledWith('/columns/1/issues', newIssue);
      expect(result.id).toBe(100);
      expect(result.title).toBe('New feature');
    });

    it('creates issue with all optional fields', async () => {
      const newIssue = {
        title: 'Full issue',
        priority: 3, // critical
        description: 'Detailed description',
        assignee: 'developer1',
        labels: 'bug,urgent',
        start_date: '2026-04-01',
        due_date: '2026-04-15',
        estimate_type: 'hours',
        estimate: 8,
      };
      mockApi.post.mockResolvedValue({ ...newIssue, id: 101, column_id: 1, position: 0, lifecycle_status: 'created', created_at: '2026-01-01', updated_at: '2026-01-01' });

      const result = await api.post<Issue>('/columns/1/issues', newIssue);
      expect(result.description).toBe('Detailed description');
      expect(result.assignee).toBe('developer1');
    });
  });

  describe('update issue', () => {
    it('updates issue title', async () => {
      mockApi.put.mockResolvedValue({ id: 1, title: 'Updated title' });

      await api.put('/issues/1', { title: 'Updated title' });
      expect(mockApi.put).toHaveBeenCalledWith('/issues/1', { title: 'Updated title' });
    });

    it('updates issue priority', async () => {
      mockApi.put.mockResolvedValue({ id: 1, priority: 1 });

      await api.put('/issues/1', { priority: 1 });
      expect(mockApi.put).toHaveBeenCalledWith('/issues/1', { priority: 1 });
    });

    it('moves issue to different column', async () => {
      mockApi.put.mockResolvedValue({ id: 1, column_id: 5 });

      await api.put('/issues/1', { column_id: 5 });
      expect(mockApi.put).toHaveBeenCalledWith('/issues/1', { column_id: 5 });
    });
  });

  describe('delete issue', () => {
    it('deletes issue by id', async () => {
      mockApi.delete.mockResolvedValue(undefined);

      await api.delete('/issues/1');
      expect(mockApi.delete).toHaveBeenCalledWith('/issues/1');
    });
  });

  describe('fetch columns', () => {
    it('fetches columns for project', async () => {
      const mockColumns: Column[] = [
        { id: 1, project_id: 1, name: 'Backlog', position: 0, lifecycle_mapping: 'created' },
        { id: 2, project_id: 1, name: 'In Progress', position: 1, lifecycle_mapping: 'implement' },
        { id: 3, project_id: 1, name: 'Done', position: 2, lifecycle_mapping: 'completed' },
      ];
      mockApi.get.mockResolvedValue(mockColumns);

      const result = await api.get<Column[]>('/projects/1/columns');
      expect(result).toHaveLength(3);
      expect(result[0].name).toBe('Backlog');
    });
  });

  describe('fetch issues', () => {
    it('fetches all issues for project', async () => {
      const mockIssues: Issue[] = [
        {
          id: 1,
          column_id: 1,
          title: 'Issue 1',
          priority: 2,
          position: 0,
          lifecycle_status: 'created',
          checklist_done: 2,
          checklist_total: 5,
          created_at: '2026-01-01',
          updated_at: '2026-01-01',
        },
      ];
      mockApi.get.mockResolvedValue(mockIssues);

      const result = await api.get<Issue[]>('/projects/1/issues');
      expect(result[0].checklist_done).toBe(2);
      expect(result[0].checklist_total).toBe(5);
    });
  });
});
