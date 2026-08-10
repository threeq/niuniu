import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { PptxViewer } from '@aiden0z/pptx-renderer';
import { useTranslation } from 'react-i18next';
import { ChevronLeft, ChevronRight, Download, FileText, Pause, Play } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { getFileContentUrl } from '@/lib/workspace-file-url';
import { MarkdownMessage } from '@/components/shared/markdown-message';
import { highlightCode } from '@/lib/syntax-highlight';
import {
  extOf,
  IMAGE_EXTS,
  AUDIO_EXTS,
  VIDEO_EXTS,
  BINARY_EXTS,
  NO_HIGHLIGHT_EXTS,
} from '@/lib/file-type';

// Above this line count we skip per-line tokenization and render a plain
// scrollable <pre>: highlighting tens of thousands of lines would block the
// main thread on open. This is the perf 兜底 — large files degrade to plain text.
const MAX_HIGHLIGHT_LINES = 5000;

// Rendered for blank lines so the line-number table row keeps its height.
const ZERO_WIDTH_SPACE = String.fromCharCode(0x200b);

// Auto-advance interval for the slide carousel, in milliseconds.
const SLIDE_AUTOPLAY_MS = 4000;

interface FilePreviewProps {
  workspaceId: string;
  path: string;
}

// FilePreview renders a workspace deliverable by type: markdown, docx, image,
// audio, video (native + HLS/FLV), pdf, or plain text. Falls back to a download
// link for unknown binaries. Thin wrapper over FilePreviewByUrl that builds the
// workspace raw-bytes URL.
export function FilePreview({ workspaceId, path }: FilePreviewProps) {
  return <FilePreviewByUrl url={getFileContentUrl(workspaceId, path, 'raw')} path={path} />;
}

// FilePreviewByUrl is the type-dispatching renderer. It takes a raw-bytes URL
// plus the file path (used only for extension detection and titles), so any
// surface with its own raw file endpoint — the workspace artifact panel or the
// repository file browser — can reuse the same renderers.
export function FilePreviewByUrl({ url, path }: { url: string; path: string }) {
  const ext = extOf(path);

  if (IMAGE_EXTS.includes(ext)) {
    return <ImageFilePreview url={url} path={path} />;
  }

  if (AUDIO_EXTS.includes(ext)) {
    return (
      <div className="p-4">
        <audio controls src={url} className="w-full">
          <track kind="captions" />
        </audio>
      </div>
    );
  }

  // These players latch failed/loading state per source and unmount the
  // <video> on failure, so they can't recover once stuck. Key them on `url`
  // so a source change always remounts a clean player, independent of whether
  // the caller happens to remount FilePreview.
  if (VIDEO_EXTS.includes(ext)) {
    return <VideoFilePreview key={url} url={url} />;
  }

  if (ext === 'm3u8') {
    return <HlsFilePreview key={url} url={url} />;
  }

  if (ext === 'flv') {
    return <FlvFilePreview key={url} url={url} />;
  }

  if (ext === 'pdf') {
    return <iframe src={url} title={path} className="w-full h-full min-h-[70vh] rounded-md border border-border" />;
  }

  // Rendered HTML deliverables (e.g. the office-design scene's single-file
  // pages). Sandboxed to a unique origin: scripts run for fidelity but can't
  // touch our origin/cookies.
  if (ext === 'html' || ext === 'htm') {
    return (
      <iframe
        src={url}
        title={path}
        sandbox="allow-scripts"
        className="w-full h-full min-h-[70vh] rounded-md border border-border bg-white"
      />
    );
  }

  if (ext === 'md' || ext === 'markdown') {
    return <MarkdownFilePreview url={url} />;
  }

  if (ext === 'docx') {
    return <DocxFilePreview url={url} />;
  }

  if (ext === 'xlsx' || ext === 'xls' || ext === 'csv' || ext === 'tsv') {
    // SheetJS auto-detects CSV/TSV from the bytes, so they render as tables too.
    return <XlsxFilePreview url={url} />;
  }

  if (ext === 'pptx') {
    return <PptxFilePreview url={url} />;
  }

  // Binary formats can't be shown inline — offer a download instead. Everything
  // else (code, config, and extensionless/specially-named files like Makefile,
  // .gitignore, go.mod, Dockerfile, LICENSE, …) renders as text/code.
  if (BINARY_EXTS.has(ext)) {
    return <DownloadFallback url={url} />;
  }
  return <TextFilePreview url={url} ext={ext} />;
}

