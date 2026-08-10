// Per-block pin/anchor keys for chat-flow messages.
//
// The persisted `message_id` GROUPS every block of one assistant turn — when
// the agent emits text, runs a tool, then emits more text, all those rendered
// blocks share one messageId. Keying a pin (or the `msg-<id>` DOM anchor) off
// the raw messageId would therefore pin/highlight every sibling block of the
// turn ("pin range too large").
//
// deriveBlockKeys assigns each event a stable per-block key: the FIRST block of
// a given messageId keeps the bare messageId (so task-locate by messageId and
// any pre-existing pins still resolve to it), and later blocks get
// `messageId#N`. The numbering is purely order-derived, so it reproduces
// identically for a live stream and for the same messages reloaded from the DB.

interface BlockKeyEvent {
  id: string;
  messageId: string;
}

/** key for the Nth (0-based) block of a messageId. */
export function blockKeyFor(messageId: string, ordinal: number): string {
  return ordinal === 0 ? messageId : `${messageId}#${ordinal}`;
}

/** Map every event id to its stable per-block key, walking events in order. */
export function deriveBlockKeys(events: readonly BlockKeyEvent[]): Map<string, string> {
  const map = new Map<string, string>();
  const seen = new Map<string, number>();
  for (const e of events) {
    const n = seen.get(e.messageId) ?? 0;
    seen.set(e.messageId, n + 1);
    map.set(e.id, blockKeyFor(e.messageId, n));
  }
  return map;
}
