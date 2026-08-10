/**
 * Minimal yamux client for running over a WebSocket. Only implements the
 * subset needed to open a client stream, exchange bytes, and close.
 *
 * Wire format (yamux spec v0.1):
 *   Byte  0:     Version (1)
 *   Byte  1:     Type (0=Data, 1=WindowUpdate, 2=Ping, 3=GoAway)
 *   Bytes 2-3:   Flags (uint16 BE): SYN=0x1, ACK=0x2, FIN=0x4, RST=0x8
 *   Bytes 4-7:   StreamID (uint32 BE)
 *   Bytes 8-11:  Length (uint32 BE) — payload length for Data; delta for WindowUpdate
 *
 * Default initial window: 256 KiB per stream.  Each side sends WindowUpdate
 * to advance the peer's write quota.
 *
 * Limitation: one-stream-per-tunnel is the expected usage pattern for LAN RPC.
 * Multi-stream routing is supported (Map<streamId, StreamState>) but not
 * stress-tested for high concurrency.
 */

export enum FrameType {
  Data = 0,
  WindowUpdate = 1,
  Ping = 2,
  GoAway = 3,
}

export const FLAG_SYN = 0x1;
export const FLAG_ACK = 0x2;
export const FLAG_FIN = 0x4;
export const FLAG_RST = 0x8;

export const HEADER_SIZE = 12;
export const DEFAULT_WINDOW = 256 * 1024;

// ─── Errors ────────────────────────────────────────────────────────────────────

export class YamuxStreamClosedError extends Error {
  constructor(msg = 'yamux stream closed') {
    super(msg);
    this.name = 'YamuxStreamClosedError';
  }
}

export class YamuxStreamResetError extends Error {
  constructor(msg = 'yamux stream reset by peer') {
    super(msg);
    this.name = 'YamuxStreamResetError';
  }
}

// ─── Frame encoding / decoding ─────────────────────────────────────────────────

export interface YamuxFrame {
  version: number;
  type: FrameType;
  flags: number;
  streamId: number;
  length: number;
}

export function encodeFrame(
  type: FrameType,
  flags: number,
  streamId: number,
  length: number,
): Uint8Array {
  const hdr = new Uint8Array(HEADER_SIZE);
  const view = new DataView(hdr.buffer);
  hdr[0] = 1; // version
  hdr[1] = type;
  view.setUint16(2, flags, false);
  view.setUint32(4, streamId, false);
  view.setUint32(8, length, false);
  return hdr;
}

export function decodeFrame(buf: Uint8Array): YamuxFrame | null {
  if (buf.length < HEADER_SIZE) return null;
  const view = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
  return {
    version: buf[0],
    type: buf[1] as FrameType,
    flags: view.getUint16(2, false),
    streamId: view.getUint32(4, false),
    length: view.getUint32(8, false),
  };
}

// ─── Async queue ──────────────────────────────────────────────────────────────

/**
 * Simple single-producer / single-consumer async queue backed by a buffer and
 * a pending-resolve list.  Used for per-stream received-data buffering.
 */
class AsyncQueue<T> {
  private buf: T[] = [];
  private waiting: Array<{ resolve: (v: PromiseLike<T> | T) => void; reject: (e: Error) => void }> = [];
  private closed = false;
  private closeError: Error | null = null;

  push(item: T): void {
    if (this.waiting.length > 0) {
      const { resolve } = this.waiting.shift()!;
      resolve(item as PromiseLike<T> | T);
    } else {
      this.buf.push(item);
    }
  }

  /** Signals EOF or error to any pending readers and prevents future pushes. */
  close(err?: Error): void {
    if (this.closed) return;
    this.closed = true;
    this.closeError = err ?? new YamuxStreamClosedError();
    for (const { reject } of this.waiting) {
      reject(this.closeError!);
    }
    this.waiting = [];
  }

  /** Returns next item or throws if closed and buffer is drained. */
  async next(timeoutMs?: number): Promise<T> {
    if (this.buf.length > 0) {
      return this.buf.shift()!;
    }
    if (this.closed) {
      throw this.closeError!;
    }
    return new Promise<T>((resolve, reject) => {
      const entry = { resolve, reject };
      this.waiting.push(entry);

      if (timeoutMs !== undefined) {
        const timer = setTimeout(() => {
          const idx = this.waiting.indexOf(entry);
          if (idx !== -1) this.waiting.splice(idx, 1);
          reject(new Error(`yamux read timeout after ${timeoutMs}ms`));
        }, timeoutMs);
        // Wrap resolve/reject to also clear the timer
        entry.resolve = (v: PromiseLike<T> | T) => {
          clearTimeout(timer);
          resolve(v as T);
        };
        entry.reject = (e: Error) => {
          clearTimeout(timer);
          reject(e);
        };
      }
    });
  }

  get isEmpty(): boolean {
    return this.buf.length === 0;
  }

  get isClosed(): boolean {
    return this.closed;
  }
}

