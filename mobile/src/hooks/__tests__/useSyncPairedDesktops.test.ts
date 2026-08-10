import { renderHook, act, waitFor } from '@testing-library/react-native';
import { useSyncPairedDesktops } from '../useSyncPairedDesktops';
import { usePairedDesktopsStore, type PairedDesktop } from '../../stores/pairedDesktopsStore';
import { useRelayAccountStore } from '../../stores/relayAccountStore';

const remoteEntry = (id: string, name: string) => ({
  desktopId: id,
  desktopName: name,
  xpub: `xpub-${id}`,
  signPub: `signpub-${id}`,
  pairedAt: '2026-04-01T12:00:00Z',
});

const local = (id: string, overrides: Partial<PairedDesktop> = {}): PairedDesktop => ({
  desktopId: id,
  desktopName: `local-${id}`,
  xpub: 'xpub-old',
  signPub: 'signpub-old',
  relayDeviceToken: 'preserve-me',
  pairedAt: '2026-03-01T00:00:00Z',
  ...overrides,
});

/**
 * Override `relayBaseUrl` and `getClient` directly on the relay store
 * via setState. The hook reads both off `useRelayAccountStore` (selector
 * for the URL, getState() for the client method), so swapping them in
 * the live state is the cleanest mock — no jest.spyOn / module mocks.
 */
function configureRelay(opts: {
  relayBaseUrl: string | null;
  client: { listPairedDesktops: jest.Mock } | null;
}) {
  useRelayAccountStore.setState({
    relayBaseUrl: opts.relayBaseUrl,
    accessToken: opts.client ? 'tok' : null,
    getClient: () => opts.client,
  } as any);
}

describe('useSyncPairedDesktops', () => {
  beforeEach(() => {
    usePairedDesktopsStore.setState({ desktops: [] });
    configureRelay({ relayBaseUrl: null, client: null });
  });

  it('is a no-op when relayBaseUrl is null', async () => {
    const { result } = renderHook(() => useSyncPairedDesktops());
    await act(async () => {
      await result.current.refresh();
    });
    expect(usePairedDesktopsStore.getState().desktops).toEqual([]);
    // initialLoading flips to false even on no-op so the UI doesn't spin forever.
    await waitFor(() => expect(result.current.initialLoading).toBe(false));
  });

  it('is a no-op when getClient() returns null (no token)', async () => {
    configureRelay({ relayBaseUrl: 'https://relay.example.com', client: null });

    const { result } = renderHook(() => useSyncPairedDesktops());
    await act(async () => {
      await result.current.refresh();
    });
    expect(usePairedDesktopsStore.getState().desktops).toEqual([]);
  });

  it('inserts unknown desktops via addDesktop with empty relayDeviceToken', async () => {
    const client = {
      listPairedDesktops: jest.fn().mockResolvedValue([
        remoteEntry('desk-new', 'Fresh Desktop'),
      ]),
    };
    configureRelay({ relayBaseUrl: 'https://relay.example.com', client });

    const { result } = renderHook(() => useSyncPairedDesktops());
    await act(async () => {
      await result.current.refresh();
    });

    const stored = usePairedDesktopsStore.getState().desktops;
    expect(stored).toHaveLength(1);
    expect(stored[0]).toMatchObject({
      desktopId: 'desk-new',
      desktopName: 'Fresh Desktop',
      relayDeviceToken: '', // listing endpoint never returns the token
    });
  });

  it('refreshes existing desktops without clobbering relayDeviceToken', async () => {
    usePairedDesktopsStore.setState({
      desktops: [local('desk-exists', { relayDeviceToken: 'PRECIOUS' })],
    });
    const client = {
      listPairedDesktops: jest.fn().mockResolvedValue([
        remoteEntry('desk-exists', 'Renamed Desktop'),
      ]),
    };
    configureRelay({ relayBaseUrl: 'https://relay.example.com', client });

    const { result } = renderHook(() => useSyncPairedDesktops());
    await act(async () => {
      await result.current.refresh();
    });

    const stored = usePairedDesktopsStore.getState().desktops;
    expect(stored).toHaveLength(1);
    expect(stored[0].desktopName).toBe('Renamed Desktop');
    expect(stored[0].xpub).toBe('xpub-desk-exists'); // updated
    expect(stored[0].relayDeviceToken).toBe('PRECIOUS'); // PRESERVED
  });

  it('swallows errors silently and still flips initialLoading to false', async () => {
    const client = {
      listPairedDesktops: jest.fn().mockRejectedValue(new Error('network down')),
    };
    configureRelay({ relayBaseUrl: 'https://relay.example.com', client });

    const { result } = renderHook(() => useSyncPairedDesktops());
    await act(async () => {
      await result.current.refresh();
    });

    expect(usePairedDesktopsStore.getState().desktops).toEqual([]);
    await waitFor(() => expect(result.current.initialLoading).toBe(false));
  });

  it('handles a mixed batch of known and unknown desktops', async () => {
    usePairedDesktopsStore.setState({
      desktops: [local('desk-known', { relayDeviceToken: 'KEEP' })],
    });
    const client = {
      listPairedDesktops: jest.fn().mockResolvedValue([
        remoteEntry('desk-known', 'Known Renamed'),
        remoteEntry('desk-unknown', 'Newly Discovered'),
      ]),
    };
    configureRelay({ relayBaseUrl: 'https://relay.example.com', client });

    const { result } = renderHook(() => useSyncPairedDesktops());
    await act(async () => {
      await result.current.refresh();
    });

    const stored = usePairedDesktopsStore.getState().desktops;
    expect(stored).toHaveLength(2);
    const known = stored.find((d) => d.desktopId === 'desk-known');
    const unknown = stored.find((d) => d.desktopId === 'desk-unknown');
    expect(known?.desktopName).toBe('Known Renamed');
    expect(known?.relayDeviceToken).toBe('KEEP'); // preserved on update path
    expect(unknown?.desktopName).toBe('Newly Discovered');
    expect(unknown?.relayDeviceToken).toBe(''); // empty on insert path
  });

  it('flips isRefreshing true during a refresh and false after it resolves', async () => {
    let resolveList!: (value: any[]) => void;
    const listPromise = new Promise<any[]>((resolve) => {
      resolveList = resolve;
    });
    const client = {
      listPairedDesktops: jest.fn().mockReturnValue(listPromise),
    };
    configureRelay({ relayBaseUrl: 'https://relay.example.com', client });

    const { result } = renderHook(() => useSyncPairedDesktops());
    // Initial mount fires refresh — wait for first refresh to resolve to false.
    resolveList([]);
    await waitFor(() => expect(result.current.isRefreshing).toBe(false));

    // Now invoke refresh manually with a deferred response.
    let resolveSecond!: (value: any[]) => void;
    client.listPairedDesktops.mockReturnValueOnce(
      new Promise<any[]>((resolve) => { resolveSecond = resolve; }),
    );

    let refreshDone: Promise<void>;
    await act(async () => {
      refreshDone = result.current.refresh();
    });
    expect(result.current.isRefreshing).toBe(true);

    await act(async () => {
      resolveSecond([]);
      await refreshDone;
    });
    expect(result.current.isRefreshing).toBe(false);
  });
});
