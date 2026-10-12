import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { integrationApprovalApi } from '@/lib/integration-approval-api'
import { useAuthStore } from '@/stores/auth-store'
import type { Org, OrgMember } from '@/types/org'
import type { IntegrationApproval } from '@/types/integration-approval'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'

interface TabApprovalsProps {
  org: Org
  role: string | undefined
}

// Mirrors the backend action→role matrix. Hiding the buttons is convenience;
// the server rejects any decision the caller's role does not permit.
function canDecide(item: IntegrationApproval, role: string | undefined, currentUserId?: number): boolean {
  if (item.status !== 'pending') return false
  if (currentUserId != null && item.requested_by === currentUserId) return false
  switch (item.required_role) {
    case 'owner':
      return role === 'owner'
    case 'admin':
    case 'project_admin':
      return role === 'owner' || role === 'admin'
    default:
      return false
  }
}

function displayUser(members: OrgMember[], userID: number, fallback: string): string {
  const m = members.find((x) => x.user_id === userID)
  return m ? m.display_name || m.username : fallback
}

export function TabApprovals({ org, role }: TabApprovalsProps) {
  const { t } = useTranslation('orgs')
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((s) => s.user)
  const [notes, setNotes] = useState<Record<number, string>>({})
  const [error, setError] = useState<string | null>(null)

  const canView = role === 'owner' || role === 'admin'

  const { data: members = [] } = useQuery({
    queryKey: ['org-members', org.id],
    queryFn: () => api.listMembers(org.id),
    enabled: canView,
  })

  const { data: items = [], isLoading } = useQuery({
    queryKey: ['org-integration-approvals', org.id, 'pending'],
    queryFn: () => integrationApprovalApi.listByOrg(org.id, 'pending'),
    enabled: canView,
  })

  const decide = useMutation({
    mutationFn: ({ id, approve }: { id: number; approve: boolean }) =>
      integrationApprovalApi.decide(org.id, id, approve, notes[id]?.trim() || undefined),
    onSuccess: () => {
      setError(null)
      queryClient.invalidateQueries({ queryKey: ['org-integration-approvals', org.id] })
    },
    onError: (e: Error) => setError(t('approvals.decideFailed', { error: e.message })),
  })

  if (!canView) {
    return (
      <div className="py-6">
        <p className="text-sm text-muted-foreground">{t('approvals.noPermission')}</p>
      </div>
    )
  }

  return (
    <div className="py-6 space-y-4">
      <p className="text-sm text-muted-foreground">{t('approvals.hint')}</p>

      {error && <p className="text-sm text-destructive">{error}</p>}

      {isLoading ? (
        <p className="text-sm text-muted-foreground text-center py-8">{t('approvals.loading')}</p>
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground text-center py-8">{t('approvals.empty')}</p>
      ) : (
        <div className="space-y-3">
          {items.map((item) => {
            const permitted = canDecide(item, role, currentUser?.id)
            const selfRequest = currentUser?.id != null && item.requested_by === currentUser.id
            return (
              <Card key={item.id}>
                <CardContent className="pt-4 space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-2 min-w-0">
                      <Badge variant="secondary">{t(`approvals.actions.${item.action}`)}</Badge>
                      <span className="text-sm font-medium truncate">
                        {item.subject_label || `${item.subject_type}:${item.subject_id}`}
                      </span>
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      {item.escalation_count > 0 && (
                        <Badge variant="destructive">
                          {t('approvals.escalated', { count: item.escalation_count })}
                        </Badge>
                      )}
                      <span className="text-xs text-muted-foreground">
                        {t('approvals.requiredRole', { role: t(`approvals.role.${item.required_role}`) })}
                      </span>
                    </div>
                  </div>

                  {item.summary && <p className="text-sm text-foreground">{item.summary}</p>}

                  <p className="text-xs text-muted-foreground">
                    {t('approvals.requestedBy', {
                      user: displayUser(members, item.requested_by, t('approvals.unknownUser', { id: item.requested_by })),
                    })}
                    {' · '}
                    {t('approvals.expiresAt', { time: new Date(item.expires_at).toLocaleString() })}
                  </p>

                  {item.diff && (
                    <details className="text-xs">
                      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
                        {t('approvals.diff')}
                      </summary>
                      <pre className="mt-2 p-3 rounded bg-muted overflow-x-auto whitespace-pre-wrap break-all font-mono">
                        {item.diff}
                      </pre>
                    </details>
                  )}

                  {permitted ? (
                    <div className="flex items-center gap-2">
                      <Input
                        value={notes[item.id] ?? ''}
                        onChange={(e) => setNotes((n) => ({ ...n, [item.id]: e.target.value }))}
                        placeholder={t('approvals.notePlaceholder')}
                        className="flex-1"
                      />
                      <Button
                        size="sm"
                        onClick={() => decide.mutate({ id: item.id, approve: true })}
                        disabled={decide.isPending}
                      >
                        {t('approvals.approve')}
                      </Button>
                      <Button
                        size="sm"
                        variant="destructive"
                        onClick={() => decide.mutate({ id: item.id, approve: false })}
                        disabled={decide.isPending}
                      >
                        {t('approvals.deny')}
                      </Button>
                    </div>
                  ) : (
                    <p className="text-xs text-muted-foreground">
                      {selfRequest ? t('approvals.selfDecide') : t('approvals.notPermitted')}
                    </p>
                  )}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}
