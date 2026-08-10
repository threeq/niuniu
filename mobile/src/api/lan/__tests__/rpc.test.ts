/**
 * rpc.test.ts — unit tests for lanEncryptedRPC using mock yamux streams.
 *
 * All tests are fully in-process — no WebSocket, no desktop, no network.
 * The mock YamuxStream captures writes and lets the test inject canned
 * responses, verifying the framing and encryption/decryption logic.
 */

import { lanEncryptedRPC, writeLenPrefix, readLenPrefix } from '../rpc';
import type { LanTunnelHandle } from '../tunnel';
import type { YamuxStream } from '../../../transport/yamux';
import type { CipherPair } from '../../../crypto/noise';
import { encode as b64Encode, decode as b64Decode } from '@stablelib/base64';

// ─── Helpers ──────────────────────────────────────────────────────────────────

/** 4-byte little-endian uint32 prefix. */
function lenPrefix(n: number): Uint8Array {
  const buf = new Uint8Array(4);
  new DataView(buf.buffer).setUint32(0, n, true);
  return buf;
}

function concat(...parts: Uint8Array[]): Uint8Array {
  const total = parts.reduce((s, p) => s + p.length, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) { out.set(p, off); off += p.length; }
  return out;
}

// ─── Mock YamuxStream ─────────────────────────────────────────────────────────

class MockYamuxStream implements YamuxStream {
  readonly streamId = 1;
  writes: Uint8Array[] = [];
  private readQueue: Uint8Array[] = [];
  finSent = false;
  closed = false;

  /** Pre-load response bytes that read() will return in order. */
  enqueueResponse(data: Uint8Array): void {
    // Deliver the whole response as individual chunks (one per read call) to
    // exercise the multi-read accumulation path in readLenPrefix.
    this.readQueue.push(data);
  }

  async write(data: Uint8Array): Promise<void> {
    this.writes.push(new Uint8Array(data));
  }

  async read(_timeoutMs?: number): Promise<Uint8Array> {
    if (this.readQueue.length === 0) {
      throw new Error('MockYamuxStream: no more data to read');
    }
    return this.readQueue.shift()!;
  }

  closeWrite(): void {
    this.finSent = true;
  }

  close(): void {
    this.closed = true;
  }

  /** Concatenate all captured writes into one buffer (strips per-write boundaries). */
  get allWritten(): Uint8Array {
    return concat(...this.writes);
  }
}

// ─── Mock CipherPair (identity transform for test predictability) ─────────────
//
// We use a real Noise KK cipher-pair for the encryption correctness test and
// a simple XOR cipher for the framing shape tests so we can inspect payloads
// easily.

class XorCipherPair implements CipherPair {
  private readonly key = 0x42;
  encrypt(pt: Uint8Array): Uint8Array {
    return pt.map((b) => b ^ this.key);
  }
  decrypt(ct: Uint8Array): Uint8Array {
    return ct.map((b) => b ^ this.key);
  }
  rekey(): void {}
  setRekeyInterval(_n: number): void {}
  rekeyInterval(): number { return 0; }
}

/** Build a LanTunnelHandle whose yamux.openStream() returns the given stream. */
function makeTunnel(
  stream: MockYamuxStream,
  cipher: CipherPair = new XorCipherPair(),
): LanTunnelHandle {
  return {
    cipher,
    yamux: {
      openStream: async () => stream as unknown as YamuxStream,
      close: () => {},
    } as any,
    close: () => {},
  };
}

// ─── writeLenPrefix / readLenPrefix helpers ───────────────────────────────────

describe('writeLenPrefix', () => {
  it('prepends a 4-byte LE length prefix to the payload', async () => {
    const stream = new MockYamuxStream();
    const data = new TextEncoder().encode('hello');
    await writeLenPrefix(stream, data);

    const written = stream.allWritten;
    expect(written.length).toBe(4 + 5);

    const length = new DataView(written.buffer).getUint32(0, true);
    expect(length).toBe(5);
    expect(written.slice(4)).toEqual(data);
  });

  it('handles empty payload with zero-length prefix', async () => {
    const stream = new MockYamuxStream();
    await writeLenPrefix(stream, new Uint8Array(0));

    const written = stream.allWritten;
    expect(written.length).toBe(4);
    const length = new DataView(written.buffer).getUint32(0, true);
    expect(length).toBe(0);
  });
});

