/**
 * foreground.test.ts — verifies that backgrounding the app clears all ciphers.
 */

// Mock AppState before importing anything that uses react-native
let capturedListener: ((state: string) => void) | null = null;
const removeMock = jest.fn();

jest.mock('react-native', () => ({
  AppState: {
    addEventListener: jest.fn((_event: string, cb: (state: string) => void) => {
      capturedListener = cb;
      return { remove: removeMock };
    }),
  },
}));

import { useTransportStore } from '../../stores/transportStore';
import { registerForegroundHandler } from '../foreground';

describe('registerForegroundHandler', () => {
  beforeEach(() => {
    capturedListener = null;
    removeMock.mockClear();
    // Reset store to a known state with a fake cipher
    useTransportStore.setState({
      activeCiphers: new Map([['desk-1', {} as any]]),
      lastHandshakeAt: { 'desk-1': 12345 },
    });
  });

  it('returns a cleanup function that removes the listener', () => {
    const cleanup = registerForegroundHandler();
    cleanup();
    expect(removeMock).toHaveBeenCalledTimes(1);
  });

  it('clears all ciphers when app goes to background', () => {
    registerForegroundHandler();
    expect(capturedListener).not.toBeNull();

    capturedListener!('background');

    expect(useTransportStore.getState().activeCiphers.size).toBe(0);
  });

  it('clears all ciphers when app becomes inactive', () => {
    registerForegroundHandler();
    capturedListener!('inactive');
    expect(useTransportStore.getState().activeCiphers.size).toBe(0);
  });

  it('does NOT clear ciphers when app becomes active', () => {
    registerForegroundHandler();
    capturedListener!('active');
    // Cipher added in beforeEach should still be present
    expect(useTransportStore.getState().activeCiphers.size).toBe(1);
  });
});
