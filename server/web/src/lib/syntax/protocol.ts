/**
 * Message protocol between the main thread and the highlight worker.
 *
 * Kept in its own module so both ends import the same types and neither pulls
 * in the other's runtime — the worker must not reach React, and the client must
 * not reach shiki (that is the entire point of moving tokenization off-thread).
 */

import type { SyntaxLine } from './tokenizer';

/** Highlight a whole file. Supersedes any earlier request with the same `id`. */
export interface HighlightRequest {
  type: 'highlight';
  /** Correlates responses; the client uses one id per file surface. */
  id: number;
  /** Shiki language id, already resolved from the path by `languageForPath`. */
  lang: string;
  /** Full file text, LF-normalized. */
  code: string;
}

/** Stop work for `id` — the surface unmounted or switched files. */
export interface CancelRequest {
  type: 'cancel';
  id: number;
}

export type WorkerRequest = HighlightRequest | CancelRequest;

/**
 * One tokenized slice, delivered as it is produced.
 *
 * Progressive rather than one final payload: the first chunk covers the top of
 * the file, so the visible lines get their colors long before a large file
 * finishes. `from` is the 0-based index of `lines[0]` within the file.
 */
export interface ChunkResponse {
  type: 'chunk';
  id: number;
  from: number;
  lines: SyntaxLine[];
  /** Set on the last chunk of the file. */
  done: boolean;
}

/**
 * Tokenization failed outright (grammar load error, malformed input). The
 * client keeps showing plain text — a highlighting failure must never blank
 * out the code itself.
 */
export interface ErrorResponse {
  type: 'error';
  id: number;
  message: string;
}

export type WorkerResponse = ChunkResponse | ErrorResponse;

/**
 * Lines per tokenization slice.
 *
 * At the measured ~155µs/line this is ~75ms of work per chunk: long enough to
 * amortize the postMessage round trip, short enough that a cancel (user scrolls
 * to another file) is honored promptly. It also bounds how long the worker can
 * ignore its own message queue, since tokenization itself cannot be interrupted.
 */
export const CHUNK_LINES = 500;
