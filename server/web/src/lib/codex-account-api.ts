import { api, apiFetch } from './api'
import type {
  CodexAccount,
  CodexAccountCreateResp,
  CodexAccountVisibility,
  CodexDefaultStatus,
  CodexSandboxMode,
  CodexApprovalPolicy,
} from '@/types/api'

export async function listCodexAccounts(): Promise<CodexAccount[]> {
  const resp = await api.get<{ items: CodexAccount[] }>('/codex-accounts')
  return resp.items
}

// Probes the host's native ~/.codex/auth.json so the settings UI can show
// whether the system default account is already logged in. Personal/embedded
// mode only — the caller should not invoke this in hosted mode.
export async function getCodexDefaultStatus(): Promise<CodexDefaultStatus> {
  return api.get<CodexDefaultStatus>('/codex-accounts/default-status')
}

export async function createCodexAccount(body: {
  name: string
  visibility?: CodexAccountVisibility
}): Promise<CodexAccountCreateResp> {
  return api.post<CodexAccountCreateResp>('/codex-accounts', body)
}

// suppressError so the caller can translate 409 `already_authed` and similar
// targeted errors. Mirrors the claude-account-api.ts companion.
export async function issueCodexLoginToken(id: number): Promise<{ ws_token: string }> {
  return apiFetch<{ ws_token: string }>(`/codex-accounts/${id}/login`, {
    method: 'POST',
    suppressError: true,
  })
}

export async function refreshCodexAccount(id: number): Promise<void> {
  await api.post(`/codex-accounts/${id}/refresh`, {})
}

export async function updateCodexAccount(
  id: number,
  patch: { name?: string; visibility?: CodexAccountVisibility },
): Promise<void> {
  await api.patch(`/codex-accounts/${id}`, patch)
}

export async function deleteCodexAccount(id: number): Promise<void> {
  await api.delete(`/codex-accounts/${id}`)
}

export async function setWorkspaceCodexAccount(
  workspaceID: number,
  accountID: number | null,
): Promise<void> {
  await api.put(`/workspaces/${workspaceID}/codex-account`, { account_id: accountID })
}

export async function setWorkspaceCodexSandbox(
  workspaceID: number,
  body: { sandbox_mode?: CodexSandboxMode; approval_policy?: CodexApprovalPolicy },
): Promise<void> {
  await api.put(`/workspaces/${workspaceID}/codex-sandbox`, body)
}
