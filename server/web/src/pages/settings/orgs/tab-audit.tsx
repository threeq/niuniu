import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { Org } from '@/types/org'

const PAGE_SIZE = 50

interface TabAuditProps {
  org: Org
  role: string | undefined
}

export function TabAudit({ org, role }: TabAuditProps) {
  const { t } = useTranslation('orgs')
  const [offset, setOffset] = useState(0)

  const canView = role === 'owner' || role === 'admin'

  const { data: entries = [], isLoading, isFetching } = useQuery({
    queryKey: ['org-audit', org.id, offset],
    queryFn: () => api.listAuditLog(org.id, PAGE_SIZE, offset),
    enabled: canView,
  })

  if (!canView) {
    return (
      <div className="py-6">
        <p className="text-sm text-muted-foreground">{t('detail.audit.noPermission')}</p>
      </div>
    )
  }

  return (
    <div className="py-6 space-y-4">
      {isLoading ? (
        <p className="text-sm text-muted-foreground text-center py-8">
          {t('detail.audit.loading')}
        </p>
      ) : entries.length === 0 && offset === 0 ? (
        <p className="text-sm text-muted-foreground text-center py-8">
          {t('detail.audit.empty')}
        </p>
      ) : (
        <>
          <div className="space-y-1 font-mono text-xs">
            {entries.map((entry) => (
              <div
                key={entry.id}
                className="flex items-start gap-3 px-3 py-2 rounded hover:bg-accent border-b border-border/40 last:border-0"
              >
                <span className="text-muted-foreground/70 shrink-0 w-36">
                  {new Date(entry.created_at).toLocaleString()}
                </span>
                <span className="text-info shrink-0">{entry.actor_label}</span>
                <span className="text-foreground shrink-0">{entry.action}</span>
                <span className="text-muted-foreground">
                  {entry.target_type}:{entry.target_id}
                </span>
              </div>
            ))}
          </div>

          {entries.length === PAGE_SIZE && (
            <button
              onClick={() => setOffset((o) => o + PAGE_SIZE)}
              disabled={isFetching}
              className="text-sm text-info hover:text-info/80 disabled:opacity-50"
            >
              {isFetching ? t('detail.audit.loadingMore') : t('detail.audit.loadMore')}
            </button>
          )}
        </>
      )}
    </div>
  )
}
