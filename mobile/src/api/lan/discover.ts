/**
 * LAN discovery — probe-based endpoint finder.
 *
 * Two-path approach:
 *   1. If `react-native-zeroconf` is available (native-linked Expo prebuild),
 *      use mDNS to discover `_niuniu-mobile._tcp` services.
 *   2. Fallback: probe each of the desktop's `lastKnownLanEndpoints` via HTTP
 *      GET `/.well-known/niuniu-identity` with a short timeout, first match wins.
 *
 * react-native-zeroconf is loaded lazily so the module won't crash in Expo Go
 * or managed workflow where the native module isn't linked.
 */

import type { PairedDesktop } from '../../stores/pairedDesktopsStore';
import { fingerprintXpub } from '../../crypto/fingerprint';
import { decode as b64Decode } from '@stablelib/base64';

// ─── Identity response ────────────────────────────────────────────────────────

export interface IdentityResponse {
  version: number;
  desktop_id: string;
  xpub_fingerprint: string;
}

// ─── Probe a single endpoint ──────────────────────────────────────────────────

/**
 * Probes `http://{host}:{port}/.well-known/niuniu-identity` with an optional
 * timeout. Returns the parsed IdentityResponse, or null on any error or
 * unexpected shape.
 */
export async function probeEndpoint(
  host: string,
  port: number,
  timeoutMs = 2000,
): Promise<IdentityResponse | null> {
  try {
    const ctrl = new AbortController();
    const to = setTimeout(() => ctrl.abort(), timeoutMs);
    const resp = await fetch(`http://${host}:${port}/.well-known/niuniu-identity`, {
      signal: ctrl.signal,
    });
    clearTimeout(to);
    if (!resp.ok) return null;
    const body = await resp.json();
    if (body.version !== 1) return null;
    return body as IdentityResponse;
  } catch {
    return null;
  }
}

// ─── Find a matching LAN endpoint ────────────────────────────────────────────

/**
 * Searches the desktop's known LAN endpoints for one whose identity fingerprint
 * matches the paired desktop's pinned xpub. Probes all candidates in parallel
 * and returns the first match, or null if none match within the time budget.
 *
 * Also tries mDNS via react-native-zeroconf if the native module is available,
 * though that path is opportunistic and requires a prebuild.
 */
export async function findLanEndpoint(
  desktop: PairedDesktop,
  overallTimeoutMs = 3000,
): Promise<{ host: string; port: number } | null> {
  const pinnedFp = fingerprintXpub(b64Decode(desktop.xpub));
  const candidates = desktop.lastKnownLanEndpoints ?? [];

  if (candidates.length === 0) {
    // No cached endpoints — nothing to probe; mDNS path below may still find one.
    // For now, no candidates means no LAN path.
    return null;
  }

  const timeoutPromise = new Promise<null>((resolve) =>
    setTimeout(() => resolve(null), overallTimeoutMs),
  );

  const probePromise = Promise.any(
    candidates.map(async (c) => {
      const ir = await probeEndpoint(c.host, c.port);
      if (
        ir &&
        ir.desktop_id === desktop.desktopId &&
        ir.xpub_fingerprint === pinnedFp
      ) {
        return { host: c.host, port: c.port };
      }
      throw new Error('no match');
    }),
  ).catch(() => null);

  const result = await Promise.race([probePromise, timeoutPromise]);
  return result;
}

// ─── mDNS discovery (optional native path) ────────────────────────────────────

/**
 * Attempts to resolve a LAN endpoint via mDNS using react-native-zeroconf.
 * This requires the native module to be linked (Expo prebuild / bare workflow).
 * Returns null if the module is unavailable or if no matching service is found
 * within the timeout.
 *
 * NOTE: This is opportunistic. In Expo Go / managed workflow this will always
 * return null because the native module isn't linked.
 */
export async function findLanEndpointViaMdns(
  desktop: PairedDesktop,
  timeoutMs = 5000,
): Promise<{ host: string; port: number } | null> {
  let Zeroconf: any;
  try {
    // Lazy import — won't throw if module is absent in managed workflow
    Zeroconf = require('react-native-zeroconf').default;
  } catch {
    return null;
  }

  if (!Zeroconf) return null;

  const pinnedFp = fingerprintXpub(b64Decode(desktop.xpub));

  return new Promise((resolve) => {
    const zeroconf = new Zeroconf();
    const timer = setTimeout(() => {
      zeroconf.stop();
      resolve(null);
    }, timeoutMs);

    zeroconf.on('resolved', (service: any) => {
      // TXT record fields: did, ifp, v, port
      const txt: Record<string, string> = service.txt ?? {};
      if (txt.did !== desktop.desktopId) return;
      if (txt.ifp !== pinnedFp) return;
      clearTimeout(timer);
      zeroconf.stop();
      const host: string = service.host ?? service.addresses?.[0];
      const port: number = Number(txt.port ?? service.port);
      if (host && port) {
        resolve({ host, port });
      } else {
        resolve(null);
      }
    });

    zeroconf.on('error', () => {
      clearTimeout(timer);
      resolve(null);
    });

    zeroconf.scan('_niuniu-mobile', 'tcp', 'local.');
  });
}