// DownloadFallback is the shared "can't preview — download instead" state, used
// both for unknown binaries and for video formats the browser can't play.
function DownloadFallback({ url, message }: { url: string; message?: string }) {
  const { t } = useTranslation('workspaces');
  return (
    <div className="flex flex-col items-center gap-2 p-8 text-sm text-muted-foreground">
      <FileText className="w-8 h-8" />
      <span>{message ?? t('filePreview.unsupported')}</span>
      <a href={url} download className="inline-flex items-center gap-1 text-info hover:underline">
        <Download className="w-3.5 h-3.5" />
        {t('filePreview.download')}
      </a>
    </div>
  );
}

// ImageFilePreview renders raster/vector images (png/jpg/svg/webp/…). A neutral
// transparency checkerboard sits behind the image so a transparent SVG/PNG —
// e.g. a fireworks tech-graph exported without a baked-in background — stays
// readable in both light and dark themes instead of vanishing into the panel;
// an opaque image simply covers it. Click (or Enter/Space) toggles fit-to-pane
// ↔ actual size for large exports (the skill emits 1920px @2× PNGs), with the
// container scrolling when zoomed past the viewport.
function ImageFilePreview({ url, path }: { url: string; path: string }) {
  const { t } = useTranslation('workspaces');
  const [zoomed, setZoomed] = useState(false);
  const toggle = () => setZoomed((z) => !z);
  return (
    <div className="flex items-center justify-center p-4">
      <div className="preview-checkerboard max-w-full max-h-[70vh] overflow-auto rounded-md border border-border">
        <img
          src={url}
          alt={path}
          role="button"
          tabIndex={0}
          aria-label={zoomed ? t('filePreview.imageZoomFit') : t('filePreview.imageZoomActual')}
          onClick={toggle}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              toggle();
            }
          }}
          className={
            zoomed
              ? 'block max-w-none cursor-zoom-out'
              : 'block max-w-full max-h-[70vh] object-contain cursor-zoom-in'
          }
        />
      </div>
    </div>
  );
}

// VideoFilePreview renders a natively-playable container with <video controls>.
// If the browser can't decode it (e.g. some .mov codecs), it degrades to a
// download link rather than showing a broken player.
function VideoFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const [failed, setFailed] = useState(false);
  if (failed) return <DownloadFallback url={url} message={t('filePreview.playbackFailed')} />;
  return (
    <div className="p-4">
      <video
        controls
        src={url}
        onError={() => setFailed(true)}
        className="w-full max-h-[70vh] rounded-md bg-black"
      >
        <track kind="captions" />
      </video>
    </div>
  );
}

