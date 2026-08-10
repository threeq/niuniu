import { api } from '../client';
import type { Repository, BranchInfo, CommitEntry, TreeNode, WorktreeEntry } from '../types';

jest.mock('../client', () => ({
  api: {
    get: jest.fn(),
    post: jest.fn(),
    put: jest.fn(),
    delete: jest.fn(),
  },
}));

const mockApi = api as jest.Mocked<typeof api>;

describe('Repository API calls', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  describe('repository list', () => {
    it('fetches all repositories', async () => {
      const mockRepos: Repository[] = [
        {
          id: 1,
          name: 'niuniu',
          path: '/home/user/niuniu',
          git_remote: 'git@github.com:niuniu-dev/niuniu.git',
          default_branch: 'main',
          created_at: '2026-01-01',
          updated_at: '2026-01-01',
        },
      ];
      mockApi.get.mockResolvedValue(mockRepos);

      const result = await api.get<Repository[]>('/repositories');
      expect(result).toHaveLength(1);
      expect(result[0].name).toBe('niuniu');
    });
  });

  describe('repository detail', () => {
    it('fetches branches', async () => {
      const mockBranches: BranchInfo = {
        branches: ['main', 'feature/chat', 'fix/login'],
        current_branch: 'main',
      };
      mockApi.get.mockResolvedValue(mockBranches);

      const result = await api.get<BranchInfo>('/repositories/1/branches');
      expect(result.branches).toHaveLength(3);
      expect(result.current_branch).toBe('main');
    });

    it('fetches commit history', async () => {
      const mockCommits: CommitEntry[] = [
        { hash: 'abc123', author: 'dev', date: '2026-01-01', message: 'feat: add chat' },
        { hash: 'def456', author: 'dev', date: '2026-01-01', message: 'fix: login bug' },
      ];
      mockApi.get.mockResolvedValue(mockCommits);

      const result = await api.get<CommitEntry[]>('/repositories/1/commits');
      expect(result).toHaveLength(2);
      expect(result[0].message).toContain('feat');
    });

    it('fetches file tree', async () => {
      const mockTree: TreeNode[] = [
        {
          name: 'src',
          path: 'src',
          is_dir: true,
          children: [
            { name: 'index.ts', path: 'src/index.ts', is_dir: false },
          ],
        },
        { name: 'package.json', path: 'package.json', is_dir: false },
      ];
      mockApi.get.mockResolvedValue(mockTree);

      const result = await api.get<TreeNode[]>('/repositories/1/tree');
      expect(result).toHaveLength(2);
      expect(result[0].is_dir).toBe(true);
      expect(result[0].children).toHaveLength(1);
    });

    it('fetches worktrees', async () => {
      const mockWorktrees: WorktreeEntry[] = [
        {
          id: 1,
          path: '/tmp/ws-1/niuniu',
          branch: 'feature/chat',
          is_current: false,
          has_changes: true,
          workspace_id: '1',
        },
      ];
      mockApi.get.mockResolvedValue(mockWorktrees);

      const result = await api.get<WorktreeEntry[]>('/repositories/1/worktrees');
      expect(result[0].has_changes).toBe(true);
      expect(result[0].workspace_id).toBe('1');
    });
  });
});
