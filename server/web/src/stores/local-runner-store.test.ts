import { describe, it, expect, beforeEach } from 'vitest';
import {
  useLocalRunnerStore,
  type LocalRunnerConfig,
} from './local-runner-store';

const WS = '42';

function sampleConfig(): LocalRunnerConfig {
  return {
    localDir: '/Users/me/project',
    promptSnippet: 'prefer local exec',
    allowedCommands: ['npm', 'go'],
    alwaysAllowPersist: true,
  };
}

describe('local-runner-store', () => {
  beforeEach(() => {
    localStorage.clear();
    useLocalRunnerStore.setState({ byWorkspace: {} });
  });

  it('defaults to unbound with no config', () => {
    const s = useLocalRunnerStore.getState();
    s.ensureLoaded(WS);
    expect(s.getState(WS).status).toBe('unbound');
    expect(s.getState(WS).config).toBeNull();
  });

  it('saveConfig persists to localStorage and transitions to active', async () => {
    const s = useLocalRunnerStore.getState();
    await s.saveConfig(WS, sampleConfig());

    const after = useLocalRunnerStore.getState().getState(WS);
    expect(after.status).toBe('active');
    expect(after.config?.localDir).toBe('/Users/me/project');

    const raw = localStorage.getItem('niuniu.localRunner.42');
    expect(raw).toBeTruthy();
    expect(JSON.parse(raw as string).allowedCommands).toEqual(['npm', 'go']);
  });

  it('ensureLoaded hydrates active state from a persisted config', () => {
    localStorage.setItem(
      'niuniu.localRunner.42',
      JSON.stringify(sampleConfig()),
    );
    const s = useLocalRunnerStore.getState();
    s.ensureLoaded(WS);
    expect(s.getState(WS).status).toBe('active');
    expect(s.getState(WS).config?.localDir).toBe('/Users/me/project');
  });

  it('ensureLoaded ignores a corrupt persisted value', () => {
    localStorage.setItem('niuniu.localRunner.42', '{not json');
    const s = useLocalRunnerStore.getState();
    s.ensureLoaded(WS);
    expect(s.getState(WS).status).toBe('unbound');
  });

  it('clear removes persistence and returns to unbound', async () => {
    const s = useLocalRunnerStore.getState();
    await s.saveConfig(WS, sampleConfig());
    await useLocalRunnerStore.getState().clear(WS);

    const after = useLocalRunnerStore.getState().getState(WS);
    expect(after.status).toBe('unbound');
    expect(after.config).toBeNull();
    expect(localStorage.getItem('niuniu.localRunner.42')).toBeNull();
  });

  it('appendLog accumulates entries with unique ids', () => {
    const s = useLocalRunnerStore.getState();
    s.appendLog(WS, { ts: 1, level: 'command', text: 'npm run build' });
    s.appendLog(WS, { ts: 2, level: 'stdout', text: 'done' });
    const logs = useLocalRunnerStore.getState().getState(WS).logs;
    expect(logs).toHaveLength(2);
    expect(new Set(logs.map((l) => l.id)).size).toBe(2);
  });

  it('state is isolated per workspace', async () => {
    const s = useLocalRunnerStore.getState();
    await s.saveConfig('1', sampleConfig());
    s.ensureLoaded('2');
    expect(useLocalRunnerStore.getState().getState('1').status).toBe('active');
    expect(useLocalRunnerStore.getState().getState('2').status).toBe('unbound');
  });
});
