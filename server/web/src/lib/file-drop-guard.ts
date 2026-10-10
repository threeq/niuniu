/**
 * Global guard against the browser's DEFAULT file-drop action.
 *
 * Dropping a file on a page that does not handle the drop makes the renderer
 * NAVIGATE to the dropped file — the SPA is replaced by the file's contents
 * (or the file is downloaded). In the desktop shell there is no address bar and
 * no back button, so that is a one-way trip out of the app. Drop zones (the
 * workspace chat input, attachment areas) call preventDefault themselves; this
 * guard covers every drop that nothing handled.
 *
 * Only drags that CARRY FILES are neutralized. preventDefault-ing all drops
 * would break dragging text into an input, which is a normal editing
 * affordance; `dataTransfer.types` is the only part of the payload readable
 * during dragover, and it is enough to tell the two apart.
 *
 * This became load-bearing for the desktop shell when the native drag-drop
 * interception was disabled (see desktop-v2 windows.rs `Html5DragDrop`): with
 * HTML5 drag and drop active again, the WebView2 navigation default is
 * reachable, and without this guard a stray drop would wipe the running app.
 *
 * Returns a cleanup function (used by tests; not called in production).
 */
export function installFileDropGuard(): () => void {
  const carriesFiles = (e: DragEvent): boolean =>
    Array.from(e.dataTransfer?.types ?? []).includes('Files');

  // dragover must be prevented too: without it the drop never fires and the
  // renderer performs its default navigation straight away.
  const onDragOver = (e: DragEvent): void => {
    if (carriesFiles(e)) e.preventDefault();
  };
  const onDrop = (e: DragEvent): void => {
    if (carriesFiles(e)) e.preventDefault();
  };

  window.addEventListener('dragover', onDragOver);
  window.addEventListener('drop', onDrop);
  return () => {
    window.removeEventListener('dragover', onDragOver);
    window.removeEventListener('drop', onDrop);
  };
}