// ─── Stream state ─────────────────────────────────────────────────────────────

interface StreamState {
  streamId: number;
  readQueue: AsyncQueue<Uint8Array>;
  /** How many bytes we are allowed to send to the peer. */
  sendWindow: number;
  /** Waiters blocked on a full send window. */
  windowWaiters: Array<() => void>;
  finSent: boolean;
  rstSent: boolean;
}

// ─── YamuxStream (public handle) ──────────────────────────────────────────────

export interface YamuxStream {
  readonly streamId: number;
  write(data: Uint8Array): Promise<void>;
  /** Reads next available chunk.  Throws YamuxStreamClosedError on EOF. */
  read(timeoutMs?: number): Promise<Uint8Array>;
  /** Sends FIN — signals end of outbound data. */
  closeWrite(): void;
  /** Fully closes the stream (sends RST if write side still open). */
  close(): void;
}

class YamuxStreamHandle implements YamuxStream {
  constructor(
    private state: StreamState,
    private session: YamuxSession,
  ) {}

  get streamId(): number {
    return this.state.streamId;
  }

  async write(data: Uint8Array): Promise<void> {
    return this.session._streamWrite(this.state, data);
  }

  read(timeoutMs?: number): Promise<Uint8Array> {
    return this.state.readQueue.next(timeoutMs);
  }

  closeWrite(): void {
    this.session._streamCloseWrite(this.state);
  }

  close(): void {
    this.session._streamClose(this.state);
  }
}

// ─── Frame accumulator ────────────────────────────────────────────────────────
// WebSocket may deliver data as multiple messages or (less commonly) split
// across messages.  We treat each onmessage as one complete yamux frame
// (header + payload in a single WS message), which matches how both yamux-go
// and the Go server flush frames — each `ws.WriteMessage` is one frame.

/**
 * YamuxSession manages the yamux framing layer over a WebSocket connection.
 *
 * Client streams use odd IDs (1, 3, 5, …).
 */
export class YamuxSession {
  private nextStreamId = 1;
  private streams = new Map<number, StreamState>();
  private closed = false;
  // Accumulate incoming bytes in case the WS delivers partial frames.
  private recvBuf: Uint8Array = new Uint8Array(0);

  constructor(private ws: WebSocket) {
    this.ws.binaryType = 'arraybuffer';
    this.ws.onmessage = (e) => this._onData(new Uint8Array(e.data as ArrayBuffer));
    this.ws.onclose = () => this._onWsClose();
    this.ws.onerror = () => this._onWsClose();
  }

  // ─── Public API ───────────────────────────────────────────────────────────

  /**
   * Opens a new client-initiated stream.  Sends a SYN frame and returns a
   * stream handle.  We start writing optimistically without waiting for ACK.
   */
  openStream(): Promise<YamuxStream> {
    if (this.closed) {
      return Promise.reject(new Error('yamux session is closed'));
    }
    const id = this.nextStreamId;
    this.nextStreamId += 2; // clients use odd IDs

    const state: StreamState = {
      streamId: id,
      readQueue: new AsyncQueue<Uint8Array>(),
      sendWindow: DEFAULT_WINDOW,
      windowWaiters: [],
      finSent: false,
      rstSent: false,
    };
    this.streams.set(id, state);

    // SYN frame: Data type, SYN flag, length 0
    const hdr = encodeFrame(FrameType.Data, FLAG_SYN, id, 0);
    this._wsSend(hdr);

    // Immediately advertise our receive window to the peer so it can start
    // sending.  This is the standard yamux client behaviour.
    const wuHdr = encodeFrame(FrameType.WindowUpdate, FLAG_SYN, id, DEFAULT_WINDOW);
    this._wsSend(wuHdr);

    return Promise.resolve(new YamuxStreamHandle(state, this));
  }

  /** Sends a GoAway(0) frame and closes the underlying WebSocket. */
  close(): void {
    if (this.closed) return;
    this.closed = true;
    try {
      const hdr = encodeFrame(FrameType.GoAway, 0, 0, 0);
      this._wsSend(hdr);
    } catch {
      // ignore send errors during close
    }
    try {
      this.ws.close();
    } catch {
      // ignore
    }
    this._closeAllStreams(new YamuxStreamClosedError('session closed'));
  }

  // ─── Internal helpers called by YamuxStreamHandle ─────────────────────────

  async _streamWrite(state: StreamState, data: Uint8Array): Promise<void> {
    if (state.finSent || state.rstSent) {
      throw new YamuxStreamClosedError('write after close');
    }

    let offset = 0;
    while (offset < data.length) {
      // Wait for send window to open
      while (state.sendWindow <= 0) {
        await new Promise<void>((resolve) => {
          state.windowWaiters.push(resolve);
        });
      }

      const chunk = Math.min(data.length - offset, state.sendWindow);
      state.sendWindow -= chunk;

      const hdr = encodeFrame(FrameType.Data, 0, state.streamId, chunk);
      const frame = new Uint8Array(HEADER_SIZE + chunk);
      frame.set(hdr, 0);
      frame.set(data.subarray(offset, offset + chunk), HEADER_SIZE);
      this._wsSend(frame);

      offset += chunk;
    }
  }

