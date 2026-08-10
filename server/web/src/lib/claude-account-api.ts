import { api, apiFetch } from './api'
import type {
  ClaudeAccount,
  ClaudeAccountCreateResp,
  ClaudeActiveAccountResp,
  ClaudeAccountVisibility,
} from '@/types/api'

export async function listClaudeAccounts(): Promise<ClaudeAccount[]> {
  const resp = await api.get<{ items: ClaudeAccount[] }>('/claude-accounts')
  return resp.items
}

export async function createClaudeAccount(body: {
  name: string; visibility?: ClaudeAccountVisibility
}): Promise<ClaudeAccountCreateResp> {
  return api.post<ClaudeAccountCreateResp>('/claude-accounts', body)
}

// suppressError so the caller can translate the 409 `already_authed` envelope
// (and any other targeted error) into the i18n bundle instead of letting the
// global "API Error" toast surface the raw English server message. The
// settings page maps `code === 'already_authed'` → t('errors.already_authed')
// in its handleLogin catch.
export async function issueClaudeLoginToken(id: number): Promise<{ ws_token: string }> {
  return apiFetch<{ ws_token: string }>(`/claude-accounts/${id}/login`, {
    method: 'POST',
    suppressError: true,
  })
}

export async function refreshClaudeAccount(id: number): Promise<void> {
  await api.post(`/claude-accounts/${id}/refresh`, {})
}

export async function updateClaudeAccount(
  id: number, patch: { name?: string; visibility?: ClaudeAccountVisibility }
): Promise<void> {
  await api.patch(`/claude-accounts/${id}`, patch)
}

export async function deleteClaudeAccount(id: number): Promise<void> {
  await api.delete(`/claude-accounts/${id}`)
}

export async function getActiveClaudeAccount(
  ownerType: 'user' | 'org', ownerID: number
): Promise<ClaudeActiveAccountResp> {
  return api.get(`/claude-active-account?owner_type=${ownerType}&owner_id=${ownerID}`)
}

export async function setActiveClaudeAccount(body: {
  owner_type: 'user' | 'org'; owner_id: number; account_id: number | null
}): Promise<void> {
  await api.put('/claude-active-account', body)
}

export async function setWorkspaceClaudeAccount(
  workspaceID: number, accountID: number | null
): Promise<void> {
  await api.put(`/workspaces/${workspaceID}/claude-account`, { account_id: accountID })
}
