// REST wrappers for the chat-message pin feature. Backend handler in
// server/internal/api/pinned_message.go; routes wired under
// /api/workspaces/:id/pinned-messages in server/internal/server/router.go.
//
// Spec: docs/superpowers/specs/2026-06-04-chat-message-pin-design.md
import { api } from '@/lib/api'

/** A pinned chat message (bookmark shown in the pin-message panel). */
export interface PinnedMessage {
  id: number
  workspace_id: number
  /** Stable server messageId; DOM id of the message row is `msg-<message_id>`. */
  message_id: string
  /** Originating role: 'user' | 'assistant' | 'system' (drives the panel icon). */
  role: string
  /** Short plain-text snippet captured at pin time. */
  preview: string
  /** Unix epoch milliseconds. */
  created_at: number
}

export interface CreatePinBody {
  message_id: string
  role?: string
  preview?: string
}

export async function listPinnedMessages(workspaceId: string | number): Promise<PinnedMessage[]> {
  const r = await api.get<{ pins: PinnedMessage[] }>(`/workspaces/${workspaceId}/pinned-messages`)
  return r.pins ?? []
}

export async function createPinnedMessage(
  workspaceId: string | number,
  body: CreatePinBody,
): Promise<PinnedMessage> {
  const r = await api.post<{ pin: PinnedMessage }>(`/workspaces/${workspaceId}/pinned-messages`, body)
  return r.pin
}

export async function deletePinnedMessage(
  workspaceId: string | number,
  pinId: number,
): Promise<void> {
  await api.delete<void>(`/workspaces/${workspaceId}/pinned-messages/${pinId}`)
}