// HlsFilePreview plays an HLS (.m3u8) stream. Safari plays HLS natively; other
// browsers get hls.js, lazy-loaded so the demuxer only ships when an HLS file is
// opened. Unsupported environments degrade to a download link.
function HlsFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const videoRef = useRef<HTMLVideoElement>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    let cancelled = false;
    let hls: import('hls.js').default | null = null;
    // Safari (and iOS WebKit) decode HLS without any JS help.
    if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = url;
      setLoading(false);
      return;
    }
    (async () => {
      try {
        const { default: Hls } = await import('hls.js');
        if (cancelled) return;
        if (!Hls.isSupported()) {
          setFailed(true);
          setLoading(false);
          return;
        }
        hls = new Hls();
        hls.loadSource(url);
        hls.attachMedia(video);
        hls.on(Hls.Events.MANIFEST_PARSED, () => { if (!cancelled) setLoading(false); });
        hls.on(Hls.Events.ERROR, (_event, data) => {
          if (data.fatal && !cancelled) { setFailed(true); setLoading(false); }
        });
      } catch {
        if (!cancelled) { setFailed(true); setLoading(false); }
      }
    })();
    return () => { cancelled = true; hls?.destroy(); };
  }, [url]);
  if (failed) return <DownloadFallback url={url} message={t('filePreview.playbackFailed')} />;
  return (
    <div className="p-4">
      {loading && <PreviewStatus>{t('filePreview.loading')}</PreviewStatus>}
      <video ref={videoRef} controls className="w-full max-h-[70vh] rounded-md bg-black">
        <track kind="captions" />
      </video>
    </div>
  );
}

// FlvFilePreview plays an FLV (.flv) stream via mpegts.js, lazy-loaded for the
// same code-splitting reason as HLS. Unsupported environments fall back to a
// download link.
function FlvFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const videoRef = useRef<HTMLVideoElement>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    let cancelled = false;
    let player: ReturnType<(typeof import('mpegts.js'))['default']['createPlayer']> | null = null;
    (async () => {
      try {
        const { default: mpegts } = await import('mpegts.js');
        if (cancelled) return;
        if (!mpegts.isSupported()) {
          setFailed(true);
          setLoading(false);
          return;
        }
        player = mpegts.createPlayer({ type: 'flv', url });
        player.attachMediaElement(video);
        player.load();
        player.on(mpegts.Events.ERROR, () => { if (!cancelled) { setFailed(true); setLoading(false); } });
        setLoading(false);
      } catch {
        if (!cancelled) { setFailed(true); setLoading(false); }
      }
    })();
    return () => { cancelled = true; player?.destroy(); };
  }, [url]);
  if (failed) return <DownloadFallback url={url} message={t('filePreview.playbackFailed')} />;
  return (
    <div className="p-4">
      {loading && <PreviewStatus>{t('filePreview.loading')}</PreviewStatus>}
      <video ref={videoRef} controls className="w-full max-h-[70vh] rounded-md bg-black">
        <track kind="captions" />
      </video>
    </div>
  );
}

