import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';
import { newKKInitiator } from '../../crypto/noise';
import type { CipherPair } from '../../crypto/noise';
import type { Identity } from '../../crypto/identity';

export interface HandshakeResult {
  cipher: CipherPair;
}

/**
 * performHandshake — executes the Noise KK handshake with a desktop via the relay.
 *
 * Flow:
 *   1. Build KKInitiator with mobile identity + desktop static xpub.
 *   2. WriteMessage (msg1) with empty payload → POST to /d/:desktopId/crypto/handshake
 *   3. Parse msg2 from response → ReadMessage → CipherPair.
 */
export async function performHandshake(
  baseUrl: string,
  desktopId: string,
  mobileIdentity: Identity,
  desktopXpubBytes: Uint8Array,
  deviceToken: string,
  dpopHeader?: string,
): Promise<CipherPair> {
  const kk = newKKInitiator(mobileIdentity, desktopXpubBytes);

  // Generate msg1
  const msg1 = kk.writeMessage();
  const msg1B64 = b64Encode(msg1);

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${deviceToken}`,
  };
  if (dpopHeader) {
    headers['X-DPoP'] = dpopHeader;
  }

  const res = await fetch(`${baseUrl}/d/${desktopId}/crypto/handshake`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ noise_msg_b64: msg1B64 }),
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    // Relay returns `{"error": "<code>"}` (string); older shape was `{"error": {"message": "..."}}`.
    const errField = body?.error;
    const errDetail =
      typeof errField === 'string'
        ? errField
        : (errField?.message ?? errField?.code);
    throw new Error(`handshake: HTTP ${res.status}${errDetail ? ` (${errDetail})` : ''}`);
  }

  const data: { noise_msg_b64: string } = await res.json();
  const msg2 = b64Decode(data.noise_msg_b64);

  // Complete handshake
  kk.readMessage(msg2);

  const cipher = kk.cipher();
  if (!cipher) throw new Error('handshake: cipher not available after msg2');
  // §7.5: rekey every 100,000 frames (≈100 MB at 1 KB avg) to bound key exposure.
  cipher.setRekeyInterval(100000);
  return cipher;
}
