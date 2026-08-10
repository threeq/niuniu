import { useState } from 'react'
import { toast } from 'sonner'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, KeyRound, Trash2, Eye, EyeOff, Boxes } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { authUsersApi } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import type { AuthUser, CreateUserRequest, UpdateUserRequest } from '@/types/api'
import { UserDetailDialog } from './user-detail-dialog'

type Role = 'admin' | 'member'

function RoleBadge({ role }: { role: Role }) {
  const { t } = useTranslation('common')
  return (
    <span
      className={
        role === 'admin'
          ? 'inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-200'
          : 'inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-muted text-muted-foreground'
      }
    >
      {role === 'admin' ? t('role.admin') : t('role.member')}
    </span>
  )
}

function PasswordInput({
  value,
  onChange,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  const [show, setShow] = useState(false)
  return (
    <div className="relative">
      <Input
        type={show ? 'text' : 'password'}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
      <button
        type="button"
        onClick={() => setShow(!show)}
        className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
      >
        {show ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
      </button>
    </div>
  )
}

export function UsersSettings() {
  const { t } = useTranslation('settings')
  const me = useAuthStore((s) => s.user)
  const qc = useQueryClient()
  const { data: users = [], isLoading } = useQuery({
    queryKey: ['auth-users'],
    queryFn: authUsersApi.list,
  })

  const adminCount = users.filter((u) => u.role === 'admin').length
  const isOnlyAdmin = (u: AuthUser) => u.role === 'admin' && adminCount <= 1
  const isSelf = (u: AuthUser) => me?.id === u.id

  const [createOpen, setCreateOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<AuthUser | null>(null)
  const [resetTarget, setResetTarget] = useState<AuthUser | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AuthUser | null>(null)
  const [resourceTarget, setResourceTarget] = useState<AuthUser | null>(null)

  const refetch = () => qc.invalidateQueries({ queryKey: ['auth-users'] })

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">{t('users.title')}</h2>
          <p className="text-sm text-muted-foreground">{t('users.description')}</p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="h-4 w-4 mr-1" />
          {t('users.addUser')}
        </Button>
      </div>

      {isLoading ? (
        <div className="text-sm text-muted-foreground">{t('users.loading')}</div>
      ) : (
        <div className="border border-border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/40">
              <tr>
                <th className="text-left px-3 py-2 font-medium">{t('users.table.username')}</th>
                <th className="text-left px-3 py-2 font-medium">{t('users.table.displayName')}</th>
                <th className="text-left px-3 py-2 font-medium">{t('users.table.role')}</th>
                <th className="text-left px-3 py-2 font-medium">{t('users.table.createdAt')}</th>
                <th className="text-right px-3 py-2 font-medium">{t('users.table.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => {
                const editLocked = isSelf(u) || isOnlyAdmin(u)
                const editTip = isSelf(u)
                  ? t('users.tooltip.cantChangeOwnRole')
                  : isOnlyAdmin(u)
                    ? t('users.tooltip.needAtLeastOneAdmin')
                    : ''
                const deleteLocked = isSelf(u) || isOnlyAdmin(u)
                const deleteTip = isSelf(u)
                  ? t('users.tooltip.cantDeleteSelf')
                  : isOnlyAdmin(u)
                    ? t('users.tooltip.needAtLeastOneAdmin')
                    : ''
                return (
                  <tr key={u.id} className="border-t border-border">
                    <td className="px-3 py-2 font-mono text-xs">{u.username}</td>
                    <td className="px-3 py-2">{u.display_name}</td>
                    <td className="px-3 py-2"><RoleBadge role={u.role} /></td>
                    <td className="px-3 py-2 text-muted-foreground text-xs">
                      {new Date(u.created_at).toLocaleString()}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <div className="inline-flex items-center gap-1">
                        <ActionButton
                          icon={<Boxes className="h-3.5 w-3.5" />}
                          label={t('users.actions.manageResources')}
                          onClick={() => setResourceTarget(u)}
                        />
                        <ActionButton
                          icon={<Pencil className="h-3.5 w-3.5" />}
                          label={t('users.actions.edit')}
                          disabled={editLocked}
                          tooltip={editTip}
                          onClick={() => setEditTarget(u)}
                        />
                        <ActionButton
                          icon={<KeyRound className="h-3.5 w-3.5" />}
                          label={t('users.actions.resetPassword')}
                          onClick={() => setResetTarget(u)}
                        />
                        <ActionButton
                          icon={<Trash2 className="h-3.5 w-3.5" />}
                          label={t('users.actions.delete')}
                          disabled={deleteLocked}
                          tooltip={deleteTip}
                          danger
                          onClick={() => setDeleteTarget(u)}
                        />
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      <CreateUserDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onSuccess={refetch}
      />
      <EditUserDialog
        target={editTarget}
        onClose={() => setEditTarget(null)}
        onSuccess={refetch}
      />
      <ResetPasswordDialog
        target={resetTarget}
        onClose={() => setResetTarget(null)}
        onSuccess={refetch}
      />
      <DeleteUserDialog
        target={deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onSuccess={refetch}
      />
      <UserDetailDialog
        target={resourceTarget}
        onClose={() => setResourceTarget(null)}
        onPurged={refetch}
      />
    </div>
  )
}

function ActionButton({
  icon,
  label,
  onClick,
  disabled,
  tooltip,
  danger,
}: {
  icon: React.ReactNode
  label: string
  onClick: () => void
  disabled?: boolean
  tooltip?: string
  danger?: boolean
}) {
  const btn = (
    <button
      onClick={onClick}
      disabled={disabled}
      title={!tooltip ? label : undefined}
      className={
        'p-1 rounded ' +
        (disabled
          ? 'text-muted-foreground/40 cursor-not-allowed'
          : danger
            ? 'text-muted-foreground hover:text-destructive'
            : 'text-muted-foreground hover:text-info')
      }
    >
      {icon}
    </button>
  )
  if (disabled && tooltip) {
    return (
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger asChild>
            <span>{btn}</span>
          </TooltipTrigger>
          <TooltipContent>{tooltip}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    )
  }
  return btn
}

function CreateUserDialog({
  open,
  onClose,
  onSuccess,
}: {
  open: boolean
  onClose: () => void
  onSuccess: () => void
}) {
  const { t } = useTranslation('settings')
  const [form, setForm] = useState<CreateUserRequest>({
    username: '',
    password: '',
    display_name: '',
    role: 'member',
  })
  const m = useMutation({
    mutationFn: (body: CreateUserRequest) => authUsersApi.create(body),
    onSuccess: () => {
      toast.success(t('users.create.success'))
      onSuccess()
      onClose()
      setForm({ username: '', password: '', display_name: '', role: 'member' })
    },
    onError: (err: Error) => toast.error(err.message),
  })

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('users.create.title')}</DialogTitle>
          <DialogDescription>{t('users.create.description')}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <LabeledRow label={t('users.create.usernameLabel')}>
            <Input
              value={form.username}
              onChange={(e) => setForm({ ...form, username: e.target.value })}
              placeholder={t('users.create.usernamePlaceholder')}
            />
          </LabeledRow>
          <LabeledRow label={t('users.create.passwordLabel')}>
            <PasswordInput
              value={form.password}
              onChange={(v) => setForm({ ...form, password: v })}
              placeholder={t('users.create.passwordPlaceholder')}
            />
            <p className="mt-1 text-xs text-muted-foreground">{t('users.create.passwordHint')}</p>
          </LabeledRow>
          <LabeledRow label={t('users.create.displayNameLabel')}>
            <Input
              value={form.display_name}
              onChange={(e) => setForm({ ...form, display_name: e.target.value })}
              placeholder={t('users.create.displayNamePlaceholder')}
            />
          </LabeledRow>
          <LabeledRow label={t('users.create.roleLabel')}>
            <RoleSelect
              value={form.role}
              onChange={(r) => setForm({ ...form, role: r })}
            />
          </LabeledRow>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common:actions.cancel')}</Button>
          <Button
            onClick={() => m.mutate(form)}
            disabled={m.isPending || !form.username || form.password.length < 12}
          >
            {t('users.create.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function EditUserDialog({
  target,
  onClose,
  onSuccess,
}: {
  target: AuthUser | null
  onClose: () => void
  onSuccess: () => void
}) {
  const { t } = useTranslation('settings')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [seeded, setSeeded] = useState<number | null>(null)

  if (target && seeded !== target.id) {
    setSeeded(target.id)
    setDisplayName(target.display_name)
    setRole(target.role)
  }

  const m = useMutation({
    mutationFn: (body: UpdateUserRequest) => authUsersApi.update(target!.id, body),
    onSuccess: () => {
      toast.success(t('users.edit.success'))
      onSuccess()
      onClose()
    },
    onError: (err: Error) => toast.error(err.message),
  })

  return (
    <Dialog open={!!target} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('users.edit.title')}</DialogTitle>
          <DialogDescription>{t('users.edit.description')}</DialogDescription>
        </DialogHeader>
        {target && (
          <div className="space-y-3">
            <LabeledRow label={t('users.create.usernameLabel')}>
              <div className="text-sm font-mono text-muted-foreground py-2">
                {target.username}
              </div>
            </LabeledRow>
            <LabeledRow label={t('users.create.displayNameLabel')}>
              <Input
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
              />
            </LabeledRow>
            <LabeledRow label={t('users.create.roleLabel')}>
              <RoleSelect value={role} onChange={setRole} />
            </LabeledRow>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common:actions.cancel')}</Button>
          <Button
            onClick={() => m.mutate({ display_name: displayName, role })}
            disabled={m.isPending}
          >
            {t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ResetPasswordDialog({
  target,
  onClose,
  onSuccess,
}: {
  target: AuthUser | null
  onClose: () => void
  onSuccess: () => void
}) {
  const { t } = useTranslation('settings')
  const [pw, setPw] = useState('')

  if (target === null && pw !== '') setPw('') // reset when closed

  const m = useMutation({
    mutationFn: (password: string) => authUsersApi.resetPassword(target!.id, password),
    onSuccess: () => {
      toast.success(t('users.resetPassword.success'))
      onSuccess()
      onClose()
      setPw('')
    },
    onError: (err: Error) => toast.error(err.message),
  })

  return (
    <Dialog open={!!target} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('users.resetPassword.title')}</DialogTitle>
          <DialogDescription>
            {t('users.resetPassword.description', { name: target?.display_name || target?.username || '' })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <LabeledRow label={t('users.resetPassword.newPasswordLabel')}>
            <PasswordInput value={pw} onChange={setPw} placeholder={t('users.resetPassword.newPasswordPlaceholder')} />
            <p className="mt-1 text-xs text-muted-foreground">{t('users.resetPassword.passwordHint')}</p>
          </LabeledRow>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common:actions.cancel')}</Button>
          <Button onClick={() => m.mutate(pw)} disabled={m.isPending || pw.length < 12}>
            {t('users.resetPassword.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function DeleteUserDialog({
  target,
  onClose,
  onSuccess,
}: {
  target: AuthUser | null
  onClose: () => void
  onSuccess: () => void
}) {
  const { t } = useTranslation('settings')
  const m = useMutation({
    mutationFn: () => authUsersApi.delete(target!.id),
    onSuccess: () => {
      toast.success(t('users.delete.success'))
      onSuccess()
      onClose()
    },
    onError: (err: Error) => toast.error(err.message),
  })

  return (
    <Dialog open={!!target} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('users.delete.title')}</DialogTitle>
          <DialogDescription>
            {t('users.delete.description', { name: target?.display_name || target?.username || '' })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common:actions.cancel')}</Button>
          <Button
            variant="destructive"
            onClick={() => m.mutate()}
            disabled={m.isPending}
          >
            {t('users.delete.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function LabeledRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[80px_1fr] items-center gap-3">
      <label className="text-sm text-muted-foreground">{label}</label>
      <div>{children}</div>
    </div>
  )
}

function RoleSelect({ value, onChange }: { value: Role; onChange: (r: Role) => void }) {
  const { t } = useTranslation('common')
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value as Role)}
      className="w-full h-9 px-3 rounded-md border border-input bg-background text-sm"
    >
      <option value="member">{t('role.member')}</option>
      <option value="admin">{t('role.admin')}</option>
    </select>
  )
}