function useRawText(url: string): { text: string; loading: boolean; error: string } {
  const [text, setText] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- async fetch: synchronous loading-state flag before the promise resolves is the correct pattern
    setLoading(true);
    setError('');
    fetch(url)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((body) => { if (!cancelled) setText(body); })
      .catch((e: unknown) => { if (!cancelled) setError(e instanceof Error ? e.message : 'load failed'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [url]);
  return { text, loading, error };
}

function MarkdownFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const { text, loading, error } = useRawText(url);
  if (loading) return <PreviewStatus>{t('filePreview.loading')}</PreviewStatus>;
  if (error) return <PreviewStatus error>{error}</PreviewStatus>;
  return (
    <div className="p-4 text-sm">
      <MarkdownMessage content={text} role="assistant" />
    </div>
  );
}

// TextFilePreview renders a text deliverable. Code-ish files get per-line
// syntax highlighting via the shared `highlightCode` tokenizer — the same one
// the diff viewer uses, so the two stay visually consistent. All colors come
// from the design-system `--syntax-*` tokens, which means light/dark theme
// switching is automatic (the CSS vars re-resolve; no re-highlight needed) and
// no heavy highlighter (shiki/prism) ships in the bundle. Plain-data text
// (txt/log) and oversized files degrade to an un-highlighted <pre>.
function TextFilePreview({ url, ext }: { url: string; ext: string }) {
  const { t } = useTranslation('workspaces');
  const { text, loading, error } = useRawText(url);
  const lineCount = useMemo(() => (text ? text.split('\n').length : 0), [text]);

  if (loading) return <PreviewStatus>{t('filePreview.loading')}</PreviewStatus>;
  if (error) return <PreviewStatus error>{error}</PreviewStatus>;

  // The highlighter is language-agnostic, so highlight anything that isn't plain
  // prose/tabular data — this covers extensionless code (Makefile, go.mod, …).
  const isCode = !NO_HIGHLIGHT_EXTS.has(ext);
  const tooLarge = lineCount > MAX_HIGHLIGHT_LINES;

  if (isCode && !tooLarge) {
    return <HighlightedCode text={text} />;
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      {isCode && tooLarge && (
        <PreviewStatus>{t('filePreview.highlightDisabledLargeFile')}</PreviewStatus>
      )}
      <pre
        className={`flex-1 min-h-0 overflow-auto p-4 text-xs font-mono text-foreground ${
          isCode ? 'whitespace-pre' : 'whitespace-pre-wrap break-words'
        }`}
      >
        {text}
      </pre>
    </div>
  );
}

// HighlightedCode renders code with a line-number gutter and tokenized lines.
// Tokenization runs once per file (memoized on `text`); re-renders from theme
// or layout changes reuse the cached nodes since the colors are CSS-token-based.
function HighlightedCode({ text }: { text: string }) {
  const lines = useMemo(() => {
    const split = text.split('\n');
    // Empty lines need a zero-width space so the table row keeps its height.
    return split.map((line) => (line.length > 0 ? highlightCode(line) : ZERO_WIDTH_SPACE));
  }, [text]);
  const gutterWidth = String(lines.length).length + 1;

  return (
    <div className="h-full min-h-0 overflow-auto">
      <table className="w-full border-collapse">
        <tbody>
          {lines.map((nodes, i) => (
            <tr key={i}>
              <td
                className="select-none border-r border-border px-2 text-right align-top font-mono text-[11px] leading-5 text-muted-foreground/70"
                style={{ width: `${gutterWidth}ch` }}
              >
                {i + 1}
              </td>
              <td className="whitespace-pre px-3 align-top font-mono text-[12.5px] leading-5 text-foreground">
                {nodes}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function DocxFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const [html, setHtml] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    (async () => {
      try {
        const [{ default: mammoth }, { default: DOMPurify }, buf] = await Promise.all([
          import('mammoth'),
          import('dompurify'),
          fetch(url).then((r) => (r.ok ? r.arrayBuffer() : Promise.reject(new Error(`HTTP ${r.status}`)))),
        ]);
        const result = await mammoth.convertToHtml({ arrayBuffer: buf });
        // Sanitize: mammoth output is already a constrained subset, but run it
        // through DOMPurify so a crafted .docx can never inject script/handlers.
        if (!cancelled) setHtml(DOMPurify.sanitize(result.value));
      } catch (e: unknown) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'docx render failed');
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [url]);
  if (loading) return <PreviewStatus>{t('filePreview.loading')}</PreviewStatus>;
  if (error) return <PreviewStatus error>{error}</PreviewStatus>;
  // mammoth emits a constrained subset of HTML (p/h*/ul/ol/li/table/strong/em/a);
  // it does not pass through scripts or event handlers from the .docx.
  return (
    <div
      className="p-4 text-sm prose-preview text-foreground [&_h1]:text-lg [&_h2]:text-base [&_h1]:font-semibold [&_h2]:font-semibold [&_p]:my-2 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_a]:text-info"
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

// XlsxFilePreview renders an .xlsx/.xls workbook as HTML tables, one per sheet,
// with a sheet selector. SheetJS + DOMPurify are lazy-loaded so the spreadsheet
// parser only ships when a user actually opens a workbook.
function XlsxFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const [sheets, setSheets] = useState<{ name: string; html: string }[]>([]);
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    setActive(0);
    (async () => {
      try {
        const [XLSX, { default: DOMPurify }, buf] = await Promise.all([
          import('xlsx'),
          import('dompurify'),
          fetch(url).then((r) => (r.ok ? r.arrayBuffer() : Promise.reject(new Error(`HTTP ${r.status}`)))),
        ]);
        const wb = XLSX.read(buf, { type: 'array' });
        const parsed = wb.SheetNames.map((name) => ({
          name,
          // sheet_to_html emits a constrained <table>; DOMPurify guards against
          // a crafted workbook smuggling script/handlers through cell content.
          html: DOMPurify.sanitize(XLSX.utils.sheet_to_html(wb.Sheets[name], { editable: false })),
        }));
        if (!cancelled) setSheets(parsed);
      } catch (e: unknown) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'xlsx render failed');
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [url]);
  if (loading) return <PreviewStatus>{t('filePreview.loading')}</PreviewStatus>;
  if (error) return <PreviewStatus error>{error}</PreviewStatus>;
  if (sheets.length === 0) return <PreviewStatus>{t('filePreview.unsupported')}</PreviewStatus>;
  return (
    <div className="flex flex-col h-full min-h-0">
      {sheets.length > 1 && (
        <div className="flex gap-1 flex-wrap p-2 border-b border-border shrink-0">
          {sheets.map((s, i) => (
            <button
              key={s.name}
              type="button"
              onClick={() => setActive(i)}
              className={`px-2 py-0.5 text-xs rounded-md ${i === active ? 'bg-accent text-foreground font-medium' : 'text-muted-foreground hover:bg-accent'}`}
            >
              {s.name}
            </button>
          ))}
        </div>
      )}
      <div
        className="flex-1 min-h-0 overflow-auto p-3 text-xs [&_table]:border-collapse [&_td]:border [&_td]:border-border [&_td]:px-2 [&_td]:py-1 [&_th]:border [&_th]:border-border [&_th]:px-2 [&_th]:py-1 [&_th]:bg-muted"
        dangerouslySetInnerHTML={{ __html: sheets[active]?.html ?? '' }}
      />
    </div>
  );
}

interface PptxTextSlide { lines: string[]; }

function decodeXmlEntities(s: string): string {
  return s
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&amp;/g, '&');
}

// Legacy text-only extraction: pull the <a:t> runs from each slide's XML.
// Used as a graceful fallback when the rich renderer can't handle a deck, so a
// preview never regresses below "you can at least read the words".
async function extractPptxText(buf: ArrayBuffer): Promise<PptxTextSlide[]> {
  const { default: JSZip } = await import('jszip');
  const zip = await JSZip.loadAsync(buf);
  const slideNum = (name: string) => Number(name.match(/slide(\d+)\.xml$/)?.[1] ?? 0);
  const slideNames = Object.keys(zip.files)
    .filter((n) => /^ppt\/slides\/slide\d+\.xml$/.test(n))
    .sort((a, b) => slideNum(a) - slideNum(b));
  return Promise.all(
    slideNames.map(async (n) => {
      const xml = await zip.files[n].async('string');
      const lines = [...xml.matchAll(/<a:t>([\s\S]*?)<\/a:t>/g)]
        .map((m) => decodeXmlEntities(m[1]).trim())
        .filter((s) => s.length > 0);
      return { lines };
    }),
  );
}

type PptxMode = 'loading' | 'render' | 'text' | 'error';

// PptxFilePreview renders a .pptx with real fidelity — layout, images, shapes,
// tables and charts — using @aiden0z/pptx-renderer (a pure client-side OOXML
// renderer; deps echarts + jszip are already bundled). Each slide is rendered
// into a sandboxed `<iframe sandbox="allow-same-origin">`: the renderer runs as
// trusted parent code building DOM into the frame's document, while the frame
// itself can't execute scripts, so any markup a crafted deck smuggles through a
// chart/SmartArt label sink is inert and can't touch our origin. ZIP DoS limits
// (RECOMMENDED_ZIP_LIMITS) and EMF/pdf fallback disabled (`pdfjs: false`) further
// shrink the attack surface. If parsing/rendering fails we fall back to legacy
// text extraction so the preview never regresses to nothing.
function PptxFilePreview({ url }: { url: string }) {
  const { t } = useTranslation('workspaces');
  const [mode, setMode] = useState<PptxMode>('loading');
  const [total, setTotal] = useState(0);
  const [current, setCurrent] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [aspect, setAspect] = useState('16 / 9');
  const [textSlides, setTextSlides] = useState<PptxTextSlide[]>([]);
  const [error, setError] = useState('');
  const frameRef = useRef<HTMLIFrameElement>(null);
  const viewerRef = useRef<PptxViewer | null>(null);

  useEffect(() => {
    let cancelled = false;
    const ctrl = new AbortController();
    setMode('loading');
    setCurrent(0);
    setPlaying(false);
    setError('');
    setTextSlides([]);
    (async () => {
      let buf: ArrayBuffer;
      try {
        buf = await fetch(url, { signal: ctrl.signal })
          .then((r) => (r.ok ? r.arrayBuffer() : Promise.reject(new Error(`HTTP ${r.status}`))));
      } catch (e: unknown) {
        if (!cancelled) { setError(e instanceof Error ? e.message : 'pptx load failed'); setMode('error'); }
        return;
      }
      if (cancelled) return;
      try {
        const frame = frameRef.current;
        const doc = frame?.contentDocument;
        if (!doc) throw new Error('preview frame unavailable');
        doc.body.style.margin = '0';
        doc.body.style.background = 'transparent';
        const { PptxViewer, RECOMMENDED_ZIP_LIMITS } = await import('@aiden0z/pptx-renderer');
        if (cancelled) return;
        const viewer = await PptxViewer.open(buf, doc.body, {
          renderMode: 'slide',
          fitMode: 'contain',
          zipLimits: RECOMMENDED_ZIP_LIMITS,
          pdfjs: false,
          signal: ctrl.signal,
        });
        if (cancelled) { viewer.destroy(); return; }
        viewerRef.current = viewer;
        setTotal(viewer.slideCount);
        if (viewer.slideWidth > 0 && viewer.slideHeight > 0) {
          setAspect(`${viewer.slideWidth} / ${viewer.slideHeight}`);
        }
        setMode('render');
      } catch (e: unknown) {
        if (cancelled || ctrl.signal.aborted) return;
        // Rich rendering failed — degrade to legacy text extraction.
        try {
          const slides = await extractPptxText(buf);
          if (cancelled) return;
          setTextSlides(slides);
          setTotal(slides.length);
          setMode(slides.length > 0 ? 'text' : 'error');
          if (slides.length === 0) setError(e instanceof Error ? e.message : 'pptx render failed');
        } catch (e2: unknown) {
          if (!cancelled) { setError(e2 instanceof Error ? e2.message : 'pptx render failed'); setMode('error'); }
        }
      }
    })();
    return () => {
      cancelled = true;
      ctrl.abort();
      viewerRef.current?.destroy();
      viewerRef.current = null;
    };
  }, [url]);

  // Drive the rich renderer's current slide from our own navigator state.
  useEffect(() => {
    if (mode === 'render') void viewerRef.current?.goToSlide(current);
  }, [current, mode]);

  const go = useCallback(
    (next: number) => setCurrent(total === 0 ? 0 : (next + total) % total),
    [total],
  );
  // Autoplay advances one slide every SLIDE_AUTOPLAY_MS and loops; disabled for
  // a single-slide deck. The interval is torn down whenever `playing`/`total`
  // change so toggling pause is immediate.
  useEffect(() => {
    if (!playing || total <= 1) return;
    const id = setInterval(() => setCurrent((c) => (c + 1) % total), SLIDE_AUTOPLAY_MS);
    return () => clearInterval(id);
  }, [playing, total]);

  if (mode === 'error') return <PreviewStatus error>{error || t('filePreview.unsupported')}</PreviewStatus>;

  const multi = total > 1;
  const textSlide = textSlides[current];
  return (
    <div className="flex flex-col items-center gap-4 p-4">
      {mode === 'text' && (
        <div className="text-[11px] text-muted-foreground">{t('artifactPreview.textFallback')}</div>
      )}
      <div className="flex w-full items-center justify-center gap-1.5">
        {multi && (
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 shrink-0 rounded-full"
            aria-label={t('artifactPreview.prevSlide')}
            onClick={() => go(current - 1)}
          >
            <ChevronLeft className="h-5 w-5" />
          </Button>
        )}
        <div className="w-full max-w-[640px]">
          {/* The render frame is always mounted (even while loading) so its
              contentDocument is available to the renderer; hidden until ready
              and replaced entirely when we fall back to text. */}
          {mode !== 'text' && (
            <div
              className="relative w-full bg-card border border-border rounded-md shadow-sm overflow-hidden"
              style={{ aspectRatio: aspect }}
            >
              <iframe
                ref={frameRef}
                title={t('artifactPreview.slideFrameTitle')}
                sandbox="allow-same-origin"
                className={`block w-full h-full border-0 bg-card transition-opacity ${mode === 'render' ? 'opacity-100' : 'opacity-0'}`}
              />
              {mode === 'loading' && (
                <div className="absolute inset-0 flex items-center justify-center text-xs text-muted-foreground">
                  {t('filePreview.loading')}
                </div>
              )}
              {mode === 'render' && (
                <div className="absolute bottom-1 right-1.5 text-[10px] text-muted-foreground/70 bg-card/80 rounded px-1">
                  {current + 1} / {total}
                </div>
              )}
            </div>
          )}
          {mode === 'text' && (
            <div className="w-full aspect-video bg-card border border-border rounded-md shadow-sm relative overflow-hidden flex flex-col p-6">
              <span className="absolute left-0 top-0 w-1.5 h-full bg-info" aria-hidden="true" />
              {textSlide && textSlide.lines.length > 0 ? (
                <>
                  <h3 className="text-base font-semibold text-foreground leading-tight">{textSlide.lines[0]}</h3>
                  <div className="mt-3 flex-1 min-h-0 overflow-auto text-xs text-muted-foreground space-y-1">
                    {textSlide.lines.slice(1).map((line, i) => (
                      <div key={i}>{line}</div>
                    ))}
                  </div>
                </>
              ) : (
                <div className="m-auto text-xs text-muted-foreground">
                  {t('artifactPreview.emptySlide', { n: current + 1 })}
                </div>
              )}
              <div className="mt-2 flex justify-end text-[10px] text-muted-foreground/70">
                {current + 1} / {total}
              </div>
            </div>
          )}
        </div>
        {multi && (
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 shrink-0 rounded-full"
            aria-label={t('artifactPreview.nextSlide')}
            onClick={() => go(current + 1)}
          >
            <ChevronRight className="h-5 w-5" />
          </Button>
        )}
      </div>
      {multi && (
        <div className="flex items-center gap-3">
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 shrink-0 rounded-full"
            aria-label={playing ? t('artifactPreview.pause') : t('artifactPreview.play')}
            aria-pressed={playing}
            onClick={() => setPlaying((p) => !p)}
          >
            {playing ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
          </Button>
          <div className="flex gap-1.5 flex-wrap justify-center">
            {Array.from({ length: total }, (_, i) => (
              <button
                key={i}
                type="button"
                aria-label={t('artifactPreview.gotoSlide', { n: i + 1 })}
                aria-current={i === current}
                onClick={() => setCurrent(i)}
                className={`h-1.5 rounded-full transition-all ${i === current ? 'w-4 bg-info' : 'w-1.5 bg-border hover:bg-muted-foreground/40'}`}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function PreviewStatus({ children, error }: { children: React.ReactNode; error?: boolean }) {
  return (
    <div className={`p-4 text-xs ${error ? 'text-destructive' : 'text-muted-foreground'}`}>{children}</div>
  );
}
