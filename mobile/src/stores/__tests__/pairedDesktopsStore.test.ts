import { usePairedDesktopsStore, type PairedDesktop } from '../pairedDesktopsStore';
import { useTransportStore } from '../transportStore';

const desktop = (id: string): PairedDesktop => ({
  desktopId: id,
  desktopName: `desktop-${id}`,
  xpub: 'xpub',
  signPub: 'signpub',
  relayDeviceToken: 'token',
  pairedAt: new Date().toISOString(),
});

describe('pairedDesktopsStore.removeDesktop cleanup', () => {
  beforeEach(() => {
    usePairedDesktopsStore.setState({ desktops: [] });
    useTransportStore.setState({
      activeDesktopId: null,
      activeTransportKind: 'direct',
      activeCiphers: new Map(),
      lanHandles: new Map(),
      lastHandshakeAt: {},
    });
  });

  it('clears activeDesktopId when the active desktop is unpaired', () => {
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-A'));
    useTransportStore.getState().setActiveDesktopId('desk-A');
    useTransportStore.getState().setTransportKind('relay');

    usePairedDesktopsStore.getState().removeDesktop('desk-A');

    const transport = useTransportStore.getState();
    expect(transport.activeDesktopId).toBeNull();
    expect(transport.activeTransportKind).toBe('direct');
  });

  it('preserves activeDesktopId when a different desktop is unpaired', () => {
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-A'));
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-B'));
    useTransportStore.getState().setActiveDesktopId('desk-A');
    useTransportStore.getState().setTransportKind('relay');

    usePairedDesktopsStore.getState().removeDesktop('desk-B');

    const transport = useTransportStore.getState();
    expect(transport.activeDesktopId).toBe('desk-A');
    expect(transport.activeTransportKind).toBe('relay');
  });

  it('tears down the cipher for the removed desktop', () => {
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-A'));
    useTransportStore.setState({
      activeCiphers: new Map([['desk-A', {} as never]]),
    });

    usePairedDesktopsStore.getState().removeDesktop('desk-A');

    expect(useTransportStore.getState().activeCiphers.has('desk-A')).toBe(false);
  });
});
