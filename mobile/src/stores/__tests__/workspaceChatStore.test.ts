import { useWorkspaceChatStore, type ChatMessage } from '../workspaceChatStore';

// Reset store between tests
beforeEach(() => {
  useWorkspaceChatStore.getState().clearMessages('ws-1');
});

describe('workspaceChatStore', () => {
  const wsId = 'ws-1';

  describe('initial state', () => {
    it('returns empty messages for unknown workspace', () => {
      const messages = useWorkspaceChatStore.getState().getMessages('unknown');
      expect(messages).toEqual([]);
    });

    it('returns idle agent status by default', () => {
      const status = useWorkspaceChatStore.getState().getAgentStatus('unknown');
      expect(status).toBe('idle');
    });

    it('isTyping is false by default', () => {
      const typing = useWorkspaceChatStore.getState().isTyping(wsId);
      expect(typing).toBe(false);
    });
  });

  describe('appendText', () => {
    it('creates a new assistant message when none exists', () => {
      useWorkspaceChatStore.getState().appendText(wsId, 'Hello');
      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages).toHaveLength(1);
      expect(messages[0].role).toBe('assistant');
      expect(messages[0].content).toBe('Hello');
    });

    it('appends to existing assistant message incrementally', () => {
      const store = useWorkspaceChatStore.getState();
      store.appendText(wsId, 'Hello ');
      store.appendText(wsId, 'world');
      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages).toHaveLength(1);
      expect(messages[0].content).toBe('Hello world');
    });

    it('sets typing indicator to true', () => {
      useWorkspaceChatStore.getState().appendText(wsId, 'typing...');
      expect(useWorkspaceChatStore.getState().isTyping(wsId)).toBe(true);
    });
  });

  describe('addToolUse', () => {
    it('inserts a tool_use message', () => {
      useWorkspaceChatStore.getState().addToolUse(wsId, {
        toolName: 'Read',
        toolInput: '{"path": "/foo/bar.ts"}',
        messageId: 'tool-1',
      });
      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages).toHaveLength(1);
      expect(messages[0].type).toBe('tool_use');
      expect(messages[0].toolName).toBe('Read');
    });
  });

  describe('updateToolResult', () => {
    it('updates a tool_use message with result status', () => {
      const store = useWorkspaceChatStore.getState();
      store.addToolUse(wsId, {
        toolName: 'Read',
        toolInput: '{"path": "x.ts"}',
        messageId: 'tool-1',
      });
      store.updateToolResult(wsId, 'tool-1', 'success');
      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages[0].toolStatus).toBe('success');
    });

    it('does nothing for unknown messageId', () => {
      const store = useWorkspaceChatStore.getState();
      store.addToolUse(wsId, {
        toolName: 'Read',
        toolInput: '',
        messageId: 'tool-1',
      });
      store.updateToolResult(wsId, 'unknown', 'error');
      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages[0].toolStatus).toBeUndefined();
    });
  });

  describe('addUserMessage', () => {
    it('inserts a user message', () => {
      useWorkspaceChatStore.getState().addUserMessage(wsId, 'Fix the bug');
      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages).toHaveLength(1);
      expect(messages[0].role).toBe('user');
      expect(messages[0].content).toBe('Fix the bug');
    });
  });

  describe('markDone', () => {
    it('sets typing to false and agent status to idle', () => {
      const store = useWorkspaceChatStore.getState();
      store.appendText(wsId, 'working...');
      store.setAgentStatus(wsId, 'running');
      store.markDone(wsId);
      expect(useWorkspaceChatStore.getState().isTyping(wsId)).toBe(false);
      expect(useWorkspaceChatStore.getState().getAgentStatus(wsId)).toBe('idle');
    });
  });

  describe('setAgentStatus', () => {
    it('updates agent status', () => {
      useWorkspaceChatStore.getState().setAgentStatus(wsId, 'running');
      expect(useWorkspaceChatStore.getState().getAgentStatus(wsId)).toBe('running');
    });
  });

  describe('setHistoryMessages', () => {
    it('prepends history messages before existing ones', () => {
      const store = useWorkspaceChatStore.getState();
      store.addUserMessage(wsId, 'latest');

      const history: ChatMessage[] = [
        {
          id: 'h1',
          role: 'user',
          type: 'text',
          content: 'old message',
          timestamp: '2026-01-01T00:00:00Z',
        },
        {
          id: 'h2',
          role: 'assistant',
          type: 'text',
          content: 'old reply',
          timestamp: '2026-01-01T00:01:00Z',
        },
      ];
      store.setHistoryMessages(wsId, history);

      const messages = useWorkspaceChatStore.getState().getMessages(wsId);
      expect(messages).toHaveLength(3);
      expect(messages[0].content).toBe('old message');
      expect(messages[1].content).toBe('old reply');
      expect(messages[2].content).toBe('latest');
    });
  });

  describe('clearMessages', () => {
    it('removes all messages for a workspace', () => {
      const store = useWorkspaceChatStore.getState();
      store.addUserMessage(wsId, 'msg1');
      store.addUserMessage(wsId, 'msg2');
      store.clearMessages(wsId);
      expect(useWorkspaceChatStore.getState().getMessages(wsId)).toEqual([]);
    });

    it('does not affect other workspaces', () => {
      const store = useWorkspaceChatStore.getState();
      store.addUserMessage('ws-1', 'msg for ws1');
      store.addUserMessage('ws-2', 'msg for ws2');
      store.clearMessages('ws-1');
      expect(useWorkspaceChatStore.getState().getMessages('ws-2')).toHaveLength(1);
    });
  });
});
