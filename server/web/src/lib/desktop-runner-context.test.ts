import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import {
  getDesktopRunnerContext,
  useDesktopRunnerAvailable,
  isDesktopShell,
  harvestRunnerConfigToDesktop,
  unbindRunnerFromDesktop,
} from './desktop-runner-context';

// Detection is driven entirely by the desktop raw-message bridge — WebView2
// (window.chrome.webview) on Windows or WKWebView
// (window.webkit.messageHandlers.external) on macOS — plus the host. There is no
// injected __NIUNIU_DESKTOP__ global anymore.
const realLocation = window.location;

function setHost(host: string, hostname: string) {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...realLocation, host, hostname, origin: `https://${host}` },
  });
}

function restoreHost() {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: realLocation,
  });
}

function installBridge(posted?: unknown[]) {
  (window as unknown as { chrome?: unknown }).chrome = {
    webview: {
      postMessage: (m: string) => posted?.push(JSON.parse(m)),
    },
  };
}

function removeBridge() {
  delete (window as unknown as { chrome?: unknown }).chrome;
  delete (window as unknown as { webkit?: unknown }).webkit;
}

// macOS WKWebView exposes the Wails raw-message bridge as a
// window.webkit.messageHandlers.<name> handler (Wails names it "external"),
// whose postMessage receives a string body — the same JSON string Windows sends
// via window.chrome.webview.postMessage.
function installWebkitBridge(posted?: unknown[]) {
  (window as unknown as { webkit?: unknown }).webkit = {
    messageHandlers: {
      external: {
        postMessage: (m: string) => posted?.push(JSON.parse(m)),
      },
    },
  };
}

describe('getDesktopRunnerContext', () => {
  beforeEach(() => {
    localStorage.clear();
    removeBridge();
  });

  afterEach(() => {
    localStorage.clear();
    removeBridge();
    restoreHost();
  });

  it('hides the entry in a plain browser (no WebView2 bridge)', () => {
    setHost('app.example.com', 'app.example.com');
    expect(getDesktopRunnerContext()).toEqual({ available: false, connKind: null });
    expect(isDesktopShell()).toBe(false);
  });

  it('shows the entry on desktop + remote connection (bridge + remote host)', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    installBridge();
    expect(getDesktopRunnerContext()).toEqual({ available: true, connKind: 'remote' });
    expect(isDesktopShell()).toBe(true);
  });

  it('hides the entry on desktop + local #0 (bridge + localhost)', () => {
    setHost('localhost:3000', 'localhost');
    installBridge();
    expect(getDesktopRunnerContext()).toEqual({ available: false, connKind: 'local' });
    // Still the desktop shell — just the bundled local server.
    expect(isDesktopShell()).toBe(true);
  });

  it('has no localStorage force bypass (the bridge is required)', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    localStorage.setItem('niuniu.desktopRunner.force', '1');
    expect(getDesktopRunnerContext()).toEqual({ available: false, connKind: null });
  });
});

describe('useDesktopRunnerAvailable (reactive)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
    removeBridge();
  });

  afterEach(() => {
    vi.useRealTimers();
    localStorage.clear();
    removeBridge();
    restoreHost();
  });

  it('is true immediately when the bridge is present at mount', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    installBridge();
    const { result } = renderHook(() => useDesktopRunnerAvailable(250, 15000));
    expect(result.current).toBe(true);
  });

  it('self-heals if the bridge only becomes readable after first render', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    const { result } = renderHook(() => useDesktopRunnerAvailable(250, 15000));
    expect(result.current).toBe(false); // no bridge yet at first paint

    installBridge();
    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(result.current).toBe(true); // reactive read picked it up
  });

  it('stays false and stops polling in a plain browser after the cap', () => {
    setHost('app.example.com', 'app.example.com');
    const { result } = renderHook(() => useDesktopRunnerAvailable(250, 1000));
    act(() => {
      vi.advanceTimersByTime(2000);
    });
    expect(result.current).toBe(false);
  });
});

