/**
 * yamux.test.ts — unit tests for the yamux client transport layer.
 *
 * Uses a lightweight mock WebSocket that captures sent binary frames and
 * allows test code to inject incoming frames via triggerMessage().
 *
 * All tests are standalone — no actual network or desktop connection needed.
 */

import {
  YamuxSession,
  YamuxStream,
  YamuxStreamClosedError,
  YamuxStreamResetError,
  FrameType,
  FLAG_SYN,
  FLAG_ACK,
  FLAG_FIN,
  FLAG_RST,
  DEFAULT_WINDOW,
  HEADER_SIZE,
  encodeFrame,
  decodeFrame,
} from '../yamux';

// ─── Mock WebSocket ────────────────────────────────────────────────────────────

class MockWebSocket {
  readyState = 1; // OPEN
  binaryType = 'arraybuffer';
  onmessage: ((e: { data: ArrayBuffer | SharedArrayBuffer }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  sent: Uint8Array[] = [];

  send(data: ArrayBuffer | Uint8Array): void {
    const bytes =
      data instanceof ArrayBuffer ? new Uint8Array(data) : data;
    this.sent.push(new Uint8Array(bytes));
  }

  close(): void {
    this.readyState = 3; // CLOSED
  }

  /** Simulate an incoming binary frame from the peer. */
  triggerMessage(data: Uint8Array): void {
    if (this.onmessage) {
      // Copy to a plain ArrayBuffer as a real WS would deliver
      const ab = data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength);
      this.onmessage({ data: ab });
    }
  }

  triggerClose(): void {
    if (this.onclose) this.onclose();
  }

  /** Return all sent frames decoded (header + raw payload bytes). */
  get sentFrames(): Array<{ frame: ReturnType<typeof decodeFrame>; payload: Uint8Array }> {
    return this.sent.map((buf) => {
      const frame = decodeFrame(buf)!;
      const payload = buf.slice(HEADER_SIZE);
      return { frame, payload };
    });
  }

  /** Clear the sent log. */
  resetSent(): void {
    this.sent = [];
  }
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeSession(): { session: YamuxSession; ws: MockWebSocket } {
  const ws = new MockWebSocket();
  const session = new YamuxSession(ws as unknown as WebSocket);
  return { session, ws };
}

/** Build a Data frame with optional payload. */
function buildDataFrame(
  streamId: number,
  flags: number,
  payload: Uint8Array = new Uint8Array(0),
): Uint8Array {
  const hdr = encodeFrame(FrameType.Data, flags, streamId, payload.length);
  const out = new Uint8Array(HEADER_SIZE + payload.length);
  out.set(hdr, 0);
  out.set(payload, HEADER_SIZE);
  return out;
}

function buildWindowUpdateFrame(streamId: number, delta: number, flags = 0): Uint8Array {
  return encodeFrame(FrameType.WindowUpdate, flags, streamId, delta);
}

/**
 * Build a Ping frame.  Per the yamux spec, Ping carries NO payload bytes —
 * the length field is an opaque 32-bit value, not a byte count.
 * opaqueValue is the value placed in the Length header field.
 */
function buildPingFrame(flags: number, opaqueValue = 0): Uint8Array {
  return encodeFrame(FrameType.Ping, flags, 0, opaqueValue);
}

// ─── Tests: openStream / SYN ──────────────────────────────────────────────────

describe('TestOpenStreamSendsSYN', () => {
  it('sends a SYN Data frame when opening a stream', async () => {
    const { session, ws } = makeSession();
    ws.resetSent();

    await session.openStream();

    // Should have sent at least one frame with SYN flag
    const synFrames = ws.sentFrames.filter(
      ({ frame }) => frame!.type === FrameType.Data && (frame!.flags & FLAG_SYN) !== 0,
    );
    expect(synFrames.length).toBeGreaterThanOrEqual(1);

    const { frame } = synFrames[0];
    expect(frame!.version).toBe(1);
    expect(frame!.streamId).toBe(1); // first odd client ID
    expect(frame!.length).toBe(0);
  });

  it('assigns odd stream IDs (1, 3, 5, ...)', async () => {
    const { session, ws } = makeSession();
    ws.resetSent();

    const s1 = await session.openStream();
    const s2 = await session.openStream();
    const s3 = await session.openStream();

    expect(s1.streamId).toBe(1);
    expect(s2.streamId).toBe(3);
    expect(s3.streamId).toBe(5);
  });

  it('sends a WindowUpdate SYN frame after SYN to advertise receive window', async () => {
    const { session, ws } = makeSession();
    ws.resetSent();

    await session.openStream();

    const wuFrames = ws.sentFrames.filter(
      ({ frame }) =>
        frame!.type === FrameType.WindowUpdate && (frame!.flags & FLAG_SYN) !== 0,
    );
    expect(wuFrames.length).toBeGreaterThanOrEqual(1);
    expect(wuFrames[0].frame!.length).toBe(DEFAULT_WINDOW);
  });

  it('rejects openStream on a closed session', async () => {
    const { session } = makeSession();
    session.close();
    await expect(session.openStream()).rejects.toThrow();
  });
});

// ─── Tests: Data frame round-trip ─────────────────────────────────────────────

describe('TestDataFrameRoundTrip', () => {
  it('write sends Data frames on the wire', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();
    ws.resetSent();

    const payload = new TextEncoder().encode('hello yamux');
    await stream.write(payload);

    const dataFrames = ws.sentFrames.filter(({ frame }) => frame!.type === FrameType.Data && frame!.flags === 0);
    expect(dataFrames.length).toBe(1);
    expect(dataFrames[0].frame!.length).toBe(payload.length);
    expect(dataFrames[0].payload).toEqual(payload);
  });

  it('read returns the payload from an incoming Data frame', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    const incoming = new TextEncoder().encode('world');
    // Deliver a Data frame from the "server"
    ws.triggerMessage(buildDataFrame(stream.streamId, 0, incoming));

    const received = await stream.read(100);
    expect(received).toEqual(incoming);
  });

  it('read returns chunks in order across multiple frames', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    const chunk1 = new Uint8Array([1, 2, 3]);
    const chunk2 = new Uint8Array([4, 5, 6]);

    ws.triggerMessage(buildDataFrame(stream.streamId, 0, chunk1));
    ws.triggerMessage(buildDataFrame(stream.streamId, 0, chunk2));

    const r1 = await stream.read(100);
    const r2 = await stream.read(100);
    expect(r1).toEqual(chunk1);
    expect(r2).toEqual(chunk2);
  });

