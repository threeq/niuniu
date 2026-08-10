import { api } from '@/lib/api'

export interface ClaudePluginInfo { id: string; enabled: boolean; installed: boolean; featured: boolean }
export interface ClaudeMCPInfo { name: string; enabled: boolean; source: string }
export interface ClaudeConfigView { plugins: ClaudePluginInfo[]; mcp_servers: ClaudeMCPInfo[] }

export const claudeConfigApi = {
  get: (accountId: number) =>
    api.get<ClaudeConfigView>(`/claude-accounts/${accountId}/config`),
  setPlugin: (accountId: number, pluginId: string, enabled: boolean) =>
    api.put<{ enabled: boolean; restart_required: boolean }>(
      `/claude-accounts/${accountId}/plugins/${encodeURIComponent(pluginId)}`, { enabled }),
  setMCP: (accountId: number, name: string, enabled: boolean) =>
    api.put<{ enabled: boolean; restart_required: boolean }>(
      `/claude-accounts/${accountId}/mcp/${encodeURIComponent(name)}`, { enabled }),
  installPlugin: (accountId: number, pluginId: string, ref?: string) =>
    api.post(`/claude-accounts/${accountId}/library/plugins/${encodeURIComponent(pluginId)}/install`, { ref }),
  uninstallPlugin: (accountId: number, pluginId: string) =>
    api.post(`/claude-accounts/${accountId}/library/plugins/${encodeURIComponent(pluginId)}/uninstall`, {}),
  addMarketplace: (accountId: number, source: string) =>
    api.post(`/claude-accounts/${accountId}/library/marketplaces`, { source }),
}
