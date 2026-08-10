/**
 * LAN tunnel client — performs the Noise KK handshake over WebSocket and
 * returns a LanTunnelHandle wrapping both the CipherPair and a YamuxSession.
 *
 * After the KK handshake completes, the desktop wraps the same WS connection
 * in yamux.Server().  This client speaks yamux so the mobile can open virtual
 * streams and exchange encrypted RPC frames without a separate connection per
 * request.
 *
 * Protocol (matches desktop/internal/lanhost/server.go handleLanTunnel):
 *   1. WebSocket upgrade to `ws://{host}:{port}/lan-tunnel`
 *   2. Send JSON: `{"mobile_xpub_b64": "<base64 X25519 pubkey>"}`
 *   3. Send binary: Noise KK msg1 (initiator writeMessage)
 *   4. Receive binary: Noise KK msg2 (responder writeMessage)
 *   5. CipherPair established — both sides enter yamux framing.
 *
 * Yamux notes:
 *   - Default initial window: 256 KiB per stream (DEFAULT_WINDOW).
 *   - One stream per RPC request (not a throughput path for large file transfers).
 *   - Client uses odd stream IDs (1, 3, 5, …); desktop uses even IDs.
 */

import type { PairedDesktop } from '../../stores/pairedDesktopsStore';
import type { Identity } from '../../crypto/identity';
import type { CipherPair } from '../../crypto/noise';
import { newKKInitiator } from '../../crypto/noise';
import { YamuxSession } from '../../transport/yamux';
import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';

// ─── WebSocket promise helpers ────────────────────────────────────────────────

function waitForOpen(ws: WebSocket): Promise<void> {
  return new Promise((resolve, reject) => {
    if (ws.readyState === WebSocket.OPEN) {
      resolve();
      return;
    }
    ws.addEventListener('open', () => resolve(), { once: true });
    ws.addEventListener('error', (e) => reject(new Error(`WS open error: ${e}`)), { once: true });
  });
}

function waitForMessage(ws: WebSocket): Promise<ArrayBuffer | string> {
  return new Promise((resolve, reject) => {
    ws.addEventListener(
      'message',
      (e: MessageEvent) => resolve(e.data),
      { once: true },
    );
    ws.addEventListener('error', (e) => reject(new Error(`WS message error: ${e}`)), { once: true });
    ws.addEventListener('close', () => reject(new Error('WS closed before message')), { once: true });
  });
}

// ─── LAN tunnel interface ─────────────────────────────────────────────────────

export interface LanTunnelOpts {
  desktop: PairedDesktop;
  endpoint: { host: string; port: number };
  identity: Identity;
}

export interface LanTunnelHandle {
  cipher: CipherPair;
  yamux: YamuxSession;
  close(): void;
}

/**
 * openLanTunnel — performs the Noise KK handshake over a WebSocket to the
 * desktop's `/lan-tunnel` endpoint, then wraps the connection in a YamuxSession.
 *
 * Returns a LanTunnelHandle.  The caller is responsible for calling
 * handle.close() when done (e.g. when the app goes to background).
 */
export async function openLanTunnel(opts: LanTunnelOpts): Promise<LanTunnelHandle> {
  const { host, port } = opts.endpoint;
  const ws = new WebSocket(`ws://${host}:${port}/lan-tunnel`);
  // Ensure binary frames arrive as ArrayBuffer
  ws.binaryType = 'arraybuffer';

  await waitForOpen(ws);

  // Step 1: identify mobile xpub
  ws.send(JSON.stringify({ mobile_xpub_b64: b64Encode(opts.identity.xPub) }));

  // Step 2: run Noise KK initiator
  const desktopXpubBytes = b64Decode(opts.desktop.xpub);
  const init = newKKInitiator(opts.identity, desktopXpubBytes);
  const msg1 = init.writeMessage();
  ws.send(msg1);

  // Step 3: read msg2
  const msg2Data = await waitForMessage(ws);
  const msg2 =
    msg2Data instanceof ArrayBuffer
      ? new Uint8Array(msg2Data)
      : new TextEncoder().encode(msg2Data as string);
  init.readMessage(msg2);

  const cipher = init.cipher();
  if (!cipher) {
    ws.close();
    throw new Error('openLanTunnel: handshake did not produce a cipher');
  }
  // §7.5: rekey every 100,000 frames (≈100 MB at 1 KB avg) to bound key exposure.
  cipher.setRekeyInterval(100000);

  // Handshake done — desktop is now in yamux mode.  Wrap WS in YamuxSession.
  // NOTE: YamuxSession sets ws.binaryType = 'arraybuffer' and installs its
  // own onmessage handler; it takes over the WS from this point on.
  const yamux = new YamuxSession(ws);

  return {
    cipher,
    yamux,
    close: () => yamux.close(),
  };
}

/**
 * LAN feature flag.
 * Set to true to indicate the probe + handshake + yamux infrastructure is ready.
 */
export const LAN_CAPABLE = true;
