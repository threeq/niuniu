import '@testing-library/jest-dom';
import { cleanup } from '@testing-library/react';
import { afterEach, beforeAll, afterAll } from 'vitest';
import { server } from '@/mocks/server-node';

// jsdom does not implement ResizeObserver — polyfill with a noop so components
// that construct one in a useEffect (e.g. DispatchEdgeOverlay) don't crash.
if (typeof globalThis.ResizeObserver === 'undefined') {
  class ResizeObserverMock {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver = ResizeObserverMock as unknown as typeof ResizeObserver;
}

// jsdom does not implement EventSource — polyfill with an inert mock so stores
// that open an SSE stream in a useEffect (e.g. agent-sse-store) don't crash the
// component under test. It never emits; tests asserting SSE behaviour mock it
// themselves.
if (typeof globalThis.EventSource === 'undefined') {
  class EventSourceMock {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readyState = EventSourceMock.CONNECTING;
    onopen: ((this: EventSource, ev: Event) => unknown) | null = null;
    onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null;
    onerror: ((this: EventSource, ev: Event) => unknown) | null = null;
    url: string;
    constructor(url: string) {
      this.url = url;
    }
    addEventListener() {}
    removeEventListener() {}
    dispatchEvent() {
      return false;
    }
    close() {
      this.readyState = EventSourceMock.CLOSED;
    }
  }
  globalThis.EventSource = EventSourceMock as unknown as typeof EventSource;
}

// Start server before all tests
beforeAll(() => server.listen({ onUnhandledRequest: 'warn' }));

// Reset handlers after each test
afterEach(() => {
  server.resetHandlers();
  cleanup();
});

// Clean up after all tests
afterAll(() => server.close());
