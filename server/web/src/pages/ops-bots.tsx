import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Bot, Package, RefreshCw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/shared/empty-state'
import { AccountList } from '@/components/ops/account-list'
import { opsApi } from '@/lib/api'
import type { OpsAccount, OpsOwner } from '@/types/ops'

/**
 * Ops Bot console (design 2026-11-06 §14.3). The page is grouped owner →
 * channel (Platform Pack) → account and never collapses the accounts into one
 * score. Pause/resume is available inline; every account drills into its own
 * page for metrics, calendar, engagements, approvals and browser state.
 */
export function OpsBotsPage() {
  const { t } = useTranslation('ops')
  const queryClient = useQueryClient()

  const { data, isLoading, isError, isFetching, refetch } = useQuery({
    queryKey: ['ops-bots'],
    queryFn: () => opsApi.list(null),
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: 1,
  })

  const toggleStatus = useMutation({
    mutationFn: (account: OpsAccount) =>
      account.status === 'active'
        ? opsApi.pause(account.bot_id)
        : opsApi.resume(account.bot_id),
    onSuccess: (_bot, account) => {
      const name = account.display_name || account.account_ref || `#${account.bot_id}`
      toast.success(
        account.status === 'active'
          ? t('toast.paused', { name })
          : t('toast.resumed', { name }),
      )
      queryClient.invalidateQueries({ queryKey: ['ops-bots'] })
    },
    onError: (err: unknown) => {
      toast.error(
        t('toast.actionFailed', { message: err instanceof Error ? err.message : String(err) }),
      )
    },
  })

  const busyBotId = toggleStatus.isPending ? toggleStatus.variables?.bot_id : null

  const owners: OpsOwner[] = (data?.owners ?? []).filter(
    (owner) => owner.channels.length > 0 || owner.error,
  )

  return (
    <div className="h-full overflow-auto bg-warm-canvas">
      <div className="mx-auto max-w-6xl p-4 md:p-6 space-y-4">
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="text-lg font-semibold">{t('title')}</h1>
            <p className="text-sm text-muted-foreground">{t('subtitle')}</p>
          </div>
          <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
            <RefreshCw className={isFetching ? 'h-4 w-4 animate-spin' : 'h-4 w-4'} />
            {t('refresh')}
          </Button>
        </header>

        {isLoading && !data ? (
          <div className="space-y-3" aria-busy="true">
            <div className="h-24 rounded-lg border bg-card" />
            <div className="h-24 rounded-lg border bg-card" />
          </div>
        ) : isError && !data ? (
          <div className="rounded-lg border bg-card p-4 flex items-center justify-between gap-3">
            <span className="text-sm text-muted-foreground">{t('loadError')}</span>
            <Button variant="outline" size="sm" onClick={() => refetch()}>
              {t('retry')}
            </Button>
          </div>
        ) : !data || owners.length === 0 ? (
          <div className="rounded-lg border bg-card">
            <EmptyState
              icon={<Bot className="h-10 w-10 text-muted-foreground" />}
              title={t('emptyTitle')}
              description={t('emptyDescription')}
              suggestions={[t('emptySuggestion1'), t('emptySuggestion2')]}
            />
          </div>
        ) : (
          owners.map((owner) => (
            <section
              key={`${owner.owner_type}:${owner.owner_id}`}
              className="space-y-2"
              aria-label={owner.owner_label}
            >
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-sm font-semibold">{owner.owner_label}</h2>
                <Badge variant="outline" className="rounded px-1.5 py-0.5 text-xs">
                  {owner.owner_type}
                </Badge>
              </div>

              {owner.error ? (
                <p className="text-sm text-destructive-text">
                  {t('ownerError', { message: owner.error })}
                </p>
              ) : null}

              {owner.channels.map((channel) => (
                <div key={channel.pack_id} className="rounded-lg border bg-card overflow-hidden">
                  <div className="flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
                    <div className="flex min-w-0 items-center gap-2">
                      <Package className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                      <span className="truncate text-sm font-medium">
                        {channel.pack_id || t('packUnbound')}
                      </span>
                      {channel.pack_version > 0 ? (
                        <span className="text-xs text-muted-foreground">
                          {t('packVersion', { version: channel.pack_version })}
                        </span>
                      ) : null}
                    </div>
                    <span className="text-xs text-muted-foreground">
                      {t('channelCount', { count: channel.accounts.length })}
                    </span>
                  </div>
                  <div className="p-3 md:p-0">
                    <AccountList
                      accounts={channel.accounts}
                      busyBotId={busyBotId}
                      onToggleStatus={(account) => toggleStatus.mutate(account)}
                    />
                  </div>
                </div>
              ))}
            </section>
          ))
        )}
      </div>
    </div>
  )
}
