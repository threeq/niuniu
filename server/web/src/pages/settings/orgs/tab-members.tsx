import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Trash2, UserPlus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import { useAuthStore } from '@/stores/auth-store'
import { UserSearchInput } from '@/components/shared/user-search-input'
import type { Org, SelectedUser } from '@/types/org'

interface TabMembersProps {
  org: Org
  role: string | undefined
}

export function TabMembers({ org, role }: TabMembersProps) {
  const { t } = useTranslation('orgs')
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((s) => s.user)

  const canManage = role === 'owner' || role === 'admin'
  const isOwner = role === 'owner'

  const [selectedUser, setSelectedUser] = useState<SelectedUser | null>(null)
  const [addRole, setAddRole] = useState<'admin' | 'member'>('member')

  const roleLabels: Record<string, string> = {
    owner: t('detail.members.roleOwner'),
    admin: t('detail.members.roleAdmin'),
    member: t('detail.members.roleMember'),
  }

  const { data: members = [], isLoading } = useQuery({
    queryKey: ['org-members', org.id],
    queryFn: () => api.listMembers(org.id),
  })

  const addMutation = useMutation({
    mutationFn: async () => {
      if (!selectedUser) return;
      await api.addMember(org.id, { user_id: selectedUser.id, role: addRole })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['org-members', org.id] })
      setSelectedUser(null)
      setAddRole('member')
      toast.success(t('detail.members.added'))
    },
    onError: (err: Error) => {
      toast.error(t('detail.members.addFailed', { error: err.message }))
    },
  })

  const roleChangeMutation = useMutation({
    mutationFn: ({ userID, newRole }: { userID: number; newRole: string }) =>
      api.updateMemberRole(org.id, userID, newRole),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['org-members', org.id] })
    },
    onError: (err: Error) => {
      toast.error(t('detail.members.roleChangeFailed', { error: err.message }))
    },
  })

  const removeMutation = useMutation({
    mutationFn: (userID: number) => api.removeMember(org.id, userID),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['org-members', org.id] })
      toast.success(t('detail.members.removed'))
    },
    onError: (err: Error) => {
      toast.error(t('detail.members.removeFailed', { error: err.message }))
    },
  })

  function canRemove(memberUserID: number, memberRole: string) {
    if (currentUser?.id === memberUserID) return true // self-leave
    if (memberRole === 'owner') return false
    return canManage
  }

  return (
    <div className="py-6 space-y-6">
      {/* Add member */}
      {canManage && (
        <div className="border border-border rounded-lg p-4 space-y-3">
          <p className="text-sm font-medium text-foreground">{t('detail.members.addTitle')}</p>
          <div className="flex items-center gap-2">
            <UserSearchInput
              orgId={org.id}
              value={selectedUser}
              onChange={setSelectedUser}
              placeholder={t('detail.members.searchPlaceholder')}
              disabled={addMutation.isPending}
            />
            <select
              value={addRole}
              onChange={(e) => setAddRole(e.target.value as 'admin' | 'member')}
              className="h-9 px-3 border border-border bg-background rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring"
            >
              <option value="member">{t('detail.members.roleMember')}</option>
              <option value="admin">{t('detail.members.roleAdmin')}</option>
            </select>
            <Button
              size="sm"
              onClick={() => addMutation.mutate()}
              disabled={!selectedUser || addMutation.isPending}
            >
              <UserPlus className="h-4 w-4 mr-1" />
              {t('detail.members.addButton')}
            </Button>
          </div>
        </div>
      )}

      {/* Member list */}
      {isLoading ? (
        <p className="text-sm text-muted-foreground py-8 text-center">
          {t('detail.members.loading')}
        </p>
      ) : members.length === 0 ? (
        <p className="text-sm text-muted-foreground py-8 text-center">
          {t('detail.members.empty')}
        </p>
      ) : (
        <div className="space-y-2">
          {members.map((m) => (
            <div
              key={m.user_id}
              className="flex items-center justify-between px-4 py-3 border border-border rounded-lg bg-card"
            >
              <div>
                <p className="text-sm font-medium text-foreground">
                  {m.display_name || m.username}
                </p>
                <p className="text-xs text-muted-foreground">{m.username}</p>
              </div>
              <div className="flex items-center gap-2">
                {isOwner && m.role !== 'owner' ? (
                  <select
                    value={m.role}
                    onChange={(e) =>
                      roleChangeMutation.mutate({ userID: m.user_id, newRole: e.target.value })
                    }
                    className="h-7 px-2 border border-border bg-background rounded text-xs focus:outline-none focus:ring-1 focus:ring-ring"
                  >
                    <option value="admin">{t('detail.members.roleAdmin')}</option>
                    <option value="member">{t('detail.members.roleMember')}</option>
                  </select>
                ) : (
                  <span className="text-xs text-muted-foreground px-2">
                    {roleLabels[m.role] ?? m.role}
                  </span>
                )}
                {canRemove(m.user_id, m.role) && (
                  <button
                    onClick={async () => {
                      const isSelf = currentUser?.id === m.user_id
                      const msg = isSelf
                        ? t('detail.members.confirmLeave')
                        : t('detail.members.confirmRemove', {
                            name: m.display_name || m.username,
                          })
                      if (await confirm(msg)) {
                        removeMutation.mutate(m.user_id)
                      }
                    }}
                    className="p-1 text-muted-foreground hover:text-destructive"
                    title={t('detail.members.removeTitle')}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
