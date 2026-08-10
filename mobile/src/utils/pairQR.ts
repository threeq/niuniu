/**
 * pairQR — utilities for parsing the niuniu-pair:// QR code URL emitted by
 * the desktop pairing dialog.
 *
 * URL format (v1):
 *   niuniu-pair://v1?r=<relayHost>&d=<desktopId>&s=<sessionId>
 *     &xk=<desktopXpubB64>&sk=<desktopSignPubB64>&n=<nonceB64>&t=<expiresAt>
 *
 * All base64 values use standard (padded) base64.
 */

export interface PairingQR {
  relayHost: string;
  desktopId: string;
  sessionId: string;
  desktopXpubB64: string;
  desktopSignPubB64: string;
  nonceB64: string;
  expiresAt: number; // unix seconds
}

/**
 * parsePairingURL — parse a niuniu-pair://v1?... URL into a PairingQR.
 * Returns null if the URL is malformed or missing required params.
 */
export function parsePairingURL(url: string): PairingQR | null {
  if (!url.startsWith('niuniu-pair://v1?')) return null;
  const query = url.slice('niuniu-pair://v1?'.length);
  const params = new URLSearchParams(query);
  const r = params.get('r');
  const d = params.get('d');
  const s = params.get('s');
  const xk = params.get('xk');
  const sk = params.get('sk');
  const n = params.get('n');
  const t = params.get('t');
  if (!r || !d || !s || !xk || !sk || !n || !t) return null;
  const expiresAt = parseInt(t, 10);
  if (isNaN(expiresAt)) return null;
  return {
    relayHost: r,
    desktopId: d,
    sessionId: s,
    desktopXpubB64: xk,
    desktopSignPubB64: sk,
    nonceB64: n,
    expiresAt,
  };
}
