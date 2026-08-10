// File-type classification shared by the file preview and the content viewer.
// Kept in its own (component-free) module so both can import it without
// tripping react-refresh's "only export components" rule.

export function extOf(path: string): string {
  return path.split('.').pop()?.toLowerCase() ?? '';
}

export const IMAGE_EXTS = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif'];
export const AUDIO_EXTS = ['mp3', 'wav', 'ogg', 'oga', 'm4a', 'aac', 'flac'];
// Containers a browser <video> element can play natively. Streaming formats
// (HLS .m3u8 / FLV .flv) need a JS demuxer and are handled separately.
export const VIDEO_EXTS = ['mp4', 'webm', 'ogv', 'mov'];

// Formats with their own rich renderer (image/av/pdf/office/markdown).
export const RICH_PREVIEW_EXTS = new Set<string>([
  ...IMAGE_EXTS, ...AUDIO_EXTS, ...VIDEO_EXTS,
  'm3u8', 'flv', 'pdf', 'html', 'htm', 'md', 'markdown',
  'doc', 'docx', 'xls', 'xlsx', 'csv', 'tsv', 'ppt', 'pptx', 'odt', 'ods', 'odp',
]);

// Known binary/non-text formats — offered as a download, never rendered inline.
export const BINARY_EXTS = new Set<string>([
  // archives
  'zip', 'tar', 'gz', 'tgz', 'bz2', 'xz', '7z', 'rar', 'jar', 'war', 'ear', 'zst',
  // executables / objects / libraries / packages
  'exe', 'dll', 'so', 'dylib', 'bin', 'o', 'obj', 'a', 'lib', 'class', 'pyc', 'pyo',
  'wasm', 'node', 'msi', 'apk', 'app', 'dmg', 'deb', 'rpm',
  // fonts
  'ttf', 'otf', 'woff', 'woff2', 'eot',
  // images/media/raw not handled above
  'ico', 'icns', 'heic', 'heif', 'tiff', 'tif', 'psd', 'ai', 'sketch', 'raw',
  // databases / packed data
  'db', 'sqlite', 'sqlite3', 'mdb', 'dat', 'pack', 'idx',
]);

// Plain prose / tabular data reads better without code tokenization; everything
// else routed to the text renderer gets the language-agnostic highlighter.
export const NO_HIGHLIGHT_EXTS = new Set<string>(['txt', 'log', 'csv', 'tsv', 'md', 'markdown']);

/**
 * True when a file should render as editable/commentable text/code: anything
 * that is neither a rich-preview format nor a known binary. Covers every
 * programming file, including extensionless / specially-named ones like
 * Makefile, Dockerfile, .gitignore, go.mod, LICENSE.
 */
export function isTextLikeFile(path: string): boolean {
  const ext = extOf(path);
  return !RICH_PREVIEW_EXTS.has(ext) && !BINARY_EXTS.has(ext);
}

/**
 * True for Markdown files. Markdown has a rich rendered preview (so it's not
 * "text-like" above), but it's also plain text — the content viewer offers a
 * preview/source toggle so the raw source can be read and commented on.
 */
export function isMarkdownFile(path: string): boolean {
  const ext = extOf(path);
  return ext === 'md' || ext === 'markdown';
}