  it('write respects DEFAULT_WINDOW capacity without blocking for small writes', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();
    ws.resetSent();

    // Write 100 bytes — well within the 256 KiB window
    const data = new Uint8Array(100).fill(0xab);
    await stream.write(data);

    const frames = ws.sentFrames.filter(({ frame }) => frame!.type === FrameType.Data && frame!.flags === 0);
    expect(frames.length).toBe(1);
    expect(frames[0].payload).toEqual(data);
  });
});

// ─── Tests: closeWrite / FIN ──────────────────────────────────────────────────

describe('TestCloseWriteSendsFIN', () => {
  it('sends a Data frame with FIN flag', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();
    ws.resetSent();

    stream.closeWrite();

    const finFrames = ws.sentFrames.filter(
      ({ frame }) => frame!.type === FrameType.Data && (frame!.flags & FLAG_FIN) !== 0,
    );
    expect(finFrames.length).toBe(1);
    expect(finFrames[0].frame!.streamId).toBe(stream.streamId);
    expect(finFrames[0].frame!.length).toBe(0);
  });

  it('does not send duplicate FIN on second closeWrite call', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();
    ws.resetSent();

    stream.closeWrite();
    stream.closeWrite(); // idempotent

    const finFrames = ws.sentFrames.filter(
      ({ frame }) => frame!.type === FrameType.Data && (frame!.flags & FLAG_FIN) !== 0,
    );
    expect(finFrames.length).toBe(1);
  });

  it('read throws after receiving FIN from peer', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    // Peer sends FIN
    ws.triggerMessage(buildDataFrame(stream.streamId, FLAG_FIN));

    await expect(stream.read(100)).rejects.toThrow(YamuxStreamClosedError);
  });

  it('read returns buffered data then throws after FIN', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    const payload = new Uint8Array([7, 8, 9]);
    ws.triggerMessage(buildDataFrame(stream.streamId, 0, payload));
    ws.triggerMessage(buildDataFrame(stream.streamId, FLAG_FIN));

    const received = await stream.read(100);
    expect(received).toEqual(payload);

    await expect(stream.read(100)).rejects.toThrow(YamuxStreamClosedError);
  });
});

