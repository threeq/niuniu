import { useTransportStore } from '../transportStore';

describe('transportStore activeDesktopId', () => {
  beforeEach(() => {
    useTransportStore.setState({
      activeDesktopId: null,
      activeTransportKind: 'direct',
    });
  });

  it('stores activeDesktopId', () => {
    useTransportStore.getState().setActiveDesktopId('desk-1');
    expect(useTransportStore.getState().activeDesktopId).toBe('desk-1');
  });

  it('clears activeDesktopId', () => {
    useTransportStore.getState().setActiveDesktopId('desk-1');
    useTransportStore.getState().setActiveDesktopId(null);
    expect(useTransportStore.getState().activeDesktopId).toBeNull();
  });
});

describe('transportStore resetActiveTransport', () => {
  it('resets activeDesktopId and kind to defaults', () => {
    useTransportStore.setState({
      activeDesktopId: 'desk-7',
      activeTransportKind: 'relay',
    });
    useTransportStore.getState().resetActiveTransport();
    const state = useTransportStore.getState();
    expect(state.activeDesktopId).toBeNull();
    expect(state.activeTransportKind).toBe('direct');
  });

  it('does not touch session-ephemeral fields', () => {
    // Seed live ciphers / handshake timestamps to ensure reset leaves them
    // alone — they're owned by the session lifecycle, not the user's choice.
    useTransportStore.setState({
      activeDesktopId: 'desk-7',
      activeTransportKind: 'relay',
      activeCiphers: new Map([['desk-7', {} as never]]),
      lastHandshakeAt: { 'desk-7': 1234 },
    });
    useTransportStore.getState().resetActiveTransport();
    const state = useTransportStore.getState();
    expect(state.activeCiphers.size).toBe(1);
    expect(state.lastHandshakeAt['desk-7']).toBe(1234);
  });
});
