import {
  decideInitialRoute,
  type InitialHref,
  type InitialRoute,
  type InitialRouteInputs,
} from '../initialRoute';

/** Convenience for writing cases — all flags default to the most-common
 *  "nothing configured yet" state so tests only override what matters. */
const inputs = (overrides: Partial<InitialRouteInputs>): InitialRouteInputs => ({
  allHydrated: true,
  relayAuthed: false,
  activeDesktopId: null,
  pairedDesktopIds: [],
  hasServer: false,
  serverAuthDisabled: false,
  isServerAuthed: false,
  ...overrides,
});

const loading = (): InitialRoute => ({ kind: 'loading' });
const redirect = (href: InitialHref): InitialRoute => ({ kind: 'redirect', href });

describe('decideInitialRoute', () => {
  describe('hydration gate', () => {
    it('shows loading when not all stores have hydrated', () => {
      expect(decideInitialRoute(inputs({ allHydrated: false }))).toEqual(
        loading(),
      );
    });

    it('shows loading even if relayAuthed is true but not hydrated yet', () => {
      // The caller must not compute relayAuthed=true until the account
      // store has actually hydrated, but the function itself still
      // gates on allHydrated as a defence-in-depth check.
      expect(
        decideInitialRoute(inputs({ allHydrated: false, relayAuthed: true })),
      ).toEqual(loading());
    });
  });

  describe('relay path (priority over server path per design doc §1)', () => {
    it('relayAuthed + activeDesktopId still in paired list → tabs (skip picker)', () => {
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: 'desk-1',
            pairedDesktopIds: ['desk-1', 'desk-2'],
          }),
        ),
      ).toEqual(redirect('/(tabs)/projects'));
    });

    it('relayAuthed + no activeDesktopId (first post-login) → picker', () => {
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: null,
            pairedDesktopIds: ['desk-1'],
          }),
        ),
      ).toEqual(redirect('/(auth)/relay-desktops'));
    });

    it('relayAuthed + stale activeDesktopId (not in paired list) → picker', () => {
      // Guards the "user unpaired their active desktop on another device,
      // then cold-started" scenario. decideInitialRoute must not trust
      // activeDesktopId blindly — caller could forget the intersection
      // check, which was the whole reason for moving the validation
      // inside the function.
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: 'desk-gone',
            pairedDesktopIds: ['desk-1', 'desk-2'],
          }),
        ),
      ).toEqual(redirect('/(auth)/relay-desktops'));
    });

    it('relayAuthed + empty paired list → picker', () => {
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: 'desk-1',
            pairedDesktopIds: [],
          }),
        ),
      ).toEqual(redirect('/(auth)/relay-desktops'));
    });

    it('relayAuthed wins over configured server with auth disabled', () => {
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: 'desk-1',
            pairedDesktopIds: ['desk-1'],
            hasServer: true,
            serverAuthDisabled: true,
          }),
        ),
      ).toEqual(redirect('/(tabs)/projects'));
    });

    it('relayAuthed wins over server-path auth state', () => {
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: null,
            hasServer: true,
            isServerAuthed: true,
          }),
        ),
      ).toEqual(redirect('/(auth)/relay-desktops'));
    });
  });

  describe('server path (fallback when not relay-authed)', () => {
    it('no server configured → server setup', () => {
      expect(decideInitialRoute(inputs({ hasServer: false }))).toEqual(
        redirect('/(auth)/server'),
      );
    });

    it('has server + auth disabled → straight to tabs', () => {
      expect(
        decideInitialRoute(
          inputs({ hasServer: true, serverAuthDisabled: true }),
        ),
      ).toEqual(redirect('/(tabs)/projects'));
    });

    it('has server + auth required + not authed → login', () => {
      expect(
        decideInitialRoute(
          inputs({
            hasServer: true,
            serverAuthDisabled: false,
            isServerAuthed: false,
          }),
        ),
      ).toEqual(redirect('/(auth)/auth-email'));
    });

    it('has server + auth required + authed → tabs', () => {
      expect(
        decideInitialRoute(
          inputs({
            hasServer: true,
            serverAuthDisabled: false,
            isServerAuthed: true,
          }),
        ),
      ).toEqual(redirect('/(tabs)/projects'));
    });
  });

  describe('edge: first-time user with nothing configured', () => {
    it('lands on server setup page', () => {
      expect(decideInitialRoute(inputs({}))).toEqual(redirect('/(auth)/server'));
    });
  });

  describe('pairedDesktopIds iterable contract', () => {
    it('accepts a Set', () => {
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: 'desk-X',
            pairedDesktopIds: new Set(['desk-A', 'desk-X', 'desk-B']),
          }),
        ),
      ).toEqual(redirect('/(tabs)/projects'));
    });

    it('short-circuits on first match (no need to consume the full iterable)', () => {
      // Build a generator that would throw on 2nd access, to prove
      // decideInitialRoute stops at the first match.
      function* ids() {
        yield 'desk-X';
        throw new Error('iterator consumed past first match');
      }
      expect(
        decideInitialRoute(
          inputs({
            relayAuthed: true,
            activeDesktopId: 'desk-X',
            pairedDesktopIds: ids(),
          }),
        ),
      ).toEqual(redirect('/(tabs)/projects'));
    });
  });
});