// ─── Tests: WindowUpdate flow control ────────────────────────────────────────

describe('TestWindowUpdateBlocksWriteUntilAdvance', () => {
  it('blocks write when send window is exhausted, unblocks on WindowUpdate', async () => {
    const { session, ws } = makeSession();

    // We need to exhaust the send window.  The default is 256 KiB which is
    // large, so we directly poke the stream state via a controlled write
    // sequence.  Instead of allocating 256 KiB we test the blocking mechanism
    // with a zero-window stream.
    const stream = await session.openStream();

    // Drain the send window by accessing the internal state
    // We'll use the public interface: write DEFAULT_WINDOW bytes minus a small
    // amount so the window is nearly exhausted, then write one more chunk.
    // To keep the test fast, we'll zero out the window by injecting a tiny
    // window via a mock approach.

    // Easiest: write a small write and use a mock stream state instead.
    // We test the window-blocking behaviour by temporarily zeroing the window
    // on the stream state via reflection.

    // Access internal streams map via the session handle.
    // Since TypeScript private fields aren't truly private in Jest/JS:
    const sessionAny = session as any;
    const stateMap: Map<number, any> = sessionAny.streams;
    const state = stateMap.get(stream.streamId)!;
    expect(state).toBeDefined();

    // Zero out the send window to force blocking
    state.sendWindow = 0;

    let writeResolved = false;
    const writePromise = stream.write(new Uint8Array([42])).then(() => {
      writeResolved = true;
    });

    // Give the microtask queue a turn — write should still be blocked
    await new Promise((r) => setTimeout(r, 10));
    expect(writeResolved).toBe(false);

    // Deliver a WindowUpdate from the peer to unblock
    ws.triggerMessage(buildWindowUpdateFrame(stream.streamId, 1024));

    await writePromise;
    expect(writeResolved).toBe(true);
  });

  it('processes WindowUpdate frame and adds delta to send window', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    const sessionAny = session as any;
    const state = sessionAny.streams.get(stream.streamId)!;
    const before = state.sendWindow as number;

    ws.triggerMessage(buildWindowUpdateFrame(stream.streamId, 4096));

    // Allow the onmessage handler to process
    await new Promise((r) => setTimeout(r, 0));

    expect(state.sendWindow).toBe(before + 4096);
  });
});

// ─── Tests: Ping keepalive (Bug F8 fix) ──────────────────────────────────────

