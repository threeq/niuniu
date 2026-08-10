export interface ClaimRequest {
  sessionId: string;
  mobileXpub: string;       // base64
  mobileSignPub: string;    // base64
  mobileName: string;
  platform: 'ios' | 'android' | string;
  pairingNonceB64?: string; // forwarded for desktop-side SAS derivation
}

export interface ClaimResponse {
  status: 'pending' | 'claimed' | 'confirmed' | 'expired' | 'error';
  relay_device_token?: string;
  desktop_xpub_b64?: string;
  desktop_sign_pub_b64?: string;
  desktop_id?: string;
  desktop_name?: string;
  mobile_id?: string;
  /**
   * Base64-encoded 32-byte ChaCha20-Poly1305 key the relay generated for
   * this (mobile, desktop) pair at confirm time. The mobile uses it for
   * the pairkey transport (random per-message nonce, no handshake). Empty
   * on relays that predate the 008 migration; the mobile then falls back
   * to the legacy noise / plaintext mode.
   */
  pair_key_b64?: string;
  /** Conflict flag: set when a prior mobile device with the same name/platform exists */
  conflict?: boolean;
  prior_mobile_id?: string;
  prior_mobile_name?: string;
}

/**
 * resolveConflict — call after the user chooses "Replace previous device".
 *
 * POST /api/pairing/sessions/:id/resolve-conflict
 */
export async function resolveConflict(
  baseUrl: string,
  accessToken: string,
  sessionId: string,
  replacePriorId: string,
): Promise<void> {
  const res = await fetch(
    `${baseUrl}/api/pairing/sessions/${sessionId}/resolve-conflict`,
    {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${accessToken}`,
      },
      body: JSON.stringify({ replace_prior_id: replacePriorId }),
    },
  );
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const msg = body?.error?.message ?? `HTTP ${res.status}`;
    throw new Error(`pairing/resolve-conflict: ${msg}`);
  }
}

/**
 * claim — long-poll claim of a pairing session.
 *
 * POST /api/pairing/sessions/:id/claim
 */
export async function claim(
  baseUrl: string,
  accessToken: string,
  req: ClaimRequest,
): Promise<ClaimResponse> {
  const res = await fetch(
    `${baseUrl}/api/pairing/sessions/${req.sessionId}/claim`,
    {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${accessToken}`,
      },
      body: JSON.stringify({
        mobile_xpub_b64: req.mobileXpub,
        mobile_sign_pub_b64: req.mobileSignPub,
        mobile_name: req.mobileName,
        mobile_platform: req.platform,
        ...(req.pairingNonceB64 != null ? { pairing_nonce_b64: req.pairingNonceB64 } : {}),
      }),
    },
  );

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const msg = body?.error?.message ?? `HTTP ${res.status}`;
    throw new Error(`pairing/claim: ${msg}`);
  }

  return res.json() as Promise<ClaimResponse>;
}
