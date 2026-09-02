import { CHUNK_LINES, type WorkerResponse } from './protocol';
import type { SyntaxLine } from './tokenizer';

/**
 * Main-thread client for the highlight worker.
 *
 * Owns the single worker instance, fans responses out to the right subscriber,
 * and — when `Worker` is unavailable (jsdom under vitest, or an embedding that
 * blocks workers) — runs the identical tokenizer inline so callers never have
 * to branch on it. The inline path is slower but produces the same tokens, so
 * tests exercise real shiki output rather than a stub.
 */

export interface HighlightHandlers {
  /** A slice of tokenized lines, starting at file line index `from`. */
  onChunk(from: number, lines: SyntaxLine[], done: boolean): void;
  /** Tokenization failed; the caller keeps rendering plain text. */
  onError(message: string): void;
}

let worker: Worker | null = null;
/** `null` until probed, then `false` if workers are unusable in this context. */
let workerUsable: boolean | null = null;

const handlers = new Map<number, HighlightHandlers>();
let nextId = 1;

/** A fresh subscription id. One per code surface, reused across its lifetime. */
export function nextHighlightId(): number {
  return nextId++;
}

function getWorker(): Worker | null {
  if (workerUsable === false) return null;
  if (worker) return worker;

  if (typeof Worker === 'undefined') {
    workerUsable = false;
    return null;
  }

  try {
    worker = new Worker(new URL('./highlight.worker.ts', import.meta.url), {
      type: 'module',
    });
    worker.addEventListener('message', (event: MessageEvent<WorkerResponse>) => {
      const msg = event.data;
      const handler = handlers.get(msg.id);
      if (!handler) return;
      if (msg.type === 'chunk') handler.onChunk(msg.from, msg.lines, msg.done);
      else handler.onError(msg.message);
    });
    worker.addEventListener('error', (event) => {
      // A worker-level failure kills every in-flight request, so tell all of
      // them rather than letting their surfaces wait forever.
      for (const handler of handlers.values()) handler.onError(event.message || 'worker error');
    });
    workerUsable = true;
    return worker;
  } catch {
    workerUsable = false;
    return null;
  }
}

/**
 * Tokenize `code` inline, chunk by chunk, yielding to the event loop between
 * chunks so a large file still cannot lock the UI.
 *
 * `isCancelled` is re-checked at every await point: without a worker to
 * discard, this loop is the only thing that can stop itself.
 */
async function runInline(
  lang: string,
  code: string,
  h: HighlightHandlers,
  isCancelled: () => boolean,
) {
  // Imported lazily so the shiki runtime stays out of the main bundle on the
  // normal (worker) path.
  const { ensureLanguage, tokenizeChunk } = await import('./tokenizer');
  if (isCancelled()) return;

  const resolved = await ensureLanguage(lang);
  if (isCancelled()) return;

  const lines = code.split('\n');
  let state: unknown;

  for (let from = 0; from < lines.length; from += CHUNK_LINES) {
    if (isCancelled()) return;
    const slice = lines.slice(from, from + CHUNK_LINES);
    const result = await tokenizeChunk(slice.join('\n'), resolved, state);
    state = result.state;
    if (isCancelled()) return;
    h.onChunk(from, result.lines, from + CHUNK_LINES >= lines.length);
  }
}

/**
 * Start highlighting a file. Returns a cancel function; calling it stops
 * delivery and, on the worker path, tells the worker to abandon the file.
 */
export function requestHighlight(
  id: number,
  lang: string,
  code: string,
  h: HighlightHandlers,
): () => void {
  handlers.set(id, h);

  const w = getWorker();
  if (w) {
    w.postMessage({ type: 'highlight', id, lang, code });
    return () => {
      handlers.delete(id);
      w.postMessage({ type: 'cancel', id });
    };
  }

  let cancelled = false;
  void runInline(lang, code, h, () => cancelled || !handlers.has(id)).catch((err) => {
    if (!cancelled) h.onError(err instanceof Error ? err.message : String(err));
  });
  return () => {
    cancelled = true;
    handlers.delete(id);
  };
}
