import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { api } from '@/lib/api';
import type { ChatAttachment } from '@/types/api';
import { useAttachmentStore } from '@/stores/attachment-store';
import { useChatInputBridge } from '@/stores/chat-input-bridge-store';

// Mirrors the backend attachment cap (imageopt runs server-side ≤10MB).
const MAX_IMAGE_BYTES = 10 * 1024 * 1024;

export interface CanvasSourceFile {
  /** Worktree name (WorktreeGroup.name) the source file is written into. */
  worktree: string;
  /** Worktree-relative path, e.g. "docs/canvas/annotation.excalidraw". */
  path: string;
  /** File contents (e.g. serialized `.excalidraw` JSON). */
  content: string;
}

export interface SendCanvasOptions {
  /** Exported raster image (PNG) the agent can view via read_image. */
  blob: Blob;
  /** Image filename, e.g. "annotation.png". */
  filename: string;
  /** Message text sent alongside the image. */
  prompt: string;
  /** true → send immediately; false → stage in the input for review. */
  autoSend?: boolean;
  /** Optional editor source to persist into the worktree (so it's diffable). */
  source?: CanvasSourceFile;
}

export interface SendCanvasResult {
  attachment: ChatAttachment;
  /** Workspace-root-relative path of the persisted source file, if any. */
  sourcePath?: string;
}

/**
 * useCanvasBridge is the reusable "embedded canvas → Agent" skeleton (B 模式).
 * It takes an exported image (+ optional source file) from any inline editor
 * and runs the shared pipeline:
 *
 *   (persist source → worktree, diffable) → uploadAttachment (imageopt ≤10MB)
 *   → chat-input bridge
 *
 * Editor-agnostic — Excalidraw is the first consumer, but the same hook drives
 * any future embedded canvas.
 */
export function useCanvasBridge(workspaceId: string) {
  const { t } = useTranslation('workspaces');
  const [sending, setSending] = useState(false);
  const requestChatInput = useChatInputBridge((s) => s.request);
  const addAttachment = useAttachmentStore((s) => s.addAttachment);

  const send = useCallback(
    async (opts: SendCanvasOptions): Promise<SendCanvasResult | null> => {
      if (opts.blob.size > MAX_IMAGE_BYTES) {
        toast.error(t('canvas.errors.tooLarge', { limit: '10MB' }));
        return null;
      }
      setSending(true);
      try {
        // 1) Persist the editor source into the worktree first, so it lands in
        //    git (diffable) even if a later step fails.
        let sourcePath: string | undefined;
        if (opts.source) {
          const res = await api.writeWorktreeFile(
            workspaceId,
            opts.source.worktree,
            opts.source.path,
            opts.source.content,
          );
          sourcePath = res.path;
        }

        // 2) Upload the exported image (reuses server-side imageopt).
        const file = new File([opts.blob], opts.filename, {
          type: opts.blob.type || 'image/png',
        });
        const uploaded = await api.uploadAttachment(workspaceId, file);
        const attachment: ChatAttachment = {
          path: uploaded.path,
          type: 'upload',
          name: uploaded.name,
          mimeType: uploaded.mimeType,
          size: uploaded.size,
          originalSize: uploaded.originalSize,
          optimized: uploaded.optimized,
        };

        // 3) Hand off to the chat input. On autoSend the attachment rides
        //    straight into handleSend via the bridge; otherwise stage it as a
        //    removable tag in the attachment store for the user to review.
        if (opts.autoSend) {
          requestChatInput(workspaceId, opts.prompt, true, [attachment]);
        } else {
          addAttachment(attachment);
          requestChatInput(workspaceId, opts.prompt, false);
        }
        return { attachment, sourcePath };
      } catch {
        toast.error(t('canvas.errors.sendFailed'));
        return null;
      } finally {
        setSending(false);
      }
    },
    [workspaceId, requestChatInput, addAttachment, t],
  );

  return { send, sending };
}
