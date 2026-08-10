import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import JSZip from 'jszip';

import { FilePreview } from './file-preview';

// i18n: return the key so assertions are language-agnostic.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}));

vi.mock('@/lib/workspace-file-url', () => ({
  getFileContentUrl: (_id: string, path: string) => `/files/${path}`,
}));

vi.mock('@/components/shared/markdown-message', () => ({
  MarkdownMessage: ({ content }: { content: string }) => <div>{content}</div>,
}));

// The rich renderer is mocked per-test: either a working viewer or a thrower.
const openMock = vi.fn();
vi.mock('@aiden0z/pptx-renderer', () => ({
  PptxViewer: { open: (...args: unknown[]) => openMock(...args) },
  RECOMMENDED_ZIP_LIMITS: {},
}));

function mockFetchReturning(buf: ArrayBuffer | (() => Promise<ArrayBuffer>)) {
  const arrayBuffer = typeof buf === 'function' ? buf : async () => buf;
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, arrayBuffer }));
}

async function makePptxBuffer(texts: string[]): Promise<ArrayBuffer> {
  const zip = new JSZip();
  texts.forEach((txt, i) => {
    zip.file(`ppt/slides/slide${i + 1}.xml`, `<p:sld><a:t>${txt}</a:t></p:sld>`);
  });
  const u8 = await zip.generateAsync({ type: 'uint8array' });
  return u8.buffer as ArrayBuffer;
}

afterEach(() => {
  vi.unstubAllGlobals();
  openMock.mockReset();
});

describe('PptxFilePreview (rich render)', () => {
  beforeEach(() => {
    mockFetchReturning(new ArrayBuffer(8));
  });

  function fakeViewer(slideCount: number) {
    return {
      slideCount,
      slideWidth: 960,
      slideHeight: 540,
      goToSlide: vi.fn().mockResolvedValue(undefined),
      destroy: vi.fn(),
    };
  }

  it('renders slides in a sandboxed iframe with a pager when multi-slide', async () => {
    openMock.mockResolvedValue(fakeViewer(3));
    render(<FilePreview workspaceId="1" path="deck.pptx" />);

    const frame = await screen.findByTitle('artifactPreview.slideFrameTitle');
    expect(frame).toHaveAttribute('sandbox', 'allow-same-origin');
    expect(screen.getByText('1 / 3')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'artifactPreview.nextSlide' })).toBeInTheDocument();
    // one dot per slide
    expect(screen.getAllByRole('button', { name: /artifactPreview\.gotoSlide/ })).toHaveLength(3);
  });

  it('drives the renderer when navigating', async () => {
    const viewer = fakeViewer(3);
    openMock.mockResolvedValue(viewer);
    render(<FilePreview workspaceId="1" path="deck.pptx" />);
    await screen.findByTitle('artifactPreview.slideFrameTitle');

    await userEvent.click(screen.getByRole('button', { name: 'artifactPreview.nextSlide' }));
    expect(screen.getByText('2 / 3')).toBeInTheDocument();
    await waitFor(() => expect(viewer.goToSlide).toHaveBeenCalledWith(1));
  });

  it('hides the pager for a single-slide deck', async () => {
    openMock.mockResolvedValue(fakeViewer(1));
    render(<FilePreview workspaceId="1" path="deck.pptx" />);
    await screen.findByTitle('artifactPreview.slideFrameTitle');
    expect(screen.queryByRole('button', { name: 'artifactPreview.nextSlide' })).not.toBeInTheDocument();
  });
});

describe('PptxFilePreview (text fallback)', () => {
  it('falls back to extracted text when the renderer fails', async () => {
    mockFetchReturning(() => makePptxBuffer(['Hello slide', 'Second slide']));
    openMock.mockRejectedValue(new Error('renderer boom'));

    render(<FilePreview workspaceId="1" path="deck.pptx" />);

    expect(await screen.findByText('artifactPreview.textFallback')).toBeInTheDocument();
    expect(screen.getByText('Hello slide')).toBeInTheDocument();
    // no rich iframe in fallback mode
    expect(screen.queryByTitle('artifactPreview.slideFrameTitle')).not.toBeInTheDocument();
  });
});

describe('PptxFilePreview (error)', () => {
  it('shows an error when the file cannot be loaded', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 404 }));
    render(<FilePreview workspaceId="1" path="deck.pptx" />);
    expect(await screen.findByText(/HTTP 404/)).toBeInTheDocument();
  });
});
