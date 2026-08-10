import { usePairedDesktopsStore, type PairedDesktop } from '../pairedDesktopsStore';
import { useRelayAccountStore } from '../relayAccountStore';
import { useTransportStore } from '../transportStore';

// Mock RelayClient so the `accessToken != null` branch of logout can be
// exercised without actually hitting the network. The real client's
// `logout()` internally calls clearTokens via its injected tokenStore
// adapter; the mock emulates that by receiving the tokenStore instance
// in its ctor and invoking clearTokens() in its finally-equivalent.
jest.mock('../../api/relay/client', () => {
  const actual = jest.requireActual('../../api/relay/client');
  type Store = import('../../api/relay/client').RelayTokenStore;
  return {
    ...actual,
    RelayClient: jest.fn().mockImplementation((baseUrl: string, store: Store) => ({
      baseUrl,
      logout: jest.fn(async () => {
        // Emulate the real client's finally { clearTokens() }
        store.clearTokens();
      }),
    })),
  };
});

const desktop = (id: string): PairedDesktop => ({
  desktopId: id,
  desktopName: `desktop-${id}`,
  xpub: 'xpub',
  signPub: 'signpub',
  relayDeviceToken: 'token',
  pairedAt: new Date().toISOString(),
});

describe('relayAccountStore.logout cross-store cleanup', () => {
  beforeEach(() => {
    useRelayAccountStore.setState({
      email: null,
      accessToken: null,
      refreshToken: null,
      relayBaseUrl: null,
    });
    usePairedDesktopsStore.setState({ desktops: [] });
    useTransportStore.setState({
      activeDesktopId: null,
      activeTransportKind: 'direct',
      activeCiphers: new Map(),
      lanHandles: new Map(),
      lastHandshakeAt: {},
    });
  });

  it('clears paired desktops on logout (account-scoped data hygiene)', async () => {
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-A'));
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-B'));
    useRelayAccountStore.setState({
      email: 'a@example.com',
      accessToken: null, // skip network call via client.logout()
      refreshToken: 'ref',
      relayBaseUrl: 'https://relay.example.com',
    });

    await useRelayAccountStore.getState().logout();

    expect(usePairedDesktopsStore.getState().desktops).toHaveLength(0);
  });

  it('resets the persisted transport choice on logout', async () => {
    useTransportStore.getState().setActiveDesktopId('desk-A');
    useTransportStore.getState().setTransportKind('relay');
    useRelayAccountStore.setState({
      email: 'a@example.com',
      accessToken: null,
      refreshToken: null,
      relayBaseUrl: null,
    });

    await useRelayAccountStore.getState().logout();

    const transport = useTransportStore.getState();
    expect(transport.activeDesktopId).toBeNull();
    expect(transport.activeTransportKind).toBe('direct');
  });

  it('clears account state even when no network logout call is made', async () => {
    useRelayAccountStore.setState({
      email: 'a@example.com',
      accessToken: null, // no accessToken → no network call path
      refreshToken: 'ref',
      relayBaseUrl: 'https://relay.example.com',
    });

    await useRelayAccountStore.getState().logout();

    const state = useRelayAccountStore.getState();
    expect(state.email).toBeNull();
    expect(state.accessToken).toBeNull();
    expect(state.refreshToken).toBeNull();
  });

  it('tears down live ciphers and LAN handles on logout (account-scoped cleanup)', async () => {
    // Regression guard: logout must drop live transport state even when
    // the app never backgrounds between logout and the next login. The
    // foreground handler also clears these on background, but relying
    // solely on that leaves prior-account Noise / yamux state in memory
    // during a rapid logout→login flow.
    const fakeHandle = { close: jest.fn() };
    useTransportStore.setState({
      activeCiphers: new Map([['desk-A', {} as never]]),
      lanHandles: new Map([['desk-A', fakeHandle as never]]),
    });
    useRelayAccountStore.setState({
      email: 'a@example.com',
      accessToken: null,
      refreshToken: null,
      relayBaseUrl: null,
    });

    await useRelayAccountStore.getState().logout();

    const transport = useTransportStore.getState();
    expect(transport.activeCiphers.size).toBe(0);
    expect(transport.lanHandles.size).toBe(0);
    // LAN tunnel was closed, not just detached from the map.
    expect(fakeHandle.close).toHaveBeenCalled();
  });

  it('invokes client.logout() on the network-call branch and still clears local state', async () => {
    usePairedDesktopsStore.getState().addDesktop(desktop('desk-A'));
    useTransportStore.getState().setActiveDesktopId('desk-A');
    useTransportStore.getState().setTransportKind('relay');
    useRelayAccountStore.setState({
      email: 'a@example.com',
      accessToken: 'access-123', // triggers the network-call branch
      refreshToken: 'ref-456',
      relayBaseUrl: 'https://relay.example.com',
    });

    // Clear the RelayClient mock call log from prior tests in this suite
    // so we can assert exactly one invocation here.
    const { RelayClient } = jest.requireMock('../../api/relay/client');
    (RelayClient as jest.Mock).mockClear();

    await useRelayAccountStore.getState().logout();

    // Network path was taken.
    expect(RelayClient).toHaveBeenCalledTimes(1);
    expect(RelayClient).toHaveBeenCalledWith(
      'https://relay.example.com',
      expect.any(Object),
    );
    // All cross-store state is cleared.
    const account = useRelayAccountStore.getState();
    expect(account.email).toBeNull();
    expect(account.accessToken).toBeNull();
    expect(account.refreshToken).toBeNull();
    const transport = useTransportStore.getState();
    expect(transport.activeDesktopId).toBeNull();
    expect(transport.activeTransportKind).toBe('direct');
    expect(usePairedDesktopsStore.getState().desktops).toHaveLength(0);
  });
});