describe('TestPingKeepalive', () => {
  it('responds to Ping SYN with Ping ACK carrying the same opaque length value, no payload bytes', async () => {
    const { session, ws } = makeSession();
    ws.resetSent();

    const opaqueValue = 0xdeadbeef;
    // Ping SYN has no payload bytes — just a 12-byte header with opaque length.
    ws.triggerMessage(buildPingFrame(FLAG_SYN, opaqueValue));

    await new Promise((r) => setTimeout(r, 0));

    const pongFrames = ws.sentFrames.filter(
      ({ frame }) => frame!.type === FrameType.Ping && (frame!.flags & FLAG_ACK) !== 0,
    );
    expect(pongFrames.length).toBe(1);

    // The ACK must echo the same opaque value in the Length field.
    expect(pongFrames[0].frame!.length).toBe(opaqueValue);

    // No payload bytes — the ACK frame is exactly HEADER_SIZE bytes.
    expect(pongFrames[0].payload.length).toBe(0);
  });

  it('does not send an ACK for a Ping ACK (response to our own ping)', async () => {
    const { session, ws } = makeSession();
    ws.resetSent();

    // A Ping with ACK flag only (peer responding to our ping) — should not trigger another ACK.
    ws.triggerMessage(buildPingFrame(FLAG_ACK, 42));

    await new Promise((r) => setTimeout(r, 0));

    const pongFrames = ws.sentFrames.filter(
      ({ frame }) => frame!.type === FrameType.Ping && (frame!.flags & FLAG_ACK) !== 0,
    );
    expect(pongFrames.length).toBe(0);
  });

  it('Data frame after Ping SYN is parsed correctly (no byte alignment issue)', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();
    ws.resetSent();

    const opaqueValue = 12345;
    // Deliver Ping SYN (12 bytes, no payload) immediately followed by a Data frame.
    const pingFrame = buildPingFrame(FLAG_SYN, opaqueValue);
    const dataPayload = new TextEncoder().encode('after-ping');
    const dataFrame = buildDataFrame(stream.streamId, 0, dataPayload);

    // Deliver both in the same WebSocket message to test the recvBuf parser.
    const combined = new Uint8Array(pingFrame.length + dataFrame.length);
    combined.set(pingFrame, 0);
    combined.set(dataFrame, pingFrame.length);
    ws.triggerMessage(combined);

    await new Promise((r) => setTimeout(r, 0));

    // The Data frame must have been parsed and queued correctly.
    const received = await stream.read(100);
    expect(received).toEqual(dataPayload);
  });
});

// ─── Tests: RST handling ──────────────────────────────────────────────────────

describe('RST handling', () => {
  it('closes read queue with YamuxStreamResetError on RST frame', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    ws.triggerMessage(buildDataFrame(stream.streamId, FLAG_RST));

    await expect(stream.read(100)).rejects.toThrow(YamuxStreamResetError);
  });
});

// ─── Tests: session close ─────────────────────────────────────────────────────

describe('Session close', () => {
  it('sends GoAway frame on session.close()', async () => {
    const { session, ws } = makeSession();
    ws.resetSent();
    session.close();

    const goAwayFrames = ws.sentFrames.filter(
      ({ frame }) => frame!.type === FrameType.GoAway,
    );
    expect(goAwayFrames.length).toBe(1);
  });

  it('closes all stream read queues when session closes', async () => {
    const { session, ws } = makeSession();
    const stream = await session.openStream();

    session.close();

    await expect(stream.read(100)).rejects.toThrow();
  });
});

// ─── Tests: encodeFrame / decodeFrame ────────────────────────────────────────

describe('Frame codec', () => {
  it('round-trips a Data frame header', () => {
    const hdr = encodeFrame(FrameType.Data, FLAG_SYN | FLAG_ACK, 42, 1024);
    const decoded = decodeFrame(hdr)!;
    expect(decoded.version).toBe(1);
    expect(decoded.type).toBe(FrameType.Data);
    expect(decoded.flags).toBe(FLAG_SYN | FLAG_ACK);
    expect(decoded.streamId).toBe(42);
    expect(decoded.length).toBe(1024);
  });

  it('round-trips a WindowUpdate frame header', () => {
    const hdr = encodeFrame(FrameType.WindowUpdate, 0, 7, 65536);
    const decoded = decodeFrame(hdr)!;
    expect(decoded.type).toBe(FrameType.WindowUpdate);
    expect(decoded.streamId).toBe(7);
    expect(decoded.length).toBe(65536);
  });

  it('returns null for undersized buffer', () => {
    expect(decodeFrame(new Uint8Array(8))).toBeNull();
  });
});
