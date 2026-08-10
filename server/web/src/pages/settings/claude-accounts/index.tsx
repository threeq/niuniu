import { useState } from 'react'
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useAuthStore } from '@/stores/auth-store'
import { useConfigStore } from '@/stores/config-store'
import {
  listClaudeAccounts,
  createClaudeAccount,
  issueClaudeLoginToken,
  updateClaudeAccount,
  deleteClaudeAccount,
  refreshClaudeAccount,
  getActiveClaudeAccount,
  setActiveClaudeAccount,
} from '@/lib/claude-account-api'
import { openClaudeLoginTerminal } from '@/lib/shell'
import { ApiError } from '@/lib/api'
import type { ClaudeAccount } from '@/types/api'
import { AccountRow } from './account-row'
import { ClaudeLoginDialog } from '@/components/dialogs/ClaudeLoginDialog'

export function ClaudeAccountsPage() {
  const { t } = useTranslation('claude-accounts')
  const qc = useQueryClient()
  const user = useAuthStore((s) => s.user)
  const personalMode = useConfigStore((s) => s.personalMode)
  const authEnabled = useConfigStore((s) => s.authEnabled)
  const isAdmin = authEnabled ? user?.role === 'admin' : true
  const callerID = user?.id ?? 0

  // Accounts list
  const { data: accounts = [], isLoading } = useQuery({
    queryKey: ['claude-accounts'],
    queryFn: listClaudeAccounts,
  })

  // Active account for personal space
  const { data: activeResp, refetch: refetchActive } = useQuery({
    queryKey: ['claude-active-account', 'user', callerID],
    queryFn: () => getActiveClaudeAccount('user', callerID),
    enabled: callerID > 0,
  })
  const activeAccountId = activeResp?.account?.id ?? null

  // Add dialog
  const [addOpen, setAddOpen] = useState(false)
  const [addName, setAddName] = useState('')
  const [addLoading, setAddLoading] = useState(false)

  // Rename dialog
  const [renameTarget, setRenameTarget] = useState<ClaudeAccount | null>(null)
  const [renameName, setRenameName] = useState('')
  const [renameLoading, setRenameLoading] = useState(false)

  // Delete confirm dialog
  const [deleteTarget, setDeleteTarget] = useState<ClaudeAccount | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  // Login dialog (xterm)
  const [loginDialogOpen, setLoginDialogOpen] = useState(false)
  const [loginToken, setLoginToken] = useState<string | null>(null)
  const [loginAccountId, setLoginAccountId] = useState<number | null>(null)

  const invalidateAccounts = () => {
    qc.invalidateQueries({ queryKey: ['claude-accounts'] })
  }

  async function handleAdd() {
    if (!addName.trim()) return
    setAddLoading(true)
    try {
      const resp = await createClaudeAccount({ name: addName.trim(), visibility: 'public' })
      invalidateAccounts()
      setAddOpen(false)
      setAddName('')
      // Open login dialog with token returned from create
      setLoginToken(resp.ws_token)
      setLoginAccountId(resp.id)
      setLoginDialogOpen(true)
    } catch {
      // apiFetch shows a toast on error
    } finally {
      setAddLoading(false)
    }
  }

  async function handleLogin(account: ClaudeAccount) {
    const isDefault = account.config_dir === ''
    if (isDefault && personalMode) {
      // Personal mode default account: open OS terminal
      try {
        await openClaudeLoginTerminal()
      } catch (err) {
        toast.error(t('loginDialog.failedToast'), {
          description: err instanceof Error ? err.message : String(err),
        })
      }
      return
    }
    // Managed row (or team mode default): issue a WS token then open dialog.
    // issueClaudeLoginToken uses suppressError so we own the toast surface
    // here. The backend returns 409 `{error,code,message}` with
    // code='already_authed' when a managed (non-default) row is already
    // active; default rows (config_dir=='') now succeed even when active —
    // see server/internal/api/claude_account.go:IssueLoginToken — so the
    // 409 path is reachable only for managed rows in current code.
    try {
      const { ws_token } = await issueClaudeLoginToken(account.id)
      setLoginToken(ws_token)
      setLoginAccountId(account.id)
      setLoginDialogOpen(true)
    } catch (err) {
      const body = err instanceof ApiError ? (err.body as { code?: string; error?: string; message?: string } | null) : null
      const code = body?.code ?? body?.error
      if (err instanceof ApiError && err.status === 409 && code === 'already_authed') {
        toast.error(t('errors.already_authed'))
        return
      }
      toast.error('API Error', {
        description: err instanceof Error ? err.message : String(err),
      })
    }
  }

  function handleRenameOpen(account: ClaudeAccount) {
    setRenameTarget(account)
    setRenameName(account.name)
  }

  async function handleRenameSave() {
    if (!renameTarget || !renameName.trim()) return
    setRenameLoading(true)
    try {
      await updateClaudeAccount(renameTarget.id, { name: renameName.trim() })
      invalidateAccounts()
      setRenameTarget(null)
    } catch {
      // toast from apiFetch
    } finally {
      setRenameLoading(false)
    }
  }

  async function handleToggleVisibility(account: ClaudeAccount) {
    try {
      await updateClaudeAccount(account.id, {
        visibility: account.visibility === 'public' ? 'private' : 'public',
      })
      invalidateAccounts()
    } catch {
      // toast from apiFetch
    }
  }

  async function handleDeleteConfirm() {
    if (!deleteTarget) return
    setDeleteLoading(true)
    try {
      await deleteClaudeAccount(deleteTarget.id)
      invalidateAccounts()
      setDeleteTarget(null)
    } catch {
      // toast from apiFetch
    } finally {
      setDeleteLoading(false)
    }
  }

  async function handleRefresh(account: ClaudeAccount) {
    try {
      await refreshClaudeAccount(account.id)
      invalidateAccounts()
    } catch {
      // toast from apiFetch
    }
  }

  async function handleActiveChange(accountId: number | null) {
    if (!callerID) return
    try {
      await setActiveClaudeAccount({
        owner_type: 'user',
        owner_id: callerID,
        account_id: accountId,
      })
      refetchActive()
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    } catch {
      // toast from apiFetch
    }
  }

  function handleLoginDialogClose() {
    setLoginDialogOpen(false)
    setLoginToken(null)
    setLoginAccountId(null)
    invalidateAccounts()
  }

  const subtitle = authEnabled ? t('subtitleTeam') : t('subtitle')
  // Active account might be private and not in our list
  const activeInList = accounts.find((a) => a.id === activeAccountId)
  const activeIsInvisible = activeAccountId !== null && !activeInList

  return (
    <div className="py-6 space-y-6">
      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h2 className="text-base font-medium text-foreground">{t('title')}</h2>
          <p className="text-sm text-muted-foreground mt-1">{subtitle}</p>
        </div>
        {isAdmin && (
          <Button size="sm" onClick={() => setAddOpen(true)}>
            <Plus className="h-4 w-4 mr-1" />
            {t('addButton')}
          </Button>
        )}
      </div>

      {/* Personal active account picker */}
      <div className="space-y-2">
        <label className="text-sm font-medium text-foreground">
          {t('active.label')} — {t('active.scopePersonal')}
        </label>
        <select
          value={activeAccountId ?? ''}
          onChange={(e) => handleActiveChange(e.target.value === '' ? null : Number(e.target.value))}
          className="w-full max-w-xs h-9 rounded border border-border bg-background px-3 text-sm focus:outline-none focus:ring-1 focus:ring-info"
        >
          <option value="">{t('active.followDefault')}</option>
          {activeIsInvisible && (
            <option value={String(activeAccountId)} disabled>
              {t('picker.privateNotVisible')}
            </option>
          )}
          {accounts
            .filter((a) => a.config_dir !== '' && a.status === 'active')
            .map((a) => (
              <option key={a.id} value={String(a.id)}>
                {a.name} {a.email ? `(${a.email})` : ''}
              </option>
            ))}
        </select>
      </div>

      {/* Account list */}
      <div className="space-y-2">
        {isLoading && (
          <p className="text-sm text-muted-foreground">{t('common:actions.loading', 'Loading…')}</p>
        )}
        {!isLoading && accounts.length === 0 && (
          <p className="text-sm text-muted-foreground italic">{t('row.neverUsed')}</p>
        )}
        {accounts.map((account) => (
          <AccountRow
            key={account.id}
            account={account}
            isAdmin={isAdmin ?? false}
            callerID={callerID}
            personalMode={personalMode}
            onLogin={handleLogin}
            onRename={handleRenameOpen}
            onToggleVisibility={handleToggleVisibility}
            onDelete={setDeleteTarget}
            onRefresh={handleRefresh}
          />
        ))}
      </div>

      {/* Add account dialog */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('addButton')}</DialogTitle>
            <DialogDescription>{t('subtitle')}</DialogDescription>
          </DialogHeader>
          <Input
            value={addName}
            onChange={(e) => setAddName(e.target.value)}
            placeholder={t('addDialog.namePlaceholder')}
            onKeyDown={(e) => { if (e.key === 'Enter') handleAdd() }}
            autoFocus
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setAddOpen(false)} disabled={addLoading}>
              {t('addDialog.cancel')}
            </Button>
            <Button onClick={handleAdd} disabled={!addName.trim() || addLoading}>
              {t('addDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rename dialog */}
      <Dialog open={!!renameTarget} onOpenChange={(v) => { if (!v) setRenameTarget(null) }}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('row.actions.rename')}</DialogTitle>
            <DialogDescription>{renameTarget?.name}</DialogDescription>
          </DialogHeader>
          <Input
            value={renameName}
            onChange={(e) => setRenameName(e.target.value)}
            placeholder={t('addDialog.namePlaceholder')}
            onKeyDown={(e) => { if (e.key === 'Enter') handleRenameSave() }}
            autoFocus
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenameTarget(null)} disabled={renameLoading}>
              {t('addDialog.cancel')}
            </Button>
            <Button onClick={handleRenameSave} disabled={!renameName.trim() || renameLoading}>
              {t('common:actions.save', 'Save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirm dialog */}
      <AlertDialog open={!!deleteTarget} onOpenChange={(v) => { if (!v) setDeleteTarget(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('row.actions.delete')}</AlertDialogTitle>
            <AlertDialogDescription>
              {deleteTarget ? t('delete.confirm', { name: deleteTarget.name }) : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteLoading}>
              {t('addDialog.cancel')}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDeleteConfirm} disabled={deleteLoading}>
              {t('row.actions.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Xterm login dialog */}
      {loginDialogOpen && loginToken && loginAccountId !== null && (
        <ClaudeLoginDialog
          accountId={loginAccountId}
          wsToken={loginToken}
          accountName={accounts.find((a) => a.id === loginAccountId)?.name ?? String(loginAccountId)}
          onClose={handleLoginDialogClose}
        />
      )}
    </div>
  )
}
