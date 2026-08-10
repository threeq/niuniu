// IM Bot remote channel types (Epic #555). Mirrors the Go DTOs in
// internal/service/imbot.go. Credentials are write-only: they are never
// returned by the API (only `has_credential` flags whether one is stored).

export type ImBotChannelType = 'lark' | 'dingtalk' | 'telegram' | 'wework' | 'wechat';
export type ImBotConnectionMode = 'stream' | 'webhook';
export type ImBotChannelStatus = 'active' | 'disabled';
export type ImBotChatStatus = 'pending' | 'active' | 'disabled';
export type ImBotBindMode = 'project' | 'workspace';

export interface ImBotChannel {
  id: number;
  channel_type: ImBotChannelType;
  name: string;
  connection_mode: ImBotConnectionMode;
  status: ImBotChannelStatus;
  has_credential: boolean;
  created_at: string;
  updated_at: string;
}

export interface ImBotChat {
  id: number;
  channel_id: number;
  chat_ext_id: string;
  chat_name: string;
  bind_mode: ImBotBindMode;
  pinned_issue_id: number | null;
  active_issue_id: number | null;
  status: ImBotChatStatus;
  paired_by: number | null;
  created_at: string;
  updated_at: string;
}

export interface CreateImBotChannelBody {
  channel_type: ImBotChannelType;
  name: string;
  connection_mode?: ImBotConnectionMode;
  webhook_secret?: string;
  credential?: Record<string, unknown>;
}

export interface UpdateImBotChannelBody {
  name?: string;
  connection_mode?: ImBotConnectionMode;
  webhook_secret?: string;
  status?: ImBotChannelStatus;
  credential?: Record<string, unknown>;
}

// ---------------------------------------------------------------------------
// Owner-level "shared bot" model (design 2026-07-08). A bot = one credential =
// one connection, owned by the owner (user/org) and bound to no project. All of
// the owner's projects are peers; routing lives on the chat (see project_id on
// ImBotPendingChat). The `im_bot_channels` table is reused (UI name "IM 机器人"
// != DB name).
// ---------------------------------------------------------------------------

export interface ImBotBot {
  id: number;
  name: string;
  channel_type: ImBotChannelType;
  connection_mode: ImBotConnectionMode;
  status: ImBotChannelStatus;
  has_credential: boolean;
}

// Owner-level pending chat awaiting approval + project routing. `project_id` is
// null while pending (assigned at approval / reassignment time).
export interface ImBotPendingChat {
  id: number;
  channel_id: number;
  chat_ext_id: string;
  chat_name: string;
  status: ImBotChatStatus;
  // Nullable DTO contract: null while pending, set once routed to a project.
  project_id: number | null;
}

// Normalize a raw owner-level pending chat DTO (nullable project_id contract).
export function normalizeImBotPendingChat(raw: ImBotPendingChat): ImBotPendingChat {
  return { ...raw, project_id: raw.project_id ?? null };
}
