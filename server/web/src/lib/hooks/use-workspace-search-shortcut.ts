import { useEffect, useState } from 'react';

/**
 * Splits a matched line into highlighted / plain segments using the backend's
 * BYTE offsets.
 *
 * Offsets are byte-based (ripgrep reports them that way) while JS strings are
 * UTF-16, so the line is converted to bytes and each slice decoded back. Slicing
 * with plain string indices would mis-highlight any line containing non-ASCII
 * text — a real case in this codebase, whose comments are frequently Chinese.
 */
export function splitByColumns(
  text: string,
  columns?: [number, number][],
): { text: string; hit: boolean }[] {
  if (!columns?.length) return [{ text, hit: false }];

  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  const bytes = encoder.encode(text);

  const segments: { text: string; hit: boolean }[] = [];
  let cursor = 0;

  // Sort and clamp defensively: a malformed range must degrade to plain text,
  // never throw inside a render.
  const ranges = [...columns]
    .map(([s, e]) => [Math.max(0, s), Math.min(bytes.length, e)] as [number, number])
    .filter(([s, e]) => e > s)
    .sort((a, b) => a[0] - b[0]);

  for (const [start, end] of ranges) {
    if (start < cursor) continue; // overlapping ranges: keep the first
    if (start > cursor) {
      segments.push({ text: decoder.decode(bytes.slice(cursor, start)), hit: false });
    }
    segments.push({ text: decoder.decode(bytes.slice(start, end)), hit: true });
    cursor = end;
  }
  if (cursor < bytes.length) {
    segments.push({ text: decoder.decode(bytes.slice(cursor)), hit: false });
  }
  return segments;
}

/**
 * Owns the open/closed state and the global shortcut for the search dialog.
 *
 * Ctrl/Cmd+Shift+F is the editor-standard "find in files"; Ctrl/Cmd+P is the
 * equally standard "go to file". Both open the SAME panel — which is the point:
 * the user reaches for whichever shortcut they already know and gets both kinds
 * of result either way, instead of having to pick a search mode up front.
 */
export function useWorkspaceSearchShortcut() {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      const key = e.key.toLowerCase();
      const isFindInFiles = e.shiftKey && key === 'f';
      const isGoToFile = !e.shiftKey && !e.altKey && key === 'p';
      if (isFindInFiles || isGoToFile) {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  return { open, setOpen };
}