  _streamCloseWrite(state: StreamState): void {
    if (state.finSent || state.rstSent) return;
    state.finSent = true;
    const hdr = encodeFrame(FrameType.Data, FLAG_FIN, state.streamId, 0);
    this._wsSend(hdr);
  }

  _streamClose(state: StreamState): void {
    if (!state.finSent && !state.rstSent) {
      state.rstSent = true;
      try {
        const hdr = encodeFrame(FrameType.Data, FLAG_RST, state.streamId, 0);
        this._wsSend(hdr);
      } catch {
        // ignore
      }
    }
    state.readQueue.close();
    this.streams.delete(state.streamId);
  }

  // ─── Incoming frame dispatch ──────────────────────────────────────────────

  private _onData(chunk: Uint8Array): void {
    // Append to receive buffer
    const next = new Uint8Array(this.recvBuf.length + chunk.length);
    next.set(this.recvBuf, 0);
    next.set(chunk, this.recvBuf.length);
    this.recvBuf = next;

    // Process all complete frames
    while (this.recvBuf.length >= HEADER_SIZE) {
      const frame = decodeFrame(this.recvBuf);
      if (!frame) break;

      // Only Data frames carry trailing payload bytes equal to frame.length.
      // Ping, WindowUpdate, and GoAway use the length field as an opaque
      // scalar value — NO payload bytes follow the 12-byte header.
      // (hashicorp/yamux spec §5: Ping Length is "an opaque value".)
      const hasPayload = frame.type === FrameType.Data;
      const totalLen = HEADER_SIZE + (hasPayload ? frame.length : 0);
      if (this.recvBuf.length < totalLen) break; // wait for payload

      const payload =
        hasPayload && frame.length > 0
          ? this.recvBuf.slice(HEADER_SIZE, totalLen)
          : null;

      this.recvBuf = this.recvBuf.slice(totalLen);
      this._dispatchFrame(frame, payload);
    }
  }

  private _dispatchFrame(frame: YamuxFrame, payload: Uint8Array | null): void {
    switch (frame.type) {
      case FrameType.Data:
        this._handleData(frame, payload);
        break;
      case FrameType.WindowUpdate:
        this._handleWindowUpdate(frame);
        break;
      case FrameType.Ping:
        this._handlePing(frame, payload);
        break;
      case FrameType.GoAway:
        this._onWsClose();
        break;
    }
  }

  private _handleData(frame: YamuxFrame, payload: Uint8Array | null): void {
    const state = this.streams.get(frame.streamId);

    if (frame.flags & FLAG_RST) {
      if (state) {
        state.readQueue.close(new YamuxStreamResetError());
        this.streams.delete(frame.streamId);
      }
      return;
    }

    if (!state) {
      // Unknown stream — ignore (could be ACK-only frame on a stream we opened)
      return;
    }

    if (payload && payload.length > 0) {
      state.readQueue.push(new Uint8Array(payload)); // defensive copy already in slice
    }

    if (frame.flags & FLAG_FIN) {
      state.readQueue.close();
    }
  }

  private _handleWindowUpdate(frame: YamuxFrame): void {
    const state = this.streams.get(frame.streamId);
    if (!state) return;

    state.sendWindow += frame.length;

    // Wake all window waiters
    const waiters = state.windowWaiters.splice(0);
    for (const resolve of waiters) {
      resolve();
    }
  }

  private _handlePing(frame: YamuxFrame, _payload: Uint8Array | null): void {
    // In the yamux spec the Ping Length field is an opaque 32-bit value — there
    // are NO payload bytes following the 12-byte header.  The ACK must echo the
    // same opaque value in its own Length field.
    if (frame.flags & FLAG_SYN) {
      // Peer is initiating a keepalive: send Ping + ACK with the same opaque value.
      const pongHdr = encodeFrame(FrameType.Ping, FLAG_ACK, 0, frame.length);
      this._wsSend(pongHdr);
    }
    // If FLAG_ACK is set this is a response to our own Ping — nothing to do.
  }

  private _onWsClose(): void {
    if (this.closed) return;
    this.closed = true;
    this._closeAllStreams(new YamuxStreamClosedError('WebSocket closed'));
  }

  private _closeAllStreams(err: Error): void {
    for (const state of this.streams.values()) {
      state.readQueue.close(err);
      // Wake window waiters so they don't hang
      const waiters = state.windowWaiters.splice(0);
      for (const resolve of waiters) {
        resolve();
      }
    }
    this.streams.clear();
  }

  private _wsSend(data: Uint8Array): void {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(data.buffer instanceof SharedArrayBuffer
        ? data
        : data.buffer.byteLength === data.byteLength
          ? data.buffer
          : data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
    }
  }
}
