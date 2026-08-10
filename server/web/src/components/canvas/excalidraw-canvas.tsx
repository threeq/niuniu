import { useEffect, useMemo, useRef } from 'react';
import { Excalidraw, exportToBlob, serializeAsJSON, THEME } from '@excalidraw/excalidraw';
import '@excalidraw/excalidraw/index.css';
import type { ExcalidrawImperativeAPI } from '@excalidraw/excalidraw/types';
import type { CanvasExporter } from './embedded-canvas-panel';

interface ExcalidrawCanvasProps {
  dark: boolean;
  /** Registers the current-scene exporter with the parent panel; cleared on
   *  unmount. The parent invokes it when the user clicks "发给 Agent". Optional
   *  in view-only mode, where nothing is exported. */
  registerExporter?: (fn: CanvasExporter | null) => void;
  /** Existing `.excalidraw` JSON to load instead of a blank scene (preview). */
  initialContent?: string;
  /** Preview mode: load the scene read-only, no export/send. */
  viewOnly?: boolean;
}

// Parse a `.excalidraw` file into Excalidraw's initialData shape; tolerate junk.
function parseScene(raw: string | undefined) {
  if (!raw) return undefined;
  try {
    const j = JSON.parse(raw) as { elements?: unknown; appState?: unknown; files?: unknown };
    return {
      elements: (Array.isArray(j.elements) ? j.elements : []) as never,
      appState: (j.appState ?? {}) as never,
      files: (j.files ?? {}) as never,
      scrollToContent: true,
    };
  } catch {
    return undefined;
  }
}

/**
 * ExcalidrawCanvas embeds the Excalidraw annotation surface and knows how to
 * export the current scene to a PNG blob plus a `.excalidraw` source string.
 *
 * Default-exported so it can be `React.lazy()`-loaded — this keeps the heavy
 * Excalidraw bundle (and its CSS) out of the main chunk until the canvas panel
 * is actually opened.
 */
export default function ExcalidrawCanvas({
  dark,
  registerExporter,
  initialContent,
  viewOnly = false,
}: ExcalidrawCanvasProps) {
  const apiRef = useRef<ExcalidrawImperativeAPI | null>(null);
  const initialData = useMemo(() => parseScene(initialContent), [initialContent]);

  useEffect(() => {
    if (viewOnly) return; // preview mode never exports
    const exporter: CanvasExporter = async () => {
      const api = apiRef.current;
      if (!api) return null;
      const elements = api.getSceneElements();
      if (elements.length === 0) return null; // empty canvas — nothing to send
      const appState = api.getAppState();
      const files = api.getFiles();
      const blob = await exportToBlob({
        elements,
        appState: { ...appState, exportBackground: true },
        files,
        mimeType: 'image/png',
        exportPadding: 16,
      });
      // `.excalidraw` JSON — the editable source, persisted into the worktree so
      // the annotation is diffable and re-openable.
      const sourceContent = serializeAsJSON(elements, appState, files, 'local');
      return { blob, sourceContent };
    };
    registerExporter?.(exporter);
    return () => registerExporter?.(null);
  }, [registerExporter, viewOnly]);

  return (
    <div className="h-full w-full">
      <Excalidraw
        excalidrawAPI={(api) => {
          apiRef.current = api;
        }}
        theme={dark ? THEME.DARK : THEME.LIGHT}
        initialData={initialData}
        viewModeEnabled={viewOnly}
      />
    </div>
  );
}
