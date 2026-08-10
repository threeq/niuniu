import { api } from '@/lib/api';
import type {
  ImBotChannel,
  ImBotChat,
  ImBotBot,
  ImBotPendingChat,
  CreateImBotChannelBody,
  UpdateImBotChannelBody,
} from '@/types/imbot';
import { normalizeImBotPendingChat } from '@/types/imbot';

// REST wrapper for the project-scoped IM Bot endpoints. Every path is prefixed
// with /projects/${pid}/imbot/... (api.ts already prepends /api).
export const imbotApi = {
  listChannels: (pid: number): Promise<ImBotChannel[]> =>
    api
      .get<{ items: ImBotChannel[] }>(`/projects/${pid}/imbot/channels`)
      .then((r) => r.items ?? []),

  createChannel: (pid: number, body: CreateImBotChannelBody): Promise<ImBotChannel> =>
    api.post<ImBotChannel>(`/projects/${pid}/imbot/channels`, body),

  updateChannel: (pid: number, id: number, body: UpdateImBotChannelBody): Promise<ImBotChannel> =>
    api.patch<ImBotChannel>(`/projects/${pid}/imbot/channels/${id}`, body),

  deleteChannel: (pid: number, id: number): Promise<void> =>
    api.delete<void>(`/projects/${pid}/imbot/channels/${id}`),

  testChannel: (pid: number, id: number): Promise<{ ok: boolean }> =>
    api.post<{ ok: boolean }>(`/projects/${pid}/imbot/channels/${id}/test`),

  addChat: (pid: number, channelId: number, chatExtId: string, chatName: string): Promise<ImBotChat> =>
    api.post<ImBotChat>(`/projects/${pid}/imbot/channels/${channelId}/chats`, {
      chat_ext_id: chatExtId,
      chat_name: chatName,
    }),

  listChats: (pid: number): Promise<ImBotChat[]> =>
    api
      .get<{ items: ImBotChat[] }>(`/projects/${pid}/imbot/chats`)
      .then((r) => r.items ?? []),

  approveChat: (pid: number, chatId: number): Promise<ImBotChat> =>
    api.post<ImBotChat>(`/projects/${pid}/imbot/chats/${chatId}/approve`),

  patchChat: (
    pid: number,
    chatId: number,
    body: { bind_mode?: string; pinned_issue_id?: number | null; active_issue_id?: number | null; status?: string },
  ): Promise<ImBotChat> => api.patch<ImBotChat>(`/projects/${pid}/imbot/chats/${chatId}`, body),

  deleteChat: (pid: number, chatId: number): Promise<void> =>
    api.delete<void>(`/projects/${pid}/imbot/chats/${chatId}`),

  // Minimal issue list for the third-layer "pin a chat to a task" picker
  // (GET /api/projects/:id/issues returns the full issue objects; we only use
  // id + title). Kept here so the imbot UI has a single import surface.
  listProjectIssues: (pid: number): Promise<{ id: number; title: string }[]> =>
    api.get<{ id: number; title: string }[]>(`/projects/${pid}/issues`),

  // Onboarding: start an AI-guided channel setup conversation. Returns the
  // issue and workspace IDs for the guidance session.
  // POST /api/projects/:pid/imbot/onboarding → 201 { issue_id, workspace_id }
  startOnboarding: (pid: number): Promise<{ issue_id: number; workspace_id: number }> =>
    api.post<{ issue_id: number; workspace_id: number }>(`/projects/${pid}/imbot/onboarding`),

  // WeChat direct connect: mint a one-time QR-onboarding link for a project and
  // return its root-relative path, so the settings UI can jump straight to the
  // scan page (same secure token flow the assistant uses, without the chat step).
  // POST /api/projects/:pid/imbot/wechat-link → { url }
  issueWechatLink: (pid: number, name?: string): Promise<{ url: string }> =>
    api.post<{ url: string }>(`/projects/${pid}/imbot/wechat-link`, name ? { name } : {}),

  // Onboarding info — public, token-authed (not project-scoped).
  // GET /api/imbot/onboarding/:token/info → 200 { platform, channel_name, connection_mode }
  // 410 = token invalid/expired/used.
  getOnboardingInfo: (token: string): Promise<{ platform: string; channel_name: string; connection_mode: string }> =>
    api.get<{ platform: string; channel_name: string; connection_mode: string }>(
      `/imbot/onboarding/${token}/info`,
    ),

  // Onboarding credential submission — public, token-authed (not project-scoped).
  // POST /api/imbot/onboarding/:token/credential → 200 { ok, channel_id }
  // 410 = token invalid/expired; 400 = validation error.
  submitOnboardingCredential: (
    token: string,
    body: Record<string, string>,
  ): Promise<{ ok: boolean; channel_id: number }> =>
    api.post<{ ok: boolean; channel_id: number }>(`/imbot/onboarding/${token}/credential`, body),

  // WeChat 微信ClawBot QR connect — public, token-authed. Start the QR handshake
  // and long-poll its status. The confirmed poll creates the channel server-side.
  // POST /api/imbot/onboarding/:token/wechat/login/start → { qrcode_img_content }
  wechatLoginStart: (token: string): Promise<{ qrcode_img_content: string }> =>
    api.post<{ qrcode_img_content: string }>(`/imbot/onboarding/${token}/wechat/login/start`),

  // POST /api/imbot/onboarding/:token/wechat/login/poll { verify_code? }
  // → { status, channel_id? }. status is one of the iLink states; "confirmed"
  // means the channel was created (channel_id set), "expired" means restart.
  wechatLoginPoll: (
    token: string,
    verifyCode?: string,
  ): Promise<{ status: string; channel_id?: number }> =>
    api.post<{ status: string; channel_id?: number }>(
      `/imbot/onboarding/${token}/wechat/login/poll`,
      verifyCode ? { verify_code: verifyCode } : {},
    ),
};