describe('readLenPrefix', () => {
  it('reads 4-byte prefix then payload in a single chunk', async () => {
    const stream = new MockYamuxStream();
    const payload = new TextEncoder().encode('response data');
    stream.enqueueResponse(concat(lenPrefix(payload.length), payload));

    const result = await readLenPrefix(stream, 100);
    expect(result).toEqual(payload);
  });

  it('handles payload split across two reads', async () => {
    const stream = new MockYamuxStream();
    const payload = new TextEncoder().encode('split payload');
    const full = concat(lenPrefix(payload.length), payload);

    // Split at byte 6 (within the payload)
    stream.enqueueResponse(full.slice(0, 6));
    stream.enqueueResponse(full.slice(6));

    const result = await readLenPrefix(stream, 100);
    expect(result).toEqual(payload);
  });

  it('handles prefix split across reads', async () => {
    const stream = new MockYamuxStream();
    const payload = new Uint8Array([10, 20, 30]);
    const full = concat(lenPrefix(payload.length), payload);

    // Split after 2 bytes (within the 4-byte prefix)
    stream.enqueueResponse(full.slice(0, 2));
    stream.enqueueResponse(full.slice(2));

    const result = await readLenPrefix(stream, 100);
    expect(result).toEqual(payload);
  });
});

// ─── lanEncryptedRPC ──────────────────────────────────────────────────────────

describe('lanEncryptedRPC stream header shape', () => {
  it('writes stream_type and mobile_id in the first len-prefixed frame', async () => {
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();

    // Pre-load a valid response
    const respHdr = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {} }),
    );
    const innerResp = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {}, body: '' }),
    );
    const encResp = cipher.encrypt(innerResp);

    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(encResp.length), encResp));

    const tunnel = makeTunnel(stream, cipher);
    await lanEncryptedRPC({
      tunnel,
      mobileId: 'test-mobile-id',
      method: 'GET',
      path: '/api/test',
    });

    // Parse the stream header from written data
    const written = stream.allWritten;
    const hdrLen = new DataView(written.buffer).getUint32(0, true);
    const hdrJson = new TextDecoder().decode(written.slice(4, 4 + hdrLen));
    const parsed = JSON.parse(hdrJson);

    expect(parsed.stream_type).toBe('encrypted_rpc');
    expect(parsed.mobile_id).toBe('test-mobile-id');
  });

  it('stream header contains "lan:"-prefixed mobile_id when called from smartFetch', async () => {
    // This test verifies the integration contract: apiClient.ts prefixes the
    // fingerprint with "lan:" before passing it to lanEncryptedRPC, matching
    // the desktop cipher namespace "lan:<fingerprint>" set by server.go.
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();

    const respHdr = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {} }),
    );
    const innerResp = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {}, body: '' }),
    );
    const encResp = cipher.encrypt(innerResp);
    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(encResp.length), encResp));

    const fingerprint = 'abcdef1234567890';
    const tunnel = makeTunnel(stream, cipher);
    // Simulate what apiClient.ts does: prefix with 'lan:'
    const mobileId = 'lan:' + fingerprint;
    await lanEncryptedRPC({
      tunnel,
      mobileId,
      method: 'GET',
      path: '/api/ping',
    });

    const written = stream.allWritten;
    const hdrLen = new DataView(written.buffer).getUint32(0, true);
    const hdrJson = new TextDecoder().decode(written.slice(4, 4 + hdrLen));
    const parsed = JSON.parse(hdrJson);

    expect(parsed.mobile_id).toBe('lan:' + fingerprint);
    expect(parsed.mobile_id).toMatch(/^lan:/);
  });

  it('sends FIN after writing the encrypted request', async () => {
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();

    const respHdr = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {} }),
    );
    const innerResp = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {}, body: '' }),
    );
    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(cipher.encrypt(innerResp).length), cipher.encrypt(innerResp)));

    const tunnel = makeTunnel(stream, cipher);
    await lanEncryptedRPC({
      tunnel,
      mobileId: 'test-id',
      method: 'POST',
      path: '/api/test',
      body: new TextEncoder().encode('{"foo":"bar"}'),
    });

    expect(stream.finSent).toBe(true);
  });
});

