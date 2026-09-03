import type { WorkerResponse } from './protocol';
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
 * Tokenize inline, yielding to the event loop between chunks so a large file
 * still cannot lock the UI.
 *
 * Delegates the actual segmentation to `tokenizeDocument`, the same function
 * the worker runs — so the fallback cannot drift from the primary path.
 */
async function runInline(
  lang: string,
  code: string,
  resets: number[] | undefined,
  h: HighlightHandlers,
  isCancelled: () => boolean,
) {
  // Imported lazily so the shiki runtime stays out of the main bundle on the
  // normal (worker) path.
  const { ensureLanguage, tokenizeDocument } = await import('./tokenizer');
  if (isCancelled()) return;

  const resolved = await ensureLanguage(lang);
  if (isCancelled()) return;

  await tokenizeDocument(code, resolved, resets, h.onChunk, isCancelled);
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
  resets?: number[],
): () => void {
  handlers.set(id, h);

  // Only retract *this* subscription. Ids are reused across a surface's
  // lifetime, so a late cleanup must not evict a handler that a newer request
  // has already installed under the same id.
  const release = () => {
    if (handlers.get(id) === h) handlers.delete(id);
  };

  const w = getWorker();
  if (w) {
    w.postMessage({ type: 'highlight', id, lang, code, resets });
    return () => {
      release();
      w.postMessage({ type: 'cancel', id });
    };
  }

  let cancelled = false;
  // Superseded counts as cancelled: if a newer request took over this id, the
  // old loop must stop rather than keep pushing chunks of the previous file
  // into a handler whose component is gone.
  void runInline(lang, code, resets, h, () => cancelled || handlers.get(id) !== h).catch((err) => {
    if (!cancelled) h.onError(err instanceof Error ? err.message : String(err));
  });
  return () => {
    cancelled = true;
    release();
  };
}
