import { describe, it, expect } from 'vitest';
import { DRAWIO_EMBED_SRC } from './drawio-embed';

describe('DRAWIO_EMBED_SRC', () => {
  it('is a same-origin relative path under /drawio/ (never a remote host)', () => {
    expect(DRAWIO_EMBED_SRC.startsWith('/drawio/drawio.html')).toBe(true);
    expect(DRAWIO_EMBED_SRC).not.toContain('diagrams.net');
    expect(DRAWIO_EMBED_SRC).not.toMatch(/^https?:\/\//);
    expect(DRAWIO_EMBED_SRC).not.toContain('//'); // no protocol-relative //host
  });

  it('does NOT use an index.html entry (embedded Go server 301-redirects */index.html → ./, stranding the iframe on the SPA "Not Found")', () => {
    expect(DRAWIO_EMBED_SRC).not.toContain('index.html');
  });

  it('forces fully-local operation via offline + stealth flags', () => {
    expect(DRAWIO_EMBED_SRC).toContain('offline=1');
    expect(DRAWIO_EMBED_SRC).toContain('stealth=1');
  });

  it('keeps the JSON embed protocol + diffable-XML configure handshake', () => {
    expect(DRAWIO_EMBED_SRC).toContain('embed=1');
    expect(DRAWIO_EMBED_SRC).toContain('proto=json');
    expect(DRAWIO_EMBED_SRC).toContain('configure=1');
  });
});
