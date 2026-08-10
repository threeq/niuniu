/**
 * fetchOrThrow — wraps a `fetch()` so connection-level failures (the kind
 * React Native surfaces as the unhelpful `TypeError: Network request failed`)
 * become diagnosable: the rejection includes the URL and method we were
 * trying to reach, plus the original cause. Without this, every fetch call
 * site that times out, hits a stale LAN IP, or fails TLS just bubbles up
 * the same opaque message and the user has no way to tell whether the
 * problem is the relay, the LAN endpoint, or the direct server URL.
 *
 * Production callers should always use this wrapper. The shape mirrors
 * `fetch()` exactly so it's a drop-in replacement.
 */
export async function fetchOrThrow(
  url: string,
  init?: RequestInit,
): Promise<Response> {
  if (typeof url !== 'string' || url.length === 0) {
    throw new Error(`fetch: invalid URL (got ${typeof url}: ${String(url)})`);
  }
  // Catch obvious URL-construction bugs (undefined interpolated into a
  // template literal would otherwise hit fetch and surface as the same
  // generic Network-request-failed). The check is intentionally narrow —
  // we don't want to reject relative URLs since plain fetch supports them.
  if (url.startsWith('undefined') || url.startsWith('null')) {
    throw new Error(`fetch: URL begins with literal "${url.slice(0, 9)}", a state hydration bug — bailed before sending`);
  }

  const method = (init?.method ?? 'GET').toUpperCase();
  try {
    return await fetch(url, init);
  } catch (err) {
    // RN's fetch rejects with `TypeError: Network request failed` for any
    // connection-level problem (DNS, TLS, refused, timeout, ATS, …) and
    // gives the caller no indication of what URL it was trying to reach.
    // Re-throw with the URL + method baked in so the next "Network request
    // failed" surface in the UI carries enough context to diagnose without
    // attaching a debugger.
    const reason = err instanceof Error ? err.message : String(err);
    const wrapped = new Error(`fetch ${method} ${url} failed: ${reason}`);
    // Preserve the original error in `cause` so callers that introspect
    // beyond .message can still get at it.
    (wrapped as Error & { cause?: unknown }).cause = err;
    throw wrapped;
  }
}
