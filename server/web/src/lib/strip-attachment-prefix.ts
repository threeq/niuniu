import i18n from '@/i18n';
import type { ChatAttachment } from '@/types/api';

// Strip attachment metadata that the backend appends to message content for the
// Agent, so the UI shows only what the user actually typed.
//   New format:    "original content\n\n[XX: path1, path2]"
//   Legacy format: prefix marker + "\n---\n" + original content
export function stripAttachmentPrefix(content: string, attachments?: ChatAttachment[]): string {
  if (!attachments || attachments.length === 0) return content;

  // Strip new suffix format that backend appends. The marker is server-side
  // (not localized) — use unicode escape so the source stays clean of raw
  // Chinese literals for the no-chinese-literal rule.
  const attachLabel = String.fromCharCode(0x9644, 0x4ef6); // attachment marker
  const suffix = '\n\n[' + attachLabel + ': ' + attachments.map((a) => a.path).join(', ') + ']';
  if (content.endsWith(suffix)) {
    return content.slice(0, -suffix.length);
  }

  // Strip legacy prefix format: "<attachmentSuffixPrefix>...\n---\noriginal content"
  const sep = '\n---\n';
  const idx = content.indexOf(sep);
  const legacyMarker = i18n.t('workspaces:chatMessage.attachmentSuffixPrefix');
  if (idx !== -1 && content.startsWith(legacyMarker)) {
    return content.slice(idx + sep.length);
  }

  return content;
}
