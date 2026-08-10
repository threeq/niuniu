/**
 * Decides the initial route for `app/index.tsx` on app launch.
 *
 * Extracted as a pure function so the routing matrix — which is the
 * entry point for the entire mobile-login-flow optimization — is
 * unit-testable without React rendering infrastructure. `index.tsx`
 * reads the relevant store slices + hydration flags, passes them in,
 * and wraps the returned route in `<Redirect />` (or the loading
 * spinner for `kind: 'loading'`).
 *
 * Routing matrix (in evaluation order):
 *
 *   any store still hydrating?              → loading
 *   relay authed + persisted desktop still paired → /(tabs)/projects
 *   relay authed (no choice / stale choice) → /(auth)/relay-desktops
 *   no server configured                    → /(auth)/server
 *   server has auth disabled                → /(tabs)/projects
 *   server not yet authed                   → /(auth)/auth-email
 *   server authed                           → /(tabs)/projects
 */

export type InitialRoute =
  | { kind: 'loading' }
  | { kind: 'redirect'; href: InitialHref };

/**
 * The closed set of targets this decision can produce. Kept as a literal
 * union so callers get compile-time safety against typos in route paths.
 */
export type InitialHref =
  | '/(tabs)/projects'
  | '/(auth)/relay-desktops'
  | '/(auth)/server'
  | '/(auth)/auth-email';

export interface InitialRouteInputs {
  /** True once every persist middleware in play has finished rehydrating
   *  AND the auth store has finished its initial load. Gates routing. */
  allHydrated: boolean;

  /** True when the relay account store holds a valid access token. Takes
   *  priority over server auth per design. */
  relayAuthed: boolean;

  /** The user's persisted desktop choice (from transportStore), or null.
   *  Validated against `pairedDesktopIds` internally — the function
   *  decides whether the choice is still live, so callers can't forget
   *  to cross-check the two stores. */
  activeDesktopId: string | null;

  /** The set of desktop IDs currently in `pairedDesktopsStore`.
   *  Accepts any iterable so callers don't have to build a Set / Array
   *  specifically for this function. */
  pairedDesktopIds: Iterable<string>;

  /** True when the user has configured at least one niuniu server. */
  hasServer: boolean;

  /** True when the active server has explicitly disabled auth
   *  (server returns `auth_enabled: false` on /api/health). */
  serverAuthDisabled: boolean;

  /** True when the server auth store holds a valid JWT. */
  isServerAuthed: boolean;
}

export function decideInitialRoute(inputs: InitialRouteInputs): InitialRoute {
  if (!inputs.allHydrated) {
    return { kind: 'loading' };
  }

  // Relay path takes priority over server path per design doc §1.
  if (inputs.relayAuthed) {
    if (hasValidActiveDesktop(inputs.activeDesktopId, inputs.pairedDesktopIds)) {
      return { kind: 'redirect', href: '/(tabs)/projects' };
    }
    return { kind: 'redirect', href: '/(auth)/relay-desktops' };
  }

  // Server path fallback.
  if (!inputs.hasServer) {
    return { kind: 'redirect', href: '/(auth)/server' };
  }
  if (inputs.serverAuthDisabled) {
    return { kind: 'redirect', href: '/(tabs)/projects' };
  }
  if (!inputs.isServerAuthed) {
    return { kind: 'redirect', href: '/(auth)/auth-email' };
  }
  return { kind: 'redirect', href: '/(tabs)/projects' };
}

function hasValidActiveDesktop(
  activeDesktopId: string | null,
  pairedDesktopIds: Iterable<string>,
): boolean {
  if (!activeDesktopId) return false;
  for (const id of pairedDesktopIds) {
    if (id === activeDesktopId) return true;
  }
  return false;
}