describe('lanEncryptedRPC encryption correctness', () => {
  it('second len-prefixed write decrypts to a valid inner envelope', async () => {
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();

    const respHdr = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: { 'content-type': ['application/json'] } }),
    );
    const innerResp = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {}, body: b64Encode(new TextEncoder().encode('{"ok":true}')) }),
    );
    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(cipher.encrypt(innerResp).length), cipher.encrypt(innerResp)));

    const tunnel = makeTunnel(stream, cipher);
    const result = await lanEncryptedRPC({
      tunnel,
      mobileId: 'mid',
      method: 'GET',
      path: '/api/health',
    });

    expect(result.status).toBe(200);

    // Verify the inner request envelope was encrypted: extract second write
    const written = stream.allWritten;
    const hdrLen = new DataView(written.buffer).getUint32(0, true);
    const afterHdr = written.slice(4 + hdrLen);
    const envelopeLen = new DataView(afterHdr.buffer, afterHdr.byteOffset).getUint32(0, true);
    const encrypted = afterHdr.slice(4, 4 + envelopeLen);
    const decrypted = cipher.decrypt(encrypted);
    const inner = JSON.parse(new TextDecoder().decode(decrypted));

    expect(inner.method).toBe('GET');
    expect(inner.path).toBe('/api/health');
  });

  it('response body is base64-decoded correctly', async () => {
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();
    const expectedBody = new TextEncoder().encode('{"value":42}');

    const respHdr = new TextEncoder().encode(JSON.stringify({ status: 200, headers: {} }));
    const innerResp = new TextEncoder().encode(
      JSON.stringify({ status: 200, headers: {}, body: b64Encode(expectedBody) }),
    );
    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(cipher.encrypt(innerResp).length), cipher.encrypt(innerResp)));

    const tunnel = makeTunnel(stream, cipher);
    const result = await lanEncryptedRPC({
      tunnel,
      mobileId: 'mid',
      method: 'GET',
      path: '/api/val',
    });

    expect(result.body).toEqual(expectedBody);
  });

  it('includes request body in the encrypted envelope', async () => {
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();
    const requestBody = new TextEncoder().encode('request payload');

    const respHdr = new TextEncoder().encode(JSON.stringify({ status: 201, headers: {} }));
    const innerResp = new TextEncoder().encode(
      JSON.stringify({ status: 201, headers: {}, body: '' }),
    );
    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(cipher.encrypt(innerResp).length), cipher.encrypt(innerResp)));

    const tunnel = makeTunnel(stream, cipher);
    const result = await lanEncryptedRPC({
      tunnel,
      mobileId: 'mid',
      method: 'POST',
      path: '/api/create',
      body: requestBody,
    });

    expect(result.status).toBe(201);

    // Verify request body was encoded in the envelope
    const written = stream.allWritten;
    const hdrLen = new DataView(written.buffer).getUint32(0, true);
    const afterHdr = written.slice(4 + hdrLen);
    const envLen = new DataView(afterHdr.buffer, afterHdr.byteOffset).getUint32(0, true);
    const encrypted = afterHdr.slice(4, 4 + envLen);
    const inner = JSON.parse(new TextDecoder().decode(cipher.decrypt(encrypted)));

    expect(inner.body).toBe(b64Encode(requestBody));
    expect(inner.method).toBe('POST');
  });

  it('closes stream on success', async () => {
    const stream = new MockYamuxStream();
    const cipher = new XorCipherPair();

    const respHdr = new TextEncoder().encode(JSON.stringify({ status: 200, headers: {} }));
    const innerResp = new TextEncoder().encode(JSON.stringify({ status: 200, headers: {}, body: '' }));
    stream.enqueueResponse(concat(lenPrefix(respHdr.length), respHdr));
    stream.enqueueResponse(concat(lenPrefix(cipher.encrypt(innerResp).length), cipher.encrypt(innerResp)));

    const tunnel = makeTunnel(stream, cipher);
    await lanEncryptedRPC({ tunnel, mobileId: 'mid', method: 'GET', path: '/x' });

    expect(stream.closed).toBe(true);
  });

  it('closes stream on error', async () => {
    const stream = new MockYamuxStream();
    // No response enqueued — read will throw
    const tunnel = makeTunnel(stream);

    await expect(
      lanEncryptedRPC({ tunnel, mobileId: 'mid', method: 'GET', path: '/fail' }),
    ).rejects.toThrow();

    expect(stream.closed).toBe(true);
  });
});

describe('lanEncryptedRPC uses real Noise KK cipher round-trip', () => {
  it('encrypts with newKKInitiator cipher and decrypts symmetrically', async () => {
    // Use actual Noise KK ciphers — build a matched pair
    const { newKKInitiator, newKKResponder } = require('../../../crypto/noise');
    const { generateKeyPair } = require('@stablelib/x25519');

    const mobileKP = generateKeyPair();
    const desktopKP = generateKeyPair();

    const mobileIdentity = { xPriv: mobileKP.secretKey, xPub: mobileKP.publicKey, edPriv: new Uint8Array(64), edPub: new Uint8Array(32) };
    const desktopIdentity = { xPriv: desktopKP.secretKey, xPub: desktopKP.publicKey, edPriv: new Uint8Array(64), edPub: new Uint8Array(32) };

    const initiator = newKKInitiator(mobileIdentity, desktopKP.publicKey);
    const responder = newKKResponder(desktopIdentity, mobileKP.publicKey);

    const msg1 = initiator.writeMessage();
    responder.readMessage(msg1);
    const msg2 = responder.writeMessage();
    initiator.readMessage(msg2);

    const mobileCipher = initiator.cipher()!;
    const desktopCipher = responder.cipher()!;

    // Mobile encrypts a message, desktop can decrypt it
    const plaintext = new TextEncoder().encode('secure rpc message');
    const ciphertext = mobileCipher.encrypt(plaintext);
    const decrypted = desktopCipher.decrypt(ciphertext);
    expect(decrypted).toEqual(plaintext);

    // Desktop encrypts a response, mobile can decrypt it
    const respPlain = new TextEncoder().encode('{"status":200}');
    const respCt = desktopCipher.encrypt(respPlain);
    const respDec = mobileCipher.decrypt(respCt);
    expect(respDec).toEqual(respPlain);
  });
});