// ---------------------------------------------------------------------------
// Owner-level "shared bot" REST wrapper (design 2026-07-08). These endpoints are
// NOT project-scoped: they list/route across every bot the current owner owns.
// (api.ts already prepends /api.)
// ---------------------------------------------------------------------------
export const imbotOwnerApi = {
  // GET /api/imbot/bots → all bots owned by the current owner.
  listBots: (): Promise<ImBotBot[]> =>
    api.get<{ items: ImBotBot[] }>(`/imbot/bots`).then((r) => r.items ?? []),

  // PUT /api/imbot/bots/:id → update a bot; here used to rename it. Only the
  // name is sent; the backend preserves every other field (incl. the webhook
  // secret) on empty (see service.UpdateChannel preserve-on-empty contract).
  updateBot: (id: number, name: string): Promise<ImBotBot> =>
    api.put<ImBotBot>(`/imbot/bots/${id}`, { name }),

  // POST /api/imbot/bots/:id/test → probe connectivity for one bot.
  testBot: (id: number): Promise<{ ok: boolean }> =>
    api.post<{ ok: boolean }>(`/imbot/bots/${id}/test`),

  // GET /api/imbot/pending-chats → owner-level chats awaiting approval + routing.
  listPendingChats: (): Promise<ImBotPendingChat[]> =>
    api
      .get<{ items: ImBotPendingChat[] }>(`/imbot/pending-chats`)
      .then((r) => (r.items ?? []).map(normalizeImBotPendingChat)),

  // POST /api/imbot/chats/:chatid/approve { project_id } → approve + route to a project.
  approveChat: (chatId: number, projectId: number): Promise<ImBotChat> =>
    api.post<ImBotChat>(`/imbot/chats/${chatId}/approve`, { project_id: projectId }),

  // POST /api/imbot/chats/:chatid/reassign { project_id } → move an active chat to another project.
  reassignChat: (chatId: number, projectId: number): Promise<ImBotChat> =>
    api.post<ImBotChat>(`/imbot/chats/${chatId}/reassign`, { project_id: projectId }),

  // GET /api/imbot/chats → the owner's active chat->project bindings.
  listChats: (): Promise<ImBotPendingChat[]> =>
    api
      .get<{ items: ImBotPendingChat[] }>(`/imbot/chats`)
      .then((r) => (r.items ?? []).map(normalizeImBotPendingChat)),

  // DELETE /api/imbot/chats/:chatid → remove a chat->project binding (unpair).
  deleteChat: (chatId: number): Promise<void> => api.delete<void>(`/imbot/chats/${chatId}`),
};
