import { useEffect, useRef } from 'react';
import type { CanvasExporter, CanvasExportResult } from './embedded-canvas-panel';
import { parseDrawioMessage, dataUriToBlob, isEmptyDiagram } from './drawio-protocol';
import { DRAWIO_EMBED_SRC } from './drawio-embed';

interface DrawioCanvasProps {
  dark: boolean;
  /** Registers the current-scene exporter with the parent panel; cleared on
   *  unmount. The parent invokes it when the user clicks "发给 Agent". Optional
   *  in view-only mode, where nothing is exported. */
  registerExporter?: (fn: CanvasExporter | null) => void;
  /** Existing `.drawio` XML to load instead of a blank diagram (preview). */
  initialContent?: string;
  /** Preview mode: load the file, disable autosave, no export/send. */
  viewOnly?: boolean;
}

/**
 * DrawioCanvas embeds the draw.io editor via an iframe and speaks its
 * `postMessage` JSON protocol. On export it asks the editor for `xmlpng`
 * (a round-trippable PNG whose response also carries the raw diagram XML), so
 * the panel gets a viewable image for the agent *and* a diffable `.drawio`
 * source for the worktree — reusing the same {@link CanvasExporter} contract as
 * the Excalidraw editor.
 *
 * Default-exported so it can be `React.lazy()`-loaded, keeping the iframe glue
 * out of the main chunk until the panel is opened.
 */
export default function DrawioCanvas({
  dark,
  registerExporter,
  initialContent,
  viewOnly = false,
}: DrawioCanvasProps) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const readyRef = useRef(false);
  // The pending export resolver — set while an export round-trip is in flight.
  const pendingRef = useRef<((r: CanvasExportResult | null) => void) | null>(null);
  // Read the latest theme without re-running the message-listener effect.
  const darkRef = useRef(dark);
  useEffect(() => {
    darkRef.current = dark;
  }, [dark]);
  // Initial XML + view-only flag read via refs so the listener effect stays put.
  const initialXmlRef = useRef(initialContent ?? '');
  useEffect(() => {
    initialXmlRef.current = initialContent ?? '';
  }, [initialContent]);
  const viewOnlyRef = useRef(viewOnly);
  useEffect(() => {
    viewOnlyRef.current = viewOnly;
  }, [viewOnly]);

  useEffect(() => {
    // Self-hosted under the SPA's own origin — post to it, not a remote host.
    const origin = window.location.origin;
    function post(msg: unknown) {
      iframeRef.current?.contentWindow?.postMessage(JSON.stringify(msg), origin);
    }

    function onMessage(e: MessageEvent) {
      if (e.source !== iframeRef.current?.contentWindow) return;
      const msg = parseDrawioMessage(e.data);
      if (!msg) return;
      switch (msg.event) {
        case 'configure':
          // Sent before `init` (because of `configure=1`). Turn off diagram
          // compression so the exported `.drawio` XML is plain, diffable text
          // (and empty-diagram detection can read it) rather than a deflated
          // base64 blob.
          post({ action: 'configure', config: { compressXml: false } });
          break;
        case 'init':
          // Editor is ready — load the initial diagram (blank when creating,
          // existing XML when previewing). Preview disables autosave.
          readyRef.current = true;
          post({
            action: 'load',
            xml: initialXmlRef.current,
            autosave: viewOnlyRef.current ? 0 : 1,
            dark: darkRef.current,
          });
          break;
        case 'export': {
          const resolve = pendingRef.current;
          pendingRef.current = null;
          if (!resolve) break;
          const data = typeof msg.data === 'string' ? msg.data : '';
          const xml = typeof msg.xml === 'string' ? msg.xml : undefined;
          if (!data || isEmptyDiagram(xml)) {
            resolve(null);
            break;
          }
          resolve({ blob: dataUriToBlob(data), sourceContent: xml });
          break;
        }
        default:
          break;
      }
    }

    window.addEventListener('message', onMessage);

    const exporter: CanvasExporter = () =>
      new Promise<CanvasExportResult | null>((resolve) => {
        if (!readyRef.current) {
          resolve(null);
          return;
        }
        // Supersede any in-flight export so a resolver never leaks.
        pendingRef.current?.(null);
        pendingRef.current = resolve;
        post({ action: 'export', format: 'xmlpng', border: 16, background: '#ffffff' });
      });
    registerExporter?.(exporter);

    return () => {
      window.removeEventListener('message', onMessage);
      registerExporter?.(null);
      pendingRef.current?.(null);
      pendingRef.current = null;
    };
  }, [registerExporter]);

  return (
    <iframe
      ref={iframeRef}
      title="draw.io"
      src={DRAWIO_EMBED_SRC}
      className="h-full w-full border-0 bg-warm-surface"
    />
  );
}
