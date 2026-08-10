import { describe, it, expect } from 'vitest';
import { parseDrawioMessage, dataUriToBlob, isEmptyDiagram } from './drawio-protocol';

describe('parseDrawioMessage', () => {
  it('parses a JSON object message from the iframe', () => {
    const msg = parseDrawioMessage(JSON.stringify({ event: 'init' }));
    expect(msg).toEqual({ event: 'init' });
  });

  it('ignores non-JSON pings like the bare "ready" string', () => {
    expect(parseDrawioMessage('ready')).toBeNull();
    expect(parseDrawioMessage('')).toBeNull();
  });

  it('ignores non-string payloads and malformed JSON', () => {
    expect(parseDrawioMessage(42)).toBeNull();
    expect(parseDrawioMessage({ event: 'init' })).toBeNull();
    expect(parseDrawioMessage('{oops')).toBeNull();
  });
});

describe('dataUriToBlob', () => {
  it('decodes a base64 PNG data URI into a Blob with the right mime + bytes', async () => {
    // 1x1 transparent PNG.
    const b64 =
      'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC';
    const blob = dataUriToBlob(`data:image/png;base64,${b64}`);
    expect(blob.type).toBe('image/png');
    const bytes = new Uint8Array(await blob.arrayBuffer());
    // PNG magic number.
    expect(Array.from(bytes.slice(0, 4))).toEqual([0x89, 0x50, 0x4e, 0x47]);
  });

  it('throws on a payload that is not a data URI', () => {
    expect(() => dataUriToBlob('not-a-data-uri')).toThrow();
  });
});

describe('isEmptyDiagram', () => {
  it('treats undefined / empty xml as empty', () => {
    expect(isEmptyDiagram(undefined)).toBe(true);
    expect(isEmptyDiagram('')).toBe(true);
  });

  it('treats a diagram with only the two default root cells as empty', () => {
    const xml =
      '<mxfile><diagram><mxGraphModel><root>' +
      '<mxCell id="0"/><mxCell id="1" parent="0"/>' +
      '</root></mxGraphModel></diagram></mxfile>';
    expect(isEmptyDiagram(xml)).toBe(true);
  });

  it('treats a diagram containing a vertex as non-empty', () => {
    const xml =
      '<mxfile><diagram><mxGraphModel><root>' +
      '<mxCell id="0"/><mxCell id="1" parent="0"/>' +
      '<mxCell id="2" value="Box" vertex="1" parent="1"><mxGeometry/></mxCell>' +
      '</root></mxGraphModel></diagram></mxfile>';
    expect(isEmptyDiagram(xml)).toBe(false);
  });

  it('treats a diagram containing an edge as non-empty', () => {
    const xml =
      '<mxGraphModel><root><mxCell id="3" edge="1" parent="1"/></root></mxGraphModel>';
    expect(isEmptyDiagram(xml)).toBe(false);
  });

  it('treats an embedded object/UserObject node as non-empty', () => {
    expect(
      isEmptyDiagram('<mxGraphModel><root><object id="4" label="x"/></root></mxGraphModel>'),
    ).toBe(false);
    expect(
      isEmptyDiagram('<mxGraphModel><root><UserObject id="5"/></root></mxGraphModel>'),
    ).toBe(false);
  });

  it('does not suppress an opaque/compressed diagram (no inline model)', () => {
    // Compressed .drawio stores a deflated base64 blob with no readable model;
    // we must err toward sending it rather than wrongly calling it empty.
    const compressed = '<mxfile><diagram>7VvJkqM4EP0aH6cDkFg';
    expect(isEmptyDiagram(compressed)).toBe(false);
  });
});
