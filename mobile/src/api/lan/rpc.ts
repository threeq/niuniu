/**
 * lanEncryptedRPC — send one encrypted RPC over a LAN yamux stream.
 *
 * Protocol (matches desktop/internal/lanhost/server.go handleEncryptedRPCStream):
 *   1. Open a yamux stream on the tunnel.
 *   2. Write len-prefixed stream-header JSON: {"stream_type":"encrypted_rpc","mobile_id":"..."}
 *   3. Write len-prefixed Noise-encrypted inner envelope (method/path/headers/body).
 *   4. Send FIN to signal end of request.
 *   5. Read len-prefixed response-header JSON.
 *   6. Read len-prefixed Noise-encrypted response body.
 *   7. Decrypt and return.
 *
 * Length prefix: 4-byte little-endian uint32, matching the Go side's
 * binary.LittleEndian.PutUint32 framing.
 */

import type { LanTunnelHandle } from './tunnel';
import type { YamuxStream } from '../../transport/yamux';
import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';
import { generateIdempotencyKey } from '../../utils/idempotency';

// ─── Length-prefix framing ─────────────────────────────────────────────────────

/** Write a 4-byte LE length prefix followed by the payload. */
export async function writeLenPrefix(stream: YamuxStream, data: Uint8Array): Promise<void> {
  const prefix = new Uint8Array(4);
  const view = new DataView(prefix.buffer);
  view.setUint32(0, data.length, true /* little-endian */);
  const frame = new Uint8Array(4 + data.length);
  frame.set(prefix, 0);
  frame.set(data, 4);
  await stream.write(frame);
}

/**
 * Read a 4-byte LE length prefix then exactly that many bytes.
 * May call stream.read() multiple times to accumulate a complete message.
 */
export async function readLenPrefix(stream: YamuxStream, timeoutMs?: number): Promise<Uint8Array> {
  // Accumulate bytes until we have at least 4
  const chunks: Uint8Array[] = [];
  let totalBytes = 0;

  // Read until we have the 4-byte header
  while (totalBytes < 4) {
    const chunk = await stream.read(timeoutMs);
    chunks.push(chunk);
    totalBytes += chunk.length;
  }

  // Reassemble
  let buf = _concat(chunks);

  const view = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
  const msgLen = view.getUint32(0, true /* little-endian */);
  buf = buf.subarray(4); // strip prefix

  // Read until we have msgLen bytes
  while (buf.length < msgLen) {
    const chunk = await stream.read(timeoutMs);
    const next = new Uint8Array(buf.length + chunk.length);
    next.set(buf, 0);
    next.set(chunk, buf.length);
    buf = next;
  }

  return buf.subarray(0, msgLen);
}

function _concat(arrays: Uint8Array[]): Uint8Array {
  const total = arrays.reduce((s, a) => s + a.length, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const a of arrays) {
    out.set(a, off);
    off += a.length;
  }
  return out;
}

// ─── RPC envelope types ───────────────────────────────────────────────────────

interface InnerRequest {
  method: string;
  path: string;
  headers: Record<string, string[]>;
  body: string; // base64
}

interface InnerResponse {
  status: number;
  headers: Record<string, string[]>;
  body: string; // base64
}

// ─── lanEncryptedRPC ──────────────────────────────────────────────────────────

export interface LanRPCOptions {
  tunnel: LanTunnelHandle;
  mobileId: string; // fingerprint of mobile xpub
  method: string;
  path: string;
  headers?: Record<string, string[]>;
  body?: Uint8Array;
}

export interface LanRPCResult {
  status: number;
  headers: Record<string, string[]>;
  body: Uint8Array;
}

/**
 * Sends a single encrypted RPC over a fresh yamux stream on the provided
 * LAN tunnel.  The stream is opened, used for exactly one request/response
 * round-trip, then closed.
 *
 * Returns a plain object; apiClient.ts wraps this in a Response.
 */
export async function lanEncryptedRPC(opts: LanRPCOptions): Promise<LanRPCResult> {
  const stream = await opts.tunnel.yamux.openStream();

  try {
    // ── Write stream header ──────────────────────────────────────────────────
    const streamHeader = new TextEncoder().encode(
      JSON.stringify({
        stream_type: 'encrypted_rpc',
        mobile_id: opts.mobileId,
      }),
    );
    // Attach X-Idempotency-Key to the inner envelope headers so the desktop
    // can deduplicate unsafe requests if it maintains its own idempotency cache.
    const headers = { ...(opts.headers ?? {}) };
    if (!headers['X-Idempotency-Key'] && !headers['x-idempotency-key']) {
      headers['X-Idempotency-Key'] = [generateIdempotencyKey()];
    }
    await writeLenPrefix(stream, streamHeader);

    // ── Encrypt and write inner envelope ────────────────────────────────────
    const inner: InnerRequest = {
      method: opts.method,
      path: opts.path,
      headers,
      body: opts.body ? b64Encode(opts.body) : '',
    };
    const plaintext = new TextEncoder().encode(JSON.stringify(inner));
    const ciphertext = opts.tunnel.cipher.encrypt(plaintext);
    await writeLenPrefix(stream, ciphertext);

    // Signal end of request
    stream.closeWrite();

    // ── Read response ────────────────────────────────────────────────────────
    const respHdrBytes = await readLenPrefix(stream);
    const respHdr = JSON.parse(new TextDecoder().decode(respHdrBytes)) as {
      status: number;
      headers: Record<string, string[]>;
    };

    const respCiphertext = await readLenPrefix(stream);
    const respPlain = opts.tunnel.cipher.decrypt(respCiphertext);
    const respInner = JSON.parse(new TextDecoder().decode(respPlain)) as InnerResponse;

    stream.close();

    return {
      status: respHdr.status ?? respInner.status,
      headers: respHdr.headers ?? respInner.headers ?? {},
      body: respInner.body ? b64Decode(respInner.body) : new Uint8Array(0),
    };
  } catch (err) {
    stream.close();
    throw err;
  }
}
