/**
 * Pure helpers for the draw.io embed postMessage protocol (embed.diagrams.net
 * with `embed=1&proto=json`). Kept free of DOM/iframe wiring so the fiddly bits
 * — message parsing, data-URI decoding, empty-diagram detection — are unit
 * testable. The thin iframe glue lives in `drawio-canvas.tsx`.
 *
 * Protocol reference: https://www.drawio.com/doc/faq/embed-mode
 */

/**
 * draw.io posts each message as a JSON *string*; it also emits a few bare
 * non-JSON pings (e.g. the legacy `"ready"`). Parse defensively: return the
 * decoded object, or null for anything that isn't a JSON object string.
 */
export function parseDrawioMessage(raw: unknown): Record<string, unknown> | null {
  if (typeof raw !== 'string') return null;
  const trimmed = raw.trim();
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return null;
  try {
    const msg = JSON.parse(trimmed);
    return msg && typeof msg === 'object' ? (msg as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

/**
 * Decode a `data:<mime>;base64,<payload>` URI (as returned in the `export`
 * event's `data` field) into a Blob. Throws on a non-data-URI input.
 */
export function dataUriToBlob(dataUri: string): Blob {
  if (!dataUri.startsWith('data:')) {
    throw new Error('not a data URI');
  }
  const comma = dataUri.indexOf(',');
  if (comma < 0) throw new Error('malformed data URI');
  const header = dataUri.slice('data:'.length, comma);
  const isBase64 = /;base64/i.test(header);
  const mime = header.split(';')[0] || 'image/png';
  const payload = dataUri.slice(comma + 1);
  const binary = isBase64 ? atob(payload) : decodeURIComponent(payload);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return new Blob([bytes], { type: mime });
}

/**
 * A blank draw.io diagram carries only the two default root cells (`id="0"` and
 * `id="1"`) and no shapes. Treat that as "nothing to send" so the panel can
 * short-circuit like the Excalidraw one does on an empty scene.
 *
 * Conservative by design: we only declare "empty" when we can positively read
 * an inline `<mxGraphModel>` that has no vertex/edge/object. If the XML is
 * missing or opaque (e.g. a compressed `<diagram>` blob), we return false so a
 * real diagram is never silently suppressed. We force uncompressed XML at load
 * time (compressXml:false), so in practice the model is always inline.
 */
export function isEmptyDiagram(xml: string | undefined): boolean {
  if (!xml) return true;
  // No readable model (opaque/compressed) — can't confirm empty; don't suppress.
  if (!/<mxGraphModel[\s>]/.test(xml)) return false;
  if (/(?:vertex|edge)\s*=\s*"1"/.test(xml)) return false;
  if (/<(?:object|UserObject)\b/.test(xml)) return false;
  return true;
}
