import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { listClaudeAccounts } from '@/lib/claude-account-api'
import { claudeConfigApi } from '@/lib/claude-config-api'

export function ClaudeSettings() {
  const { t } = useTranslation('settings')
  const qc = useQueryClient()
  const accounts = useQuery({ queryKey: ['claude-accounts'], queryFn: listClaudeAccounts })
  const effId = accounts.data?.[0]?.id ?? 0

  const [marketplaceSource, setMarketplaceSource] = useState('')
  const [pluginFilter, setPluginFilter] = useState<'all' | 'installed' | 'available'>('all')

  const cfg = useQuery({
    queryKey: ['claude-config', effId],
    queryFn: () => claudeConfigApi.get(effId),
    enabled: effId > 0,
  })

  const invalidateCfg = () => qc.invalidateQueries({ queryKey: ['claude-config', effId] })

  const pluginMut = useMutation({
    mutationFn: (v: { id: string; enabled: boolean }) =>
      claudeConfigApi.setPlugin(effId, v.id, v.enabled),
    onSuccess: invalidateCfg,
    onError: () => toast.error(t('claude.toggle_failed')),
  })

  const installMut = useMutation({
    mutationFn: (id: string) => claudeConfigApi.installPlugin(effId, id),
    onSuccess: invalidateCfg,
    onError: () => toast.error(t('claude.op_failed')),
  })

  const uninstallMut = useMutation({
    mutationFn: (id: string) => claudeConfigApi.uninstallPlugin(effId, id),
    onSuccess: invalidateCfg,
    onError: () => toast.error(t('claude.op_failed')),
  })

  const mcpMut = useMutation({
    mutationFn: (v: { name: string; enabled: boolean }) =>
      claudeConfigApi.setMCP(effId, v.name, v.enabled),
    onSuccess: invalidateCfg,
    onError: () => toast.error(t('claude.toggle_failed')),
  })

  const marketplaceMut = useMutation({
    mutationFn: (source: string) => claudeConfigApi.addMarketplace(effId, source),
    onSuccess: () => {
      invalidateCfg()
      setMarketplaceSource('')
    },
    onError: () => toast.error(t('claude.op_failed')),
  })

  const plugins = (cfg.data?.plugins ?? []).filter(
    (p) => pluginFilter === 'all' || (pluginFilter === 'installed' ? p.installed : !p.installed)
  )

  return (
    <div className="py-6 space-y-6 max-w-2xl">
      <p className="text-sm text-muted-foreground">{t('claude.restart_hint')}</p>

      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('claude.manage_marketplaces')}</h3>
        <div className="flex items-center gap-2">
          <Input
            value={marketplaceSource}
            onChange={(e) => setMarketplaceSource(e.target.value)}
            placeholder={t('claude.marketplace_placeholder')}
            className="h-8 text-sm flex-1"
            disabled={marketplaceMut.isPending}
          />
          <Button
            size="sm"
            variant="outline"
            onClick={() => marketplaceMut.mutate(marketplaceSource.trim())}
            disabled={marketplaceMut.isPending || marketplaceSource.trim() === ''}
          >
            {t('claude.add_marketplace')}
          </Button>
        </div>
      </section>

      <section className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-medium">{t('claude.plugins')}</h3>
          <div className="flex items-center gap-1 text-xs">
            {(['all', 'installed', 'available'] as const).map((f) => (
              <button
                key={f}
                type="button"
                onClick={() => setPluginFilter(f)}
                className={`rounded px-2 py-0.5 ${pluginFilter === f ? 'bg-brand/10 text-brand' : 'text-muted-foreground hover:text-foreground'}`}
              >
                {t(`claude.filter_${f}`)}
              </button>
            ))}
          </div>
        </div>
        {plugins.map((p) => (
          <div
            key={p.id}
            className="flex items-center justify-between rounded border border-warm-border px-3 py-2"
          >
            <span className="flex items-center gap-1.5 text-sm">
              {p.id}
              {p.featured && (
                <span className="shrink-0 rounded-sm bg-brand/10 px-1.5 py-0.5 text-[10px] font-medium text-brand">
                  {t('claude.featured')}
                </span>
              )}
            </span>
            {p.installed ? (
              <div className="flex items-center gap-3">
                <Switch
                  checked={p.enabled}
                  onCheckedChange={(en) => pluginMut.mutate({ id: p.id, enabled: en })}
                />
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 text-destructive hover:text-destructive"
                  onClick={() => uninstallMut.mutate(p.id)}
                  disabled={uninstallMut.isPending}
                >
                  {t('claude.uninstall')}
                </Button>
              </div>
            ) : (
              <Button
                size="sm"
                variant="outline"
                className="h-7"
                onClick={() => installMut.mutate(p.id)}
                disabled={installMut.isPending}
              >
                {t('claude.install')}
              </Button>
            )}
          </div>
        ))}
        {plugins.length === 0 && (
          <p className="text-sm text-muted-foreground">{t('claude.no_plugins')}</p>
        )}
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('claude.mcp_servers')}</h3>
        {cfg.data?.mcp_servers.map((m) => (
          <div
            key={m.name}
            className="flex items-center justify-between rounded border border-warm-border px-3 py-2"
          >
            <span className="text-sm">{m.name}</span>
            <Switch
              checked={m.enabled}
              onCheckedChange={(en) => mcpMut.mutate({ name: m.name, enabled: en })}
            />
          </div>
        ))}
        {cfg.data?.mcp_servers.length === 0 && (
          <p className="text-sm text-muted-foreground">{t('claude.no_mcp_servers')}</p>
        )}
      </section>
    </div>
  )
}
