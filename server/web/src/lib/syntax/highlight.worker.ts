/// <reference lib="webworker" />

/**
 * The highlight worker.
 *
 * Tokenization is the expensive half of syntax highlighting (~155µs/line
 * measured — 1.7s for an 11k-line file) and it is pure computation over a
 * string, so it belongs off the main thread. What comes back is plain data:
 * `{content, className}` per token, ready for the renderer to wrap in spans.
 *
 * The worker owns each file's `grammarState` and never sends it across the
 * wire. That is not an optimization but a requirement — the state holds live
 * grammar-rule objects and is not structured-cloneable. So the protocol is
 * "send a whole file, receive its chunks", not "send me chunk N".
 */

import { ensureLanguage, tokenizeChunk, type GrammarState } from './tokenizer';
import { CHUNK_LINES, type WorkerRequest, type WorkerResponse } from './protocol';

const ctx = self as unknown as DedicatedWorkerGlobalScope;

/**
 * Requests superseded or explicitly cancelled. Checked between chunks, which
 * is the only place work can stop: a single `codeToTokens` call is atomic.
 */
const cancelled = new Set<number>();

/**
 * Serializes tokenization. Grammar state is per-file, but the highlighter is a
 * shared singleton and interleaving two files' chunks through it would let one
 * file's mid-construct state leak into the other's.
 */
let queue: Promise<void> = Promise.resolve();

function post(msg: WorkerResponse) {
  ctx.postMessage(msg);
}

async function run(id: number, lang: string, code: string) {
  if (cancelled.has(id)) return;

  const resolved = await ensureLanguage(lang);
  if (cancelled.has(id)) return;

  const lines = code.split('\n');
  let state: GrammarState | undefined;

  for (let from = 0; from < lines.length; from += CHUNK_LINES) {
    if (cancelled.has(id)) return;

    const slice = lines.slice(from, from + CHUNK_LINES);
    const result = await tokenizeChunk(slice.join('\n'), resolved, state);
    state = result.state;

    if (cancelled.has(id)) return;
    post({
      type: 'chunk',
      id,
      from,
      lines: result.lines,
      done: from + CHUNK_LINES >= lines.length,
    });
  }
}

ctx.addEventListener('message', (event: MessageEvent<WorkerRequest>) => {
  const msg = event.data;

  if (msg.type === 'cancel') {
    cancelled.add(msg.id);
    return;
  }

  // A new request for an id replaces the old one; clear any stale cancel so the
  // fresh run is not killed by its predecessor's tombstone.
  cancelled.delete(msg.id);

  queue = queue
    .then(() => run(msg.id, msg.lang, msg.code))
    .catch((err) => {
      if (cancelled.has(msg.id)) return;
      post({ type: 'error', id: msg.id, message: err instanceof Error ? err.message : String(err) });
    });
});
