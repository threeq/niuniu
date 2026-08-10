/**
 * LAN-only pairing (spec §6.5).
 *
 * Used when the mobile is on the same network as the desktop and the user
 * does NOT have a relay account.  The flow:
 *   1. Desktop enters LAN-only pair mode via the server's REST surface
 *      (advertises _niuniu-pair._tcp for 120 s, accepts POST /pair/claim).
 *   2. Mobile discovers the service via mDNS (see discover.ts) or the user
 *      enters the desktop's LAN address manually.
 *   3. Mobile calls lanOnlyPairClaim() with its own key material.
 *   4. Desktop responds with its identity material.
 *   5. Caller persists the result (desktop_id, xpub, sign_pub) as a pinned
 *      desktop for future LAN connections.
 *
 * This path never contacts the relay.
 */

export interface LanOnlyPairClaimRequest {
  /** Mobile's X25519 public key, base64-encoded. */
  mobileXpubB64: string;
  /** Mobile's Ed25519 public key, base64-encoded. */
  mobileSignPubB64: string;
  /** Human-readable name for this mobile device (optional). */
  mobileName?: string;
  /** Platform string, e.g. "ios" or "android" (optional). */
  mobilePlatform?: string;
}

export interface LanOnlyPairClaimResponse {
  /** Stable opaque desktop identifier. */
  desktop_id: string;
  /** Human-readable name for this desktop. */
  desktop_name: string;
  /** Desktop's X25519 public key, base64-encoded.  Pin this for KK handshakes. */
  desktop_xpub_b64: string;
  /** Desktop's Ed25519 public key, base64-encoded.  Pin this for challenge verification. */
  desktop_sign_pub_b64: string;
}

/**
 * lanOnlyPairClaim — POST /pair/claim to the desktop's LAN address.
 *
 * @param desktopLanBaseUrl  Base URL of the desktop LAN server, e.g.
 *                           "http://192.168.1.42:PORT".  Discovered via mDNS
 *                           (lanDiscover) or entered manually.
 * @param req                Mobile key material to send to the desktop.
 * @returns                  The desktop's identity material.
 * @throws                   On network error, non-2xx HTTP response, or bad body.
 *
 */
export async function lanOnlyPairClaim(
  desktopLanBaseUrl: string,
  req: LanOnlyPairClaimRequest,
): Promise<LanOnlyPairClaimResponse> {
  const url = `${desktopLanBaseUrl.replace(/\/$/, '')}/pair/claim`;
  const body: {
    mobile_xpub_b64: string;
    mobile_sign_pub_b64: string;
    mobile_name?: string;
    mobile_platform?: string;
  } = {
    mobile_xpub_b64: req.mobileXpubB64,
    mobile_sign_pub_b64: req.mobileSignPubB64,
  };
  if (req.mobileName) body.mobile_name = req.mobileName;
  if (req.mobilePlatform) body.mobile_platform = req.mobilePlatform;

  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`lanOnlyPairClaim: ${res.status} ${text}`);
  }

  const data = (await res.json()) as LanOnlyPairClaimResponse;
  if (!data.desktop_id || !data.desktop_xpub_b64 || !data.desktop_sign_pub_b64) {
    throw new Error('lanOnlyPairClaim: incomplete response from desktop');
  }
  return data;
}
