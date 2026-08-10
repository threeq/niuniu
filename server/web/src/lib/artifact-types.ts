// Artifact type detection shared by the file preview renderer and the
// artifact preview panel. Keeps the office/image extension lists in one place
// so "what can we render inline" stays consistent across surfaces.

export const DOC_EXTS = ['docx'] as const;
// csv/tsv render as tables via the spreadsheet previewer.
export const SHEET_EXTS = ['xlsx', 'xls', 'csv', 'tsv'] as const;
export const SLIDE_EXTS = ['pptx'] as const;
export const IMAGE_EXTS = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif'] as const;
export const PDF_EXTS = ['pdf'] as const;
export const HTML_EXTS = ['html', 'htm'] as const;
export const MARKDOWN_EXTS = ['md', 'markdown'] as const;
// Common plain-text / data deliverables. Deliberately excludes source-code
// extensions (.js/.ts/.go/…) so build scripts never show up as "products".
export const TEXT_EXTS = ['txt', 'json', 'xml', 'yaml', 'yml', 'log'] as const;

export type ArtifactKind = 'doc' | 'sheet' | 'slide' | 'image' | 'pdf' | 'html' | 'markdown' | 'text' | 'other';

export function extOf(path: string): string {
  return path.split('.').pop()?.toLowerCase() ?? '';
}

export function artifactKind(path: string): ArtifactKind {
  const ext = extOf(path);
  if ((DOC_EXTS as readonly string[]).includes(ext)) return 'doc';
  if ((SHEET_EXTS as readonly string[]).includes(ext)) return 'sheet';
  if ((SLIDE_EXTS as readonly string[]).includes(ext)) return 'slide';
  if ((IMAGE_EXTS as readonly string[]).includes(ext)) return 'image';
  if ((PDF_EXTS as readonly string[]).includes(ext)) return 'pdf';
  if ((HTML_EXTS as readonly string[]).includes(ext)) return 'html';
  if ((MARKDOWN_EXTS as readonly string[]).includes(ext)) return 'markdown';
  if ((TEXT_EXTS as readonly string[]).includes(ext)) return 'text';
  return 'other';
}

// Files a non-technical user can view inline without downloading: the four
// office/产物 families the preview panel is built for.
export function isPreviewableArtifact(path: string): boolean {
  return artifactKind(path) !== 'other';
}