describe('WebView2 bridge self-harvest (SPA-driven)', () => {
  let posted: unknown[];

  beforeEach(() => {
    posted = [];
    localStorage.clear();
    installBridge(posted);
  });

  afterEach(() => {
    localStorage.clear();
    removeBridge();
    restoreHost();
  });

  it('harvest posts token + config with connKey = location.host', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    localStorage.setItem(
      'niuniu-auth-storage',
      JSON.stringify({ state: { accessToken: 'jwt-xyz' } }),
    );
    harvestRunnerConfigToDesktop('52', 'E:\\tmp\\proj');
    expect(posted).toContainEqual({
      type: 'niuniu-runner-token',
      token: 'jwt-xyz',
      connKey: 'self.niu6ai.com',
      origin: 'https://self.niu6ai.com',
    });
    expect(posted).toContainEqual({
      type: 'niuniu-runner-config',
      workspaceId: '52',
      localDir: 'E:\\tmp\\proj',
      connKey: 'self.niu6ai.com',
      origin: 'https://self.niu6ai.com',
    });
  });

  it('harvest is a no-op on localhost (local #0)', () => {
    setHost('localhost:3000', 'localhost');
    localStorage.setItem(
      'niuniu-auth-storage',
      JSON.stringify({ state: { accessToken: 'jwt' } }),
    );
    harvestRunnerConfigToDesktop('52', 'E:\\tmp\\proj');
    expect(posted).toHaveLength(0);
  });

  it('unbind posts an unbind message with the connKey', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    unbindRunnerFromDesktop('52');
    expect(posted).toContainEqual({
      type: 'niuniu-runner-unbind',
      workspaceId: '52',
      connKey: 'self.niu6ai.com',
    });
  });
});

// macOS parity: the desktop shell on macOS exposes the bridge as
// window.webkit.messageHandlers.external instead of window.chrome.webview.
// Everything downstream (visibility + harvest) must behave identically — this is
// the regression guard for "Mac 版本链接团队版本后，底部没有出现 runner 配置".
describe('macOS WKWebView bridge (window.webkit.messageHandlers.external)', () => {
  beforeEach(() => {
    localStorage.clear();
    removeBridge();
  });

  afterEach(() => {
    localStorage.clear();
    removeBridge();
    restoreHost();
  });

  it('shows the entry on desktop + remote connection (webkit bridge + remote host)', () => {
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    installWebkitBridge();
    expect(getDesktopRunnerContext()).toEqual({ available: true, connKind: 'remote' });
    expect(isDesktopShell()).toBe(true);
  });

  it('hides the entry on desktop + local #0 (webkit bridge + localhost)', () => {
    setHost('localhost:3000', 'localhost');
    installWebkitBridge();
    expect(getDesktopRunnerContext()).toEqual({ available: false, connKind: 'local' });
    expect(isDesktopShell()).toBe(true);
  });

  it('harvest posts token + config through the webkit bridge', () => {
    const posted: unknown[] = [];
    setHost('self.niu6ai.com', 'self.niu6ai.com');
    installWebkitBridge(posted);
    localStorage.setItem(
      'niuniu-auth-storage',
      JSON.stringify({ state: { accessToken: 'jwt-mac' } }),
    );
    harvestRunnerConfigToDesktop('52', '/Users/me/proj');
    expect(posted).toContainEqual({
      type: 'niuniu-runner-token',
      token: 'jwt-mac',
      connKey: 'self.niu6ai.com',
      origin: 'https://self.niu6ai.com',
    });
    expect(posted).toContainEqual({
      type: 'niuniu-runner-config',
      workspaceId: '52',
      localDir: '/Users/me/proj',
      connKey: 'self.niu6ai.com',
      origin: 'https://self.niu6ai.com',
    });
  });
});
