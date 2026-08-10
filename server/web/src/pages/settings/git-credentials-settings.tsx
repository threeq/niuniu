import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
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
import { api, ApiError } from '@/lib/api'

interface GitRemoteCredential {
  id: number
  host: string
  username: string
  public_key: string
  fingerprint: string
  last_used_at?: string
  created_at: string
  updated_at: string
}

interface ListResponse {
  items: GitRemoteCredential[]
}

const QUERY_KEY = ['me-git-remote-credentials']

export function GitCredentialsSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const [addOpen, setAddOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<GitRemoteCredential | null>(null)

  const { data, isLoading, isError } = useQuery({
    queryKey: QUERY_KEY,
    queryFn: () => api.get<ListResponse>('/me/git-remote-credentials'),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.delete<void>(`/me/git-remote-credentials/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY })
      toast.success(t('gitCredentials.deleted'))
      setPendingDelete(null)
    },
    onError: (err) => {
      const detail = err instanceof Error ? err.message : String(err)
      toast.error(t('gitCredentials.deleteFailed', { detail }))
    },
  })

  return (
    <div className="py-6 space-y-6">
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('gitCredentials.title')}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t('gitCredentials.description')}
        </p>
      </div>

      <div className="flex items-center justify-between">
        <p className="text-xs text-muted-foreground/70">
          {t('gitCredentials.hostHint')}
        </p>
        <Button size="sm" onClick={() => setAddOpen(true)}>
          {t('gitCredentials.add')}
        </Button>
      </div>

      {isError && (
        <p className="text-sm text-destructive">{t('gitCredentials.loadFailed')}</p>
      )}

      {!isError && (
        <div className="border border-border rounded-md overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-xs text-muted-foreground">
              <tr>
                <th className="text-left py-2 px-3 font-medium">{t('gitCredentials.col.host')}</th>
                <th className="text-left py-2 px-3 font-medium">{t('gitCredentials.col.username')}</th>
                <th className="text-left py-2 px-3 font-medium">{t('gitCredentials.col.fingerprint')}</th>
                <th className="py-2 px-3 w-12"></th>
              </tr>
            </thead>
            <tbody>
              {isLoading && (
                <tr>
                  <td colSpan={4} className="py-3 px-3 text-muted-foreground">{t('gitCredentials.loading')}</td>
                </tr>
              )}
              {!isLoading && data?.items?.length === 0 && (
                <tr>
                  <td colSpan={4} className="py-3 px-3 text-muted-foreground">{t('gitCredentials.empty')}</td>
                </tr>
              )}
              {data?.items?.map((cred) => (
                <tr key={cred.id} className="border-t border-border">
                  <td className="py-2 px-3 font-mono text-xs">{cred.host}</td>
                  <td className="py-2 px-3 font-mono text-xs">{cred.username}</td>
                  <td className="py-2 px-3 font-mono text-xs text-muted-foreground">{cred.fingerprint}</td>
                  <td className="py-2 px-3 text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setPendingDelete(cred)}
                      aria-label={t('gitCredentials.deleteAria', { host: cred.host })}
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="rounded-md border border-border bg-muted/40 px-4 py-3">
        <p className="text-xs text-muted-foreground">
          {t('gitCredentials.securityNote')}
        </p>
      </div>

      <AddGitCredentialDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        onCreated={() => queryClient.invalidateQueries({ queryKey: QUERY_KEY })}
      />

      <AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('gitCredentials.deleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete && t('gitCredentials.deleteConfirm', { host: pendingDelete.host })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('gitCredentials.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
            >
              {t('gitCredentials.confirmDelete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

interface AddDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
}

function AddGitCredentialDialog({ open, onOpenChange, onCreated }: AddDialogProps) {
  const { t } = useTranslation('settings')
  const [host, setHost] = useState('')
  const [username, setUsername] = useState('git')
  const [privateKey, setPrivateKey] = useState('')

  const createMutation = useMutation({
    mutationFn: () =>
      api.post<GitRemoteCredential>('/me/git-remote-credentials', {
        host: host.trim(),
        username: username.trim() || 'git',
        private_key: privateKey,
      }),
    onSuccess: () => {
      toast.success(t('gitCredentials.created'))
      setHost('')
      setUsername('git')
      setPrivateKey('')
      onOpenChange(false)
      onCreated()
    },
    onError: (err) => {
      let code = ''
      if (err instanceof ApiError) {
        const body = err.body as Record<string, unknown> | null | undefined
        code = ((body?.error as Record<string, unknown> | undefined)?.code as string) ?? ''
      }
      const messageKey: Record<string, string> = {
        ERR_INVALID_PRIVATE_KEY: 'gitCredentials.errInvalidPrivateKey',
        ERR_PASSPHRASE_REQUIRED: 'gitCredentials.errPassphraseRequired',
        ERR_INVALID_HOST: 'gitCredentials.errInvalidHost',
        ERR_HOST_CONFLICT: 'gitCredentials.errHostConflict',
      }
      if (messageKey[code]) {
        toast.error(t(messageKey[code]))
      } else {
        const detail = err instanceof Error ? err.message : String(err)
        toast.error(t('gitCredentials.createFailed', { detail }))
      }
    },
  })

  const canSubmit = host.trim() !== '' && privateKey.trim() !== '' && !createMutation.isPending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('gitCredentials.addTitle')}</DialogTitle>
          <DialogDescription>{t('gitCredentials.addDescription')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <label htmlFor="add-cred-host" className="text-sm text-muted-foreground">
              {t('gitCredentials.field.host')}
            </label>
            <Input
              id="add-cred-host"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              placeholder="github.com"
              disabled={createMutation.isPending}
            />
            <p className="text-xs text-muted-foreground/70">{t('gitCredentials.field.hostHint')}</p>
          </div>

          <div className="space-y-2">
            <label htmlFor="add-cred-username" className="text-sm text-muted-foreground">
              {t('gitCredentials.field.username')}
            </label>
            <Input
              id="add-cred-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="git"
              disabled={createMutation.isPending}
            />
            <p className="text-xs text-muted-foreground/70">{t('gitCredentials.field.usernameHint')}</p>
          </div>

          <div className="space-y-2">
            <label htmlFor="add-cred-key" className="text-sm text-muted-foreground">
              {t('gitCredentials.field.privateKey')}
            </label>
            <Textarea
              id="add-cred-key"
              value={privateKey}
              onChange={(e) => setPrivateKey(e.target.value)}
              placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;..."
              rows={8}
              disabled={createMutation.isPending}
              className="font-mono text-xs"
            />
            <p className="text-xs text-muted-foreground/70">{t('gitCredentials.field.privateKeyHint')}</p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={createMutation.isPending}>
            {t('gitCredentials.cancel')}
          </Button>
          <Button onClick={() => createMutation.mutate()} disabled={!canSubmit}>
            {createMutation.isPending ? t('gitCredentials.saving') : t('gitCredentials.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
