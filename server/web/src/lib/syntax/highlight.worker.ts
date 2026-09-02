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
 * Requests superseded or explicitly cancelled are detected between chunks,
 * which is the only place work can stop: a single `codeToTokens` call is
 * atomic.
 */
/**
 * Current generation per request id.
 *
 * A tombstone set would not work here, for two reasons. Ids are reused (the
 * client keeps one per surface for its lifetime), so a cancel would have to be
 * cleared when the next request arrives for that id — and clearing it would
 * also un-cancel the *previous* run, which may still be queued, letting it
 * resume and emit chunks of the old file under the same id. And a set that is
 * only ever added to grows for as long as the tab lives.
 *
 * A generation counter fixes both: every request bumps it, so a superseded run
 * observes a mismatch and stops, and a cancel deletes the entry outright.
 */
const generation = new Map<number, number>();

/**
 * Serializes tokenization. Grammar state is per-file, but the highlighter is a
 * shared singleton and interleaving two files' chunks through it would let one
 * file's mid-construct state leak into the other's.
 */
let queue: Promise<void> = Promise.resolve();

function post(msg: WorkerResponse) {
  ctx.postMessage(msg);
}

/** True once this run has been cancelled or replaced by a newer request. */
const stale = (id: number, gen: number) => generation.get(id) !== gen;

async function run(id: number, gen: number, lang: string, code: string) {
  if (stale(id, gen)) return;

  const resolved = await ensureLanguage(lang);
  if (stale(id, gen)) return;

  const lines = code.split('\n');
  let state: GrammarState | undefined;

  for (let from = 0; from < lines.length; from += CHUNK_LINES) {
    if (stale(id, gen)) return;

    const slice = lines.slice(from, from + CHUNK_LINES);
    const result = await tokenizeChunk(slice.join('\n'), resolved, state);
    state = result.state;

    if (stale(id, gen)) return;
    post({
      type: 'chunk',
      id,
      from,
      lines: result.lines,
      done: from + CHUNK_LINES >= lines.length,
    });
  }

  // Finished cleanly: drop the entry so the map tracks only live work.
  if (generation.get(id) === gen) generation.delete(id);
}

ctx.addEventListener('message', (event: MessageEvent<WorkerRequest>) => {
  const msg = event.data;

  if (msg.type === 'cancel') {
    generation.delete(msg.id);
    return;
  }

  const gen = (generation.get(msg.id) ?? 0) + 1;
  generation.set(msg.id, gen);

  queue = queue
    .then(() => run(msg.id, gen, msg.lang, msg.code))
    .catch((err) => {
      if (stale(msg.id, gen)) return;
      generation.delete(msg.id);
      post({ type: 'error', id: msg.id, message: err instanceof Error ? err.message : String(err) });
    });
});
