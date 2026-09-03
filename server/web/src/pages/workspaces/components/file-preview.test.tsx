import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { server } from '@/mocks/server-node';
import { FilePreview } from './file-preview';

const FILE_CONTENT = '/api/workspaces/:id/file-content';

function serveFile(body: string) {
  server.use(http.get(FILE_CONTENT, () => HttpResponse.text(body)));
}

describe('FilePreview video support', () => {
  it('renders a native <video controls> for mp4', () => {
    const { container } = render(<FilePreview workspaceId="1" path="clip.mp4" />);
    const video = container.querySelector('video');
    expect(video).not.toBeNull();
    expect(video?.getAttribute('src')).toContain('path=clip.mp4');
    expect(video?.hasAttribute('controls')).toBe(true);
  });

  it('renders a native <video> for webm and ogv/mov containers', () => {
    for (const path of ['clip.webm', 'clip.ogv', 'clip.mov']) {
      const { container } = render(<FilePreview workspaceId="1" path={path} />);
      expect(container.querySelector('video')).not.toBeNull();
    }
  });

  it('mounts a <video> for HLS (.m3u8) streams', () => {
    const { container } = render(<FilePreview workspaceId="1" path="stream.m3u8" />);
    // The player element mounts synchronously; the demuxer is lazy-loaded after.
    expect(container.querySelector('video')).not.toBeNull();
  });

  it('falls back to a download link for unsupported binaries', () => {
    const { container } = render(<FilePreview workspaceId="1" path="archive.zip" />);
    expect(container.querySelector('video')).toBeNull();
    const link = container.querySelector('a[download]');
    expect(link).not.toBeNull();
    expect(link?.getAttribute('href')).toContain('path=archive.zip');
  });
});

describe('FilePreview — image', () => {
  it('renders an <img> over the transparency checkerboard for svg/png', () => {
    for (const path of ['diagram.svg', 'chart.png']) {
      const { container } = render(<FilePreview workspaceId="1" path={path} />);
      const img = container.querySelector('img');
      expect(img).not.toBeNull();
      expect(img?.getAttribute('src')).toContain(`path=${path}`);
      // The checkerboard backing keeps a transparent SVG/PNG readable in both themes.
      expect(container.querySelector('.preview-checkerboard')).not.toBeNull();
    }
  });

  it('toggles fit ↔ actual size on click for large images', () => {
    const { container } = render(<FilePreview workspaceId="1" path="big.png" />);
    const img = container.querySelector('img');
    expect(img).not.toBeNull();
    // Starts fitted to the pane.
    expect(img?.className).toContain('cursor-zoom-in');
    fireEvent.click(img!);
    // After zoom it renders at actual size and the container scrolls.
    expect(img?.className).toContain('cursor-zoom-out');
    expect(img?.className).toContain('max-w-none');
  });
});

describe('FilePreview — text/code', () => {
  it('applies design-token syntax highlighting to a code file', async () => {
    serveFile('const greeting = "hi";');
    const { container } = render(<FilePreview workspaceId="ws1" path="a.ts" />);

    // Keyword + string get wrapped in the shared --syntax-* token classes.
    // Tokenization is asynchronous (worker in the browser, the same tokenizer
    // inline under jsdom), so the classes appear after the grammar loads rather
    // than on the first paint.
    await screen.findByText('const', undefined, { timeout: 15_000 });
    await waitFor(
      () => {
        expect(container.querySelector('.text-syntax-keyword')).not.toBeNull();
        expect(container.querySelector('.text-syntax-string')).not.toBeNull();
      },
      { timeout: 15_000 },
    );
  }, 20_000);

  it('renders a line-number gutter for code', async () => {
    serveFile('line one\nline two');
    render(<FilePreview workspaceId="ws1" path="a.go" />);
    // Gutter cells show 1-based line numbers.
    expect(await screen.findByText('1')).toBeInTheDocument();
    expect(screen.getByText('2')).toBeInTheDocument();
  });

  it('does not highlight plain-data text (.txt) — prose keywords stay plain', async () => {
    serveFile('const is a normal english word here');
    const { container } = render(<FilePreview workspaceId="ws1" path="notes.txt" />);
    await screen.findByText(/normal english word/);
    expect(container.querySelector('.text-syntax-keyword')).toBeNull();
  });

  // Inverted from the assertion this replaced, which asserted the "highlighting
  // disabled for a large file" notice appears. That notice is gone: the code
  // view windows its rows, so a big file mounts and tokenizes only the visible
  // slice and keeps its highlighting. The old cutoff was backwards — past 5000
  // lines you lost readability AND still paid for every DOM node.
  it('keeps highlighting an oversized code file, and windows it', async () => {
    const big = Array.from({ length: 5001 }, () => 'const x = 1').join('\n');
    serveFile(big);
    const { container } = render(<FilePreview workspaceId="ws1" path="big.ts" />);

    await screen.findAllByText('const');
    expect(container.querySelector('.text-syntax-keyword')).not.toBeNull();
    expect(screen.queryByText(/已关闭语法高亮/)).not.toBeInTheDocument();

    const mounted = container.querySelectorAll('[data-code-line]').length;
    expect(mounted).toBeGreaterThan(0);
    expect(mounted).toBeLessThan(200);
  });
});
