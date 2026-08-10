import { api } from '../client';
import type {
  Workspace,
  AgentMessagesResponse,
  WorkspaceTask,
  QueueItem,
} from '../types';

// Mock the client module — we test the API call shapes, not fetch internals
jest.mock('../client', () => ({
  api: {
    get: jest.fn(),
    post: jest.fn(),
    put: jest.fn(),
    delete: jest.fn(),
  },
  apiFetch: jest.fn(),
}));

const mockApi = api as jest.Mocked<typeof api>;

describe('Workspace API calls', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  describe('workspace list', () => {
    it('fetches workspace list from /workspaces', async () => {
      const mockWorkspaces: Workspace[] = [
        {
          id: '1',
          name: 'test-ws',
          path: '/tmp/ws',
          status: 'active',
          agent_status: 'running',
          created_at: '2026-01-01',
          updated_at: '2026-01-01',
          total_cost: 0.05,
          total_turns: 10,
          issue_title: 'Fix bug',
          project_name: 'MyProject',
        },
      ];
      mockApi.get.mockResolvedValue(mockWorkspaces);

      const result = await api.get<Workspace[]>('/workspaces');
      expect(mockApi.get).toHaveBeenCalledWith('/workspaces');
      expect(result).toHaveLength(1);
      expect(result[0].agent_status).toBe('running');
    });
  });

  describe('workspace detail', () => {
    it('fetches single workspace by id', async () => {
      const mockWs: Workspace = {
        id: '42',
        name: 'detail-ws',
        path: '/tmp/ws42',
        status: 'active',
        created_at: '2026-01-01',
        updated_at: '2026-01-01',
      };
      mockApi.get.mockResolvedValue(mockWs);

      const result = await api.get<Workspace>('/workspaces/42');
      expect(mockApi.get).toHaveBeenCalledWith('/workspaces/42');
      expect(result.id).toBe('42');
    });
  });

  describe('agent control', () => {
    it('starts agent with POST /workspaces/:id/agent/start', async () => {
      mockApi.post.mockResolvedValue({ status: 'started' });

      await api.post('/workspaces/1/agent/start');
      expect(mockApi.post).toHaveBeenCalledWith('/workspaces/1/agent/start');
    });

    it('stops agent with POST /workspaces/:id/agent/stop', async () => {
      mockApi.post.mockResolvedValue({ status: 'stopped' });

      await api.post('/workspaces/1/agent/stop');
      expect(mockApi.post).toHaveBeenCalledWith('/workspaces/1/agent/stop');
    });

    it('sends message to agent', async () => {
      const message = { content: 'Fix the login bug' };
      mockApi.post.mockResolvedValue({ ok: true });

      await api.post('/workspaces/1/agent/send', message);
      expect(mockApi.post).toHaveBeenCalledWith('/workspaces/1/agent/send', message);
    });
  });

  describe('workspace messages', () => {
    it('fetches message history as an envelope', async () => {
      const mockResponse: AgentMessagesResponse = {
        messages: [
          {
            id: 'm1',
            workspaceId: 1,
            role: 'user',
            content: 'Hello',
            messageId: 'msg-1',
            eventType: 'text',
            isError: false,
            createdAt: 1735689600000,
          },
        ],
        hasMore: false,
      };
      mockApi.get.mockResolvedValue(mockResponse);

      const result = await api.get<AgentMessagesResponse>('/workspaces/1/messages');
      expect(result.messages).toHaveLength(1);
      expect(result.messages[0].role).toBe('user');
      expect(result.hasMore).toBe(false);
    });

    it('passes ?after=<id> for incremental polling', async () => {
      const mockResponse: AgentMessagesResponse = { messages: [], hasMore: false };
      mockApi.get.mockResolvedValue(mockResponse);

      await api.get<AgentMessagesResponse>('/workspaces/1/messages?after=m1');
      expect(mockApi.get).toHaveBeenCalledWith('/workspaces/1/messages?after=m1');
    });

    it('passes ?limit=100 on the cold-start poll to bound first-paint payload', async () => {
      const mockResponse: AgentMessagesResponse = { messages: [], hasMore: false };
      mockApi.get.mockResolvedValue(mockResponse);

      await api.get<AgentMessagesResponse>('/workspaces/1/messages?limit=100');
      expect(mockApi.get).toHaveBeenCalledWith('/workspaces/1/messages?limit=100');
    });
  });

  describe('workspace tasks', () => {
    it('fetches tasks for workspace', async () => {
      const mockTasks: WorkspaceTask[] = [
        {
          id: 1,
          workspace_id: 1,
          subject: 'Write tests',
          description: 'Writing unit tests',
          status: 'in_progress',
          phase: 'impl',
          created_at: '2026-01-01',
        },
      ];
      mockApi.get.mockResolvedValue(mockTasks);

      const result = await api.get<WorkspaceTask[]>('/workspaces/1/workspace-tasks');
      expect(result[0].status).toBe('in_progress');
    });
  });

  describe('workspace queue', () => {
    it('fetches queue items', async () => {
      const mockQueue: QueueItem[] = [
        {
          id: 1,
          workspace_id: 1,
          content: 'Run linter',
          position: 0,
          source: 'user',
          created_at: '2026-01-01',
        },
      ];
      mockApi.get.mockResolvedValue(mockQueue);

      const result = await api.get<QueueItem[]>('/workspaces/1/queue');
      expect(result).toHaveLength(1);
    });
  });
});
