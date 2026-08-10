import { useState } from 'react'
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
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
  listCodexAccounts,
  createCodexAccount,
  issueCodexLoginToken,
  updateCodexAccount,
  deleteCodexAccount,
  refreshCodexAccount,
  getCodexDefaultStatus,
} from '@/lib/codex-account-api'
import { openCodexLoginTerminal } from '@/lib/shell'
import { ApiError } from '@/lib/api'
import type { CodexAccount, CodexDefaultStatus } from '@/types/api'
import { CodexAccountRow } from './account-row'
import { CodexLoginDialog } from '@/components/dialogs/CodexLoginDialog'

export function CodexAccountsPage() {
  const { t } = useTranslation('codex-accounts')
  const qc = useQueryClient()
  const user = useAuthStore((s) => s.user)
  const authEnabled = useConfigStore((s) => s.authEnabled)
  const personalMode = useConfigStore((s) => s.personalMode)
  const isAdmin = authEnabled ? user?.role === 'admin' : true
  const callerID = user?.id ?? 0
  const [defaultLoginPending, setDefaultLoginPending] = useState(false)

  const { data: accounts = [], isLoading } = useQuery({
    queryKey: ['codex-accounts'],
    queryFn: listCodexAccounts,
  })

  // Auth state of the host's ~/.codex/. Only relevant in personal mode where
  // we render the synthetic default-account row; we still pass `enabled` so
  // hosted-mode pages don't hit a useless endpoint.
  const { data: defaultStatus } = useQuery({
    queryKey: ['codex-accounts', 'default-status'],
    queryFn: getCodexDefaultStatus,
    enabled: personalMode,
    refetchOnWindowFocus: true,
  })

  // Add dialog
  const [addOpen, setAddOpen] = useState(false)
  const [addName, setAddName] = useState('')
  const [addLoading, setAddLoading] = useState(false)

  // Rename dialog
  const [renameTarget, setRenameTarget] = useState<CodexAccount | null>(null)
  const [renameName, setRenameName] = useState('')
  const [renameLoading, setRenameLoading] = useState(false)

  // Delete confirm
  const [deleteTarget, setDeleteTarget] = useState<CodexAccount | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  // Login dialog (xterm)
  const [loginDialogOpen, setLoginDialogOpen] = useState(false)
  const [loginToken, setLoginToken] = useState<string | null>(null)
  const [loginAccountId, setLoginAccountId] = useState<number | null>(null)
  const [loginAccountName, setLoginAccountName] = useState('')

  const invalidate = () => qc.invalidateQueries({ queryKey: ['codex-accounts'] })

  async function handleAdd() {
    if (!addName.trim()) return
    setAddLoading(true)
    try {
      const resp = await createCodexAccount({ name: addName.trim(), visibility: 'public' })
      invalidate()
      setAddOpen(false)
      setAddName('')
      setLoginToken(resp.ws_token)
      setLoginAccountId(resp.id)
      setLoginAccountName(resp.name)
      setLoginDialogOpen(true)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.createFailed', { detail: msg }))
    } finally {
      setAddLoading(false)
    }
  }

  async function handleLogin(acct: CodexAccount) {
    try {
      const { ws_token } = await issueCodexLoginToken(acct.id)
      setLoginToken(ws_token)
      setLoginAccountId(acct.id)
      setLoginAccountName(acct.name)
      setLoginDialogOpen(true)
    } catch (err: unknown) {
      // Translate the targeted 409 `already_authed` envelope. The backend
      // returns `{error,code,message}` with code='already_authed' when a row
      // is already active and must be deleted+recreated to switch creds —
      // see server/internal/api/codex_account.go:IssueLoginToken.
      const body = err instanceof ApiError ? (err.body as { code?: string; error?: string; message?: string } | null) : null
      const code = body?.code ?? body?.error
      if (err instanceof ApiError && err.status === 409 && code === 'already_authed') {
        toast.error(t('errors.already_authed'))
        return
      }
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.loginTokenFailed', { detail: msg }))
    }
  }

  // Login flow for the synthetic "system default" row: bypasses the managed
  // PTY path and instead opens a fresh OS terminal so the user can finish
  // `codex login` against the host's native ~/.codex/. Mirrors the Claude
  // pattern in claude-accounts/index.tsx.
  async function handleDefaultLogin() {
    if (defaultLoginPending) return
    setDefaultLoginPending(true)
    try {
      await openCodexLoginTerminal()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.defaultLoginFailed', { detail: msg }))
    } finally {
      // 3-second debounce so a stuck terminal window doesn't let the user
      // spam-spawn extras.
      setTimeout(() => setDefaultLoginPending(false), 3000)
    }
  }

  // Manually re-probe the host's ~/.codex/auth.json — useful after the user
  // has just completed `codex login` in the terminal window we spawned, since
  // we have no way to know when that finishes from the SPA.
  function handleDefaultRefresh() {
    qc.invalidateQueries({ queryKey: ['codex-accounts', 'default-status'] })
  }

  async function handleRefresh(acct: CodexAccount) {
    try {
      await refreshCodexAccount(acct.id)
      invalidate()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.refreshFailed', { detail: msg }))
    }
  }

  function startRename(acct: CodexAccount) {
    setRenameTarget(acct)
    setRenameName(acct.name)
  }

  async function commitRename() {
    if (!renameTarget || !renameName.trim()) return
    setRenameLoading(true)
    try {
      await updateCodexAccount(renameTarget.id, { name: renameName.trim() })
      invalidate()
      setRenameTarget(null)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.renameFailed', { detail: msg }))
    } finally {
      setRenameLoading(false)
    }
  }

  async function handleToggleVisibility(acct: CodexAccount) {
    const next = acct.visibility === 'public' ? 'private' : 'public'
    try {
      await updateCodexAccount(acct.id, { visibility: next })
      invalidate()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.renameFailed', { detail: msg }))
    }
  }

  async function commitDelete() {
    if (!deleteTarget) return
    setDeleteLoading(true)
    try {
      await deleteCodexAccount(deleteTarget.id)
      invalidate()
      setDeleteTarget(null)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error(t('errors.deleteFailed', { detail: msg }))
    } finally {
      setDeleteLoading(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-xl font-semibold">{t('title')}</h2>
          <p className="text-sm text-muted-foreground mt-1">{t('subtitle')}</p>
        </div>
        {isAdmin && (
          <Button onClick={() => setAddOpen(true)} size="sm">
            <Plus className="h-4 w-4 mr-1.5" />
            {t('addButton')}
          </Button>
        )}
      </div>

      {isLoading ? (
        <div className="text-sm text-muted-foreground py-8 text-center">…</div>
      ) : (
        <div className="space-y-2">
          {/* System default row: shown only in personal/embedded mode where
              the host's ~/.codex/ is the natural fallback for unbound
              workspaces. In team mode the server-host's home directory is
              not meaningful per-user, so we hide it entirely. */}
          {personalMode && (
            <DefaultCodexRow
              status={defaultStatus}
              loginPending={defaultLoginPending}
              onLogin={handleDefaultLogin}
              onRefresh={handleDefaultRefresh}
            />
          )}
          {accounts.length === 0 && !personalMode && (
            <div className="text-center py-12 space-y-1">
              <div className="font-medium">{t('empty.title')}</div>
              <div className="text-sm text-muted-foreground">{t('empty.body')}</div>
            </div>
          )}
          {accounts.map((acct) => (
            <CodexAccountRow
              key={acct.id}
              account={acct}
              isAdmin={isAdmin}
              callerID={callerID}
              onLogin={handleLogin}
              onRename={startRename}
              onToggleVisibility={handleToggleVisibility}
              onDelete={setDeleteTarget}
              onRefresh={handleRefresh}
            />
          ))}
        </div>
      )}

      {/* Add dialog */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('addDialog.title')}</DialogTitle>
            <DialogDescription>{t('addDialog.description')}</DialogDescription>
          </DialogHeader>
          <Input
            placeholder={t('addDialog.namePlaceholder')}
            value={addName}
            onChange={(e) => setAddName(e.target.value)}
            disabled={addLoading}
            autoFocus
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setAddOpen(false)} disabled={addLoading}>
              {t('addDialog.cancel')}
            </Button>
            <Button onClick={handleAdd} disabled={addLoading || !addName.trim()}>
              {t('addDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rename dialog */}
      <Dialog open={!!renameTarget} onOpenChange={(v) => !v && setRenameTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('renameDialog.title')}</DialogTitle>
          </DialogHeader>
          <Input
            placeholder={t('renameDialog.namePlaceholder')}
            value={renameName}
            onChange={(e) => setRenameName(e.target.value)}
            disabled={renameLoading}
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenameTarget(null)} disabled={renameLoading}>
              {t('renameDialog.cancel')}
            </Button>
            <Button onClick={commitRename} disabled={renameLoading || !renameName.trim()}>
              {t('renameDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirm */}
      <AlertDialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('deleteDialog.title', { name: deleteTarget?.name })}
            </AlertDialogTitle>
            <AlertDialogDescription>{t('deleteDialog.description')}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteLoading}>
              {t('deleteDialog.cancel')}
            </AlertDialogCancel>
            <AlertDialogAction onClick={commitDelete} disabled={deleteLoading}>
              {t('deleteDialog.confirm')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* PTY login terminal */}
      {loginDialogOpen && loginToken && loginAccountId && (
        <CodexLoginDialog
          accountId={loginAccountId}
          wsToken={loginToken}
          accountName={loginAccountName}
          onClose={() => {
            setLoginDialogOpen(false)
            setLoginToken(null)
            setLoginAccountId(null)
            setLoginAccountName('')
            invalidate()
          }}
        />
      )}
    </div>
  )
}

// DefaultCodexRow renders the host's native ~/.codex/ as a read-only row at
// the top of the accounts list. Unlike managed rows, this one is not backed
// by a DB record — the codex backend deliberately omits a "default" account
// (see service/codex_account.go). The row exists only so personal-mode users
// can see at a glance that an unbound workspace will fall back to ~/.codex/,
// and run `codex login` against it via the OS terminal without leaving the
// settings page.
//
// `status` is optional because the React Query fetch may still be in flight
// on first render; we treat undefined as "unknown" and skip the badge until
// it resolves rather than flashing a misleading "未登录" state.
function DefaultCodexRow({
  status,
  loginPending,
  onLogin,
  onRefresh,
}: {
  status: CodexDefaultStatus | undefined
  loginPending: boolean
  onLogin: () => void
  onRefresh: () => void
}) {
  const { t } = useTranslation('codex-accounts')
  const isActive = status?.status === 'active'
  return (
    <div className="flex items-center justify-between gap-3 border border-border rounded-md px-4 py-3">
      <div className="flex flex-col min-w-0 gap-1">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="font-medium truncate">{t('defaultRow.name')}</span>
          <Badge variant="secondary">{t('defaultRow.badge')}</Badge>
          {status && (
            <Badge variant={isActive ? 'default' : 'outline'}>
              {t(`status.${status.status}`)}
            </Badge>
          )}
        </div>
        <div className="text-xs text-muted-foreground truncate">
          {isActive && status?.email
            ? t('defaultRow.signedInAs', { email: status.email })
            : t('defaultRow.description')}
        </div>
      </div>
      <div className="flex items-center gap-1.5">
        <Button size="sm" variant="ghost" onClick={onRefresh}>
          {t('row.actions.refresh')}
        </Button>
        <Button size="sm" variant={isActive ? 'outline' : 'default'} disabled={loginPending} onClick={onLogin}>
          {isActive ? t('defaultRow.relogin') : t('row.actions.login')}
        </Button>
      </div>
    </div>
  )
}
