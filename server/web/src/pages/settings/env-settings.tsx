import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Pencil, ChevronDown, ChevronRight, Package, KeyRound, Server, CheckSquare } from 'lucide-react'
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
import { api } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import { OwnerBadge } from '@/components/shared/owner-badge'
import { OwnerPicker } from '@/components/shared/owner-picker'
import { useAuthStore } from '@/stores/auth-store'
import type { OwnerRef } from '@/types/org'
import type { EnvPreset, CreateEnvPresetData, EnvAccount, CreateEnvAccountData, EnvProvider, CreateEnvProviderData } from '@/types/api'

const PROVIDER_CLI_TYPES = ['claude', 'codex', 'qwen', 'omp', 'goose'] as const
type ProviderCliType = (typeof PROVIDER_CLI_TYPES)[number]

function PresetCard({ preset, onEdit, onDelete }: { preset: EnvPreset; onEdit: (p: EnvPreset) => void; onDelete: (id: number) => void }) {
  const { t } = useTranslation('settings')
  const [expanded, setExpanded] = useState(false)
  const envEntries = Object.entries(preset.env)

  return (
    <div className="border border-border rounded-lg p-4">
      <div className="flex items-center justify-between">
        <button
          onClick={() => setExpanded(!expanded)}
          className="flex items-center gap-2 text-left flex-1 min-w-0"
        >
          {expanded ? <ChevronDown className="h-4 w-4 text-muted-foreground flex-shrink-0" /> : <ChevronRight className="h-4 w-4 text-muted-foreground flex-shrink-0" />}
          <Package className="h-4 w-4 text-info flex-shrink-0" />
          <span className="font-medium text-sm text-foreground truncate">{preset.name}</span>
          {preset.owner && <OwnerBadge owner={preset.owner as OwnerRef} />}
          {preset.description && (
            <span className="text-xs text-muted-foreground truncate">{preset.description}</span>
          )}
        </button>
        <div className="flex items-center gap-1 flex-shrink-0 ml-2">
          <button
            onClick={() => onEdit(preset)}
            className="p-1 text-muted-foreground hover:text-info"
          >
            <Pencil className="h-3.5 w-3.5" />
          </button>
          <button
            onClick={async () => {
              if (!(await confirm(t('env.deleteConfirm', { name: preset.name })))) return
              onDelete(preset.id)
            }}
            className="p-1 text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      {expanded && envEntries.length > 0 && (
        <div className="mt-3 ml-6 space-y-1">
          {envEntries.map(([key, value]) => (
            <div key={key} className="flex items-center gap-2 text-xs font-mono">
              <span className="text-success font-medium">{key}</span>
              <span className="text-muted-foreground">=</span>
              <span className="text-foreground">{value}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// Mask a stored API key so it is never shown in full: keep the first 4 and
// last 4 characters, collapse the middle to "…". Empty/short keys stay as-is.
function maskKey(key: string): string {
  if (!key) return ''
  if (key.length <= 12) return '••••••'
  return `${key.slice(0, 4)}…${key.slice(-4)}`
}

function AccountCard({ account, onEdit, onDelete }: { account: EnvAccount; onEdit: (a: EnvAccount) => void; onDelete: (id: number) => void }) {
  const { t } = useTranslation('settings')
  return (
    <div className="border border-border rounded-lg p-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-left flex-1 min-w-0">
          <KeyRound className="h-4 w-4 text-info flex-shrink-0" />
          <span className="font-medium text-sm text-foreground truncate">{account.name}</span>
          {account.platform && (
            <span className="text-xs text-muted-foreground truncate">{account.platform}</span>
          )}
          {account.description && (
            <span className="text-xs text-muted-foreground truncate">{account.description}</span>
          )}
          <code className="text-xs text-muted-foreground font-mono">{maskKey(account.api_key)}</code>
        </div>
        <div className="flex items-center gap-1 flex-shrink-0 ml-2">
          <button
            onClick={() => onEdit(account)}
            className="p-1 text-muted-foreground hover:text-info"
          >
            <Pencil className="h-3.5 w-3.5" />
          </button>
          <button
            onClick={async () => {
              if (!(await confirm(t('env.accountDeleteConfirm', { name: account.name })))) return
              onDelete(account.id)
            }}
            className="p-1 text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </div>
  )
}

function ProviderCard({ provider, onEdit, onDelete, onImport }: {
  provider: EnvProvider
  onEdit: (p: EnvProvider) => void
  onDelete: (id: number) => void
  onImport: (p: EnvProvider) => void
}) {
  const { t } = useTranslation('settings')
  const [showPreview, setShowPreview] = useState(false)
  const [cliType, setCliType] = useState<ProviderCliType>('claude')
  const { data: previewEnv = {} } = useQuery({
    queryKey: ['provider-env', provider.id, cliType],
    queryFn: () => api.getProviderEnv(provider.id, cliType),
    enabled: showPreview,
  })

  const cliLabel = (c: string) => t(`env.providerCliType${c.charAt(0).toUpperCase()}${c.slice(1)}`)

  return (
    <div className="border border-border rounded-lg p-4">
      <div className="flex items-center justify-between gap-2">
        <button
          onClick={() => setShowPreview((v) => !v)}
          className="flex items-center gap-2 text-left flex-1 min-w-0"
        >
          <Server className="h-4 w-4 text-info flex-shrink-0" />
          <span className="font-medium text-sm text-foreground truncate">{provider.name}</span>
          {provider.platform && (
            <span className="text-xs text-muted-foreground truncate">{provider.platform}</span>
          )}
          {provider.base_url && (
            <span className="text-xs text-muted-foreground font-mono truncate">{provider.base_url}</span>
          )}
          {provider.model && (
            <span className="text-xs text-muted-foreground truncate">model: {provider.model}</span>
          )}
        </button>
        <div className="flex items-center gap-1 flex-shrink-0 ml-2">
          <Button size="sm" variant="outline" onClick={() => onImport(provider)}>
            <CheckSquare className="h-3.5 w-3.5 mr-1" />
            {t('env.providerImportBtn')}
          </Button>
          <button onClick={() => onEdit(provider)} className="p-1 text-muted-foreground hover:text-info">
            <Pencil className="h-3.5 w-3.5" />
          </button>
          <button
            onClick={async () => {
              if (!(await confirm(t('env.providerDeleteConfirm', { name: provider.name })))) return
              onDelete(provider.id)
            }}
            className="p-1 text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      {showPreview && (
        <div className="mt-3 ml-6">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-xs text-muted-foreground">{t('env.providerCliTypeLabel')}</span>
            <select
              value={cliType}
              onChange={(e) => setCliType(e.target.value as ProviderCliType)}
              className="rounded border border-border bg-background px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
            >
              {PROVIDER_CLI_TYPES.map((c) => (
                <option key={c} value={c}>{cliLabel(c)}</option>
              ))}
            </select>
            <span className="text-xs text-muted-foreground">{t('env.providerPreviewLabel')}</span>
          </div>
          <div className="space-y-1">
            {Object.entries(previewEnv).map(([k, v]) => (
              <div key={k} className="flex items-center gap-2 text-xs font-mono">
                <span className="text-success font-medium">{k}</span>
                <span className="text-muted-foreground">=</span>
                <span className="text-foreground break-all">{maskKey(v)}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

export function EnvSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((s) => s.user)
  const { data: presets = [], isLoading } = useQuery({
    queryKey: ['env-presets'],
    queryFn: () => api.listEnvPresets(),
  })

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingPreset, setEditingPreset] = useState<EnvPreset | null>(null)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [envVars, setEnvVars] = useState<{ key: string; value: string }[]>([])
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: currentUser?.id ?? 0 })

  const createMutation = useMutation({
    mutationFn: (data: CreateEnvPresetData) => api.createEnvPreset(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-presets'] })
      setDialogOpen(false)
    },
  })

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: CreateEnvPresetData }) => api.updateEnvPreset(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-presets'] })
      setDialogOpen(false)
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.deleteEnvPreset(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-presets'] })
    },
  })

  // --- Accounts ---
  const { data: accounts = [], isLoading: accountsLoading } = useQuery({
    queryKey: ['env-accounts'],
    queryFn: () => api.listEnvAccounts(),
  })

  const [accountDialogOpen, setAccountDialogOpen] = useState(false)
  const [editingAccount, setEditingAccount] = useState<EnvAccount | null>(null)
  const [accountName, setAccountName] = useState('')
  const [accountPlatform, setAccountPlatform] = useState('')
  const [accountDesc, setAccountDesc] = useState('')
  const [accountKey, setAccountKey] = useState('')
  const [accountOwner, setAccountOwner] = useState<OwnerRef>({ type: 'user', id: currentUser?.id ?? 0 })

  const createAccountMutation = useMutation({
    mutationFn: (data: CreateEnvAccountData) => api.createEnvAccount(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-accounts'] })
      setAccountDialogOpen(false)
    },
  })

  const updateAccountMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: CreateEnvAccountData }) => api.updateEnvAccount(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-accounts'] })
      setAccountDialogOpen(false)
    },
  })

  const deleteAccountMutation = useMutation({
    mutationFn: (id: number) => api.deleteEnvAccount(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-accounts'] })
    },
  })

  const openCreateAccountDialog = () => {
    setEditingAccount(null)
    setAccountName('')
    setAccountPlatform('')
    setAccountDesc('')
    setAccountKey('')
    setAccountOwner({ type: 'user', id: currentUser?.id ?? 0 })
    setAccountDialogOpen(true)
  }

  const openEditAccountDialog = (account: EnvAccount) => {
    setEditingAccount(account)
    setAccountName(account.name)
    setAccountPlatform(account.platform)
    setAccountDesc(account.description)
    setAccountKey(account.api_key)
    setAccountDialogOpen(true)
  }

  const handleSaveAccount = () => {
    const data: CreateEnvAccountData = {
      name: accountName,
      platform: accountPlatform,
      description: accountDesc,
      api_key: accountKey,
      owner: editingAccount ? undefined : accountOwner,
    }
    if (editingAccount) {
      updateAccountMutation.mutate({ id: editingAccount.id, data })
    } else {
      createAccountMutation.mutate(data)
    }
  }

  const isSavingAccount = createAccountMutation.isPending || updateAccountMutation.isPending

  // --- Providers ---
  const { data: providers = [], isLoading: providersLoading } = useQuery({
    queryKey: ['env-providers'],
    queryFn: () => api.listEnvProviders(),
  })

  const [providerDialogOpen, setProviderDialogOpen] = useState(false)
  const [editingProvider, setEditingProvider] = useState<EnvProvider | null>(null)
  const [provName, setProvName] = useState('')
  const [provPlatform, setProvPlatform] = useState('')
  const [provDesc, setProvDesc] = useState('')
  const [provBaseUrl, setProvBaseUrl] = useState('')
  const [provProtocol, setProvProtocol] = useState('anthropic')
  const [provApiKey, setProvApiKey] = useState('')
  const [provModel, setProvModel] = useState('')
  const [provHaiku, setProvHaiku] = useState('')
  const [provSonnet, setProvSonnet] = useState('')
  const [provOpus, setProvOpus] = useState('')
  const [provSubagent, setProvSubagent] = useState('')
  const [provExtra, setProvExtra] = useState<{ key: string; value: string }[]>([])
  const [provOwner, setProvOwner] = useState<OwnerRef>({ type: 'user', id: currentUser?.id ?? 0 })

  const createProviderMutation = useMutation({
    mutationFn: (data: CreateEnvProviderData) => api.createEnvProvider(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-providers'] })
      setProviderDialogOpen(false)
    },
  })
  const updateProviderMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: CreateEnvProviderData }) => api.updateEnvProvider(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-providers'] })
      setProviderDialogOpen(false)
    },
  })
  const deleteProviderMutation = useMutation({
    mutationFn: (id: number) => api.deleteEnvProvider(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['env-providers'] }),
  })

  const openCreateProviderDialog = () => {
    setEditingProvider(null)
    setProvName(''); setProvPlatform(''); setProvDesc(''); setProvBaseUrl(''); setProvProtocol('anthropic'); setProvApiKey('')
    setProvModel(''); setProvHaiku(''); setProvSonnet(''); setProvOpus(''); setProvSubagent('')
    setProvExtra([])
    setProvOwner({ type: 'user', id: currentUser?.id ?? 0 })
    setProviderDialogOpen(true)
  }
  const openEditProviderDialog = (p: EnvProvider) => {
    setEditingProvider(p)
    setProvName(p.name); setProvPlatform(p.platform); setProvDesc(p.description); setProvBaseUrl(p.base_url); setProvProtocol(p.protocol || 'anthropic'); setProvApiKey(p.api_key)
    setProvModel(p.model); setProvHaiku(p.haiku_model); setProvSonnet(p.sonnet_model); setProvOpus(p.opus_model); setProvSubagent(p.subagent_model)
    setProvExtra(Object.entries(p.extra_env ?? {}).map(([key, value]) => ({ key, value })))
    setProviderDialogOpen(true)
  }
  const handleSaveProvider = () => {
    const extra: Record<string, string> = {}
    for (const { key, value } of provExtra) {
      if (key.trim()) extra[key.trim()] = value
    }
    const data: CreateEnvProviderData = {
      name: provName, platform: provPlatform, description: provDesc,
      base_url: provBaseUrl, protocol: provProtocol, api_key: provApiKey,
      model: provModel, haiku_model: provHaiku, sonnet_model: provSonnet, opus_model: provOpus, subagent_model: provSubagent,
      extra_env: extra,
      owner: editingProvider ? undefined : provOwner,
    }
    if (editingProvider) updateProviderMutation.mutate({ id: editingProvider.id, data })
    else createProviderMutation.mutate(data)
  }
  const isSavingProvider = createProviderMutation.isPending || updateProviderMutation.isPending

  // Import provider → preset
  const [importTarget, setImportTarget] = useState<EnvProvider | null>(null)
  const [importCliType, setImportCliType] = useState<ProviderCliType>('claude')
  const [importPresetName, setImportPresetName] = useState('')
  const [importOverwrite, setImportOverwrite] = useState(false)
  const importMutation = useMutation({
    mutationFn: () => api.importProvider(importTarget!.id, {
      cli_type: importCliType,
      preset_name: importPresetName || undefined,
      overwrite: importOverwrite,
    }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['env-presets'] })
      setImportTarget(null)
    },
  })
  const openImportDialog = (p: EnvProvider) => {
    setImportTarget(p)
    setImportCliType('claude')
    setImportPresetName(p.name)
    setImportOverwrite(false)
  }

  const openCreateDialog = () => {
    setEditingPreset(null)
    setName('')
    setDescription('')
    setEnvVars([])
    setOwner({ type: 'user', id: currentUser?.id ?? 0 })
    setDialogOpen(true)
  }

  const openEditDialog = (preset: EnvPreset) => {
    setEditingPreset(preset)
    setName(preset.name)
    setDescription(preset.description)
    setEnvVars(
      Object.entries(preset.env).map(([key, value]) => ({ key, value }))
    )
    setDialogOpen(true)
  }

  const handleSave = () => {
    const env: Record<string, string> = {}
    for (const { key, value } of envVars) {
      if (key.trim()) {
        env[key.trim()] = value
      }
    }
    const data: CreateEnvPresetData = { name, description, env, owner: editingPreset ? undefined : owner }

    if (editingPreset) {
      updateMutation.mutate({ id: editingPreset.id, data })
    } else {
      createMutation.mutate(data)
    }
  }

  const isSaving = createMutation.isPending || updateMutation.isPending

  return (
    <div className="space-y-4">
      {/* Subscription platform providers (unified config → per-agent env) */}
      <div>
        <div className="flex items-center justify-between">
          <div className="min-w-0">
            <h3 className="text-sm font-medium text-foreground">{t('env.providersTitle')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('env.providersDescription')}
            </p>
          </div>
          <Button size="sm" onClick={openCreateProviderDialog} className="flex-shrink-0">
            <Plus className="h-3.5 w-3.5 mr-1" />
            {t('env.newProvider')}
          </Button>
        </div>

        {providersLoading ? (
          <p className="mt-3 text-sm text-muted-foreground">{t('common:actions.loading')}</p>
        ) : providers.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">{t('env.noProviders')}</p>
        ) : (
          <div className="mt-3 space-y-3">
            {providers.map((p) => (
              <ProviderCard
                key={p.id}
                provider={p}
                onEdit={openEditProviderDialog}
                onDelete={(id) => deleteProviderMutation.mutate(id)}
                onImport={openImportDialog}
              />
            ))}
          </div>
        )}
      </div>

      {/* Subscription platform accounts */}
      <div className="border-t border-border pt-4">
        <div className="flex items-center justify-between">
          <div className="min-w-0">
            <h3 className="text-sm font-medium text-foreground">{t('env.accountsTitle')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('env.accountsDescription')}
            </p>
          </div>
          <Button size="sm" onClick={openCreateAccountDialog} className="flex-shrink-0">
            <Plus className="h-3.5 w-3.5 mr-1" />
            {t('env.newAccount')}
          </Button>
        </div>

        {accountsLoading ? (
          <p className="mt-3 text-sm text-muted-foreground">{t('common:actions.loading')}</p>
        ) : accounts.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">{t('env.noAccounts')}</p>
        ) : (
          <div className="mt-3 space-y-3">
            {accounts.map((account) => (
              <AccountCard
                key={account.id}
                account={account}
                onEdit={openEditAccountDialog}
                onDelete={(id) => deleteAccountMutation.mutate(id)}
              />
            ))}
          </div>
        )}
      </div>

      {/* Env presets */}
      <div className="border-t border-border pt-4">
        <div className="flex items-center justify-between">
          <p className="text-sm text-muted-foreground">
            {t('env.description')}
          </p>
          <Button size="sm" onClick={openCreateDialog} className="flex-shrink-0">
            <Plus className="h-3.5 w-3.5 mr-1" />
            {t('env.newPreset')}
          </Button>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {t('env.oneShotHint')}
        </p>

        {isLoading ? (
          <p className="mt-3 text-sm text-muted-foreground">{t('common:actions.loading')}</p>
        ) : presets.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">{t('env.noPresets')}</p>
        ) : (
          <div className="mt-3 space-y-3">
            {presets.map((preset) => (
              <PresetCard
                key={preset.id}
                preset={preset}
                onEdit={openEditDialog}
                onDelete={(id) => deleteMutation.mutate(id)}
              />
            ))}
          </div>
        )}
      </div>

      {/* Create/Edit Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg max-h-[85vh]">
          <DialogHeader>
            <DialogTitle>{editingPreset ? t('env.dialog.editTitle') : t('env.dialog.createTitle')}</DialogTitle>
            <DialogDescription>
              {t('env.dialog.description')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            {!editingPreset && (
              <OwnerPicker
                value={owner}
                onChange={setOwner}
                userId={currentUser?.id ?? 0}
                autoSelectDefault={true}
              />
            )}
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.dialog.nameLabel')}</label>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('env.dialog.namePlaceholder')}
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.dialog.descLabel')}</label>
              <Input
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t('env.dialog.descPlaceholder')}
              />
            </div>

            <div>
              <div className="flex items-center justify-between mb-1">
                <label className="block text-sm font-medium text-foreground">{t('env.dialog.envVarsLabel')}</label>
                <button
                  onClick={() => setEnvVars((prev) => [...prev, { key: '', value: '' }])}
                  className="flex items-center gap-0.5 text-xs text-info hover:text-info/80"
                >
                  <Plus className="h-3 w-3" />
                  {t('common:actions.add')}
                </button>
              </div>
              {envVars.length === 0 ? (
                <p className="text-xs text-muted-foreground italic">{t('env.dialog.noEnvVars')}</p>
              ) : (
                <div className="space-y-1.5">
                  {envVars.map((item, index) => (
                    <div key={index} className="flex items-center gap-1.5">
                      <input
                        type="text"
                        value={item.key}
                        onChange={(e) =>
                          setEnvVars((prev) =>
                            prev.map((v, i) => (i === index ? { ...v, key: e.target.value } : v))
                          )
                        }
                        placeholder="KEY"
                        className="flex-1 min-w-0 rounded border border-border px-2 py-1 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
                      />
                      <span className="text-muted-foreground text-xs">=</span>
                      <input
                        type="text"
                        value={item.value}
                        onChange={(e) =>
                          setEnvVars((prev) =>
                            prev.map((v, i) => (i === index ? { ...v, value: e.target.value } : v))
                          )
                        }
                        placeholder="value"
                        className="flex-1 min-w-0 rounded border border-border px-2 py-1 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
                      />
                      <button
                        onClick={() => setEnvVars((prev) => prev.filter((_, i) => i !== index))}
                        className="p-1 text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button onClick={handleSave} disabled={isSaving || !name.trim()}>
              {isSaving ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Account Create/Edit Dialog */}
      <Dialog open={accountDialogOpen} onOpenChange={setAccountDialogOpen}>
        <DialogContent className="max-w-lg max-h-[85vh]">
          <DialogHeader>
            <DialogTitle>{editingAccount ? t('env.account.editTitle') : t('env.account.createTitle')}</DialogTitle>
            <DialogDescription>
              {t('env.account.description')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            {!editingAccount && (
              <OwnerPicker
                value={accountOwner}
                onChange={setAccountOwner}
                userId={currentUser?.id ?? 0}
                autoSelectDefault={true}
              />
            )}
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.account.nameLabel')}</label>
              <Input
                value={accountName}
                onChange={(e) => setAccountName(e.target.value)}
                placeholder={t('env.account.namePlaceholder')}
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.account.platformLabel')}</label>
              <Input
                value={accountPlatform}
                onChange={(e) => setAccountPlatform(e.target.value)}
                placeholder={t('env.account.platformPlaceholder')}
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.account.descLabel')}</label>
              <Input
                value={accountDesc}
                onChange={(e) => setAccountDesc(e.target.value)}
                placeholder={t('env.account.descPlaceholder')}
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.account.apiKeyLabel')}</label>
              <Input
                value={accountKey}
                onChange={(e) => setAccountKey(e.target.value)}
                placeholder={t('env.account.apiKeyPlaceholder')}
                type="password"
                autoComplete="new-password"
              />
              <p className="mt-1 text-xs text-muted-foreground">{t('env.account.apiKeyHint')}</p>
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setAccountDialogOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button onClick={handleSaveAccount} disabled={isSavingAccount || !accountName.trim()}>
              {isSavingAccount ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Provider Create/Edit Dialog */}
      <Dialog open={providerDialogOpen} onOpenChange={setProviderDialogOpen}>
        <DialogContent className="max-w-lg max-h-[90vh]">
          <DialogHeader>
            <DialogTitle>{editingProvider ? t('env.provider.editTitle') : t('env.provider.createTitle')}</DialogTitle>
            <DialogDescription>{t('env.provider.description')}</DialogDescription>
          </DialogHeader>

          <div className="space-y-4 overflow-y-auto pr-1">
            {!editingProvider && (
              <OwnerPicker
                value={provOwner}
                onChange={setProvOwner}
                userId={currentUser?.id ?? 0}
                autoSelectDefault={true}
              />
            )}
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.nameLabel')}</label>
                <Input value={provName} onChange={(e) => setProvName(e.target.value)} placeholder={t('env.provider.namePlaceholder')} />
              </div>
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.platformLabel')}</label>
                <Input value={provPlatform} onChange={(e) => setProvPlatform(e.target.value)} placeholder={t('env.provider.platformPlaceholder')} />
              </div>
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.descLabel')}</label>
              <Input value={provDesc} onChange={(e) => setProvDesc(e.target.value)} placeholder={t('env.provider.descPlaceholder')} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.baseUrlLabel')}</label>
                <Input value={provBaseUrl} onChange={(e) => setProvBaseUrl(e.target.value)} placeholder={t('env.provider.baseUrlPlaceholder')} />
              </div>
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.protocolLabel')}</label>
                <select
                  value={provProtocol}
                  onChange={(e) => setProvProtocol(e.target.value)}
                  className="w-full rounded border border-border bg-background px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
                >
                  <option value="anthropic">{t('env.provider.protocolAnthropic')}</option>
                  <option value="openai">{t('env.provider.protocolOpenai')}</option>
                </select>
              </div>
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.apiKeyLabel')}</label>
              <select
                value={accounts.some((a) => provApiKey === `\${ACCOUNT:${a.name}}`) ? accounts.find((a) => provApiKey === `\${ACCOUNT:${a.name}}`)!.name : ''}
                onChange={(e) => setProvApiKey(e.target.value ? `\${ACCOUNT:${e.target.value}}` : '')}
                className="w-full rounded border border-border bg-background px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
              >
                <option value="">{t('env.provider.apiKeyAccountNone')}</option>
                {accounts.map((a) => (
                  <option key={a.id} value={a.name}>{a.name}</option>
                ))}
              </select>
              <p className="mt-1 text-xs text-muted-foreground">{t('env.provider.apiKeyHint')}</p>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.modelLabel')}</label>
                <Input value={provModel} onChange={(e) => setProvModel(e.target.value)} />
              </div>
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.haikuModelLabel')}</label>
                <Input value={provHaiku} onChange={(e) => setProvHaiku(e.target.value)} />
              </div>
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.sonnetModelLabel')}</label>
                <Input value={provSonnet} onChange={(e) => setProvSonnet(e.target.value)} />
              </div>
              <div>
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.opusModelLabel')}</label>
                <Input value={provOpus} onChange={(e) => setProvOpus(e.target.value)} />
              </div>
              <div className="col-span-2">
                <label className="block text-sm font-medium text-foreground mb-1">{t('env.provider.subagentModelLabel')}</label>
                <Input value={provSubagent} onChange={(e) => setProvSubagent(e.target.value)} />
              </div>
            </div>
            <div>
              <div className="flex items-center justify-between mb-1">
                <label className="block text-sm font-medium text-foreground">{t('env.provider.extraEnvLabel')}</label>
                <button onClick={() => setProvExtra((prev) => [...prev, { key: '', value: '' }])} className="flex items-center gap-0.5 text-xs text-info hover:text-info/80">
                  <Plus className="h-3 w-3" />
                  {t('common:actions.add')}
                </button>
              </div>
              {provExtra.length === 0 ? (
                <p className="text-xs text-muted-foreground italic">{t('env.provider.extraEnvHint')}</p>
              ) : (
                <div className="space-y-1.5">
                  {provExtra.map((item, index) => (
                    <div key={index} className="flex items-center gap-1.5">
                      <input
                        type="text"
                        value={item.key}
                        onChange={(e) => setProvExtra((prev) => prev.map((v, i) => (i === index ? { ...v, key: e.target.value } : v)))}
                        placeholder="KEY"
                        className="flex-1 min-w-0 rounded border border-border px-2 py-1 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
                      />
                      <span className="text-muted-foreground text-xs">=</span>
                      <input
                        type="text"
                        value={item.value}
                        onChange={(e) => setProvExtra((prev) => prev.map((v, i) => (i === index ? { ...v, value: e.target.value } : v)))}
                        placeholder="value"
                        className="flex-1 min-w-0 rounded border border-border px-2 py-1 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-ring"
                      />
                      <button onClick={() => setProvExtra((prev) => prev.filter((_, i) => i !== index))} className="p-1 text-muted-foreground hover:text-destructive">
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setProviderDialogOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button onClick={handleSaveProvider} disabled={isSavingProvider || !provName.trim()}>
              {isSavingProvider ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Import Provider → Preset Dialog */}
      <Dialog open={!!importTarget} onOpenChange={(o) => !o && setImportTarget(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('env.providerImportTitle')}</DialogTitle>
            <DialogDescription>{t('env.providerImportDesc')}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.providerCliTypeLabel')}</label>
              <select
                value={importCliType}
                onChange={(e) => setImportCliType(e.target.value as ProviderCliType)}
                className="w-full rounded border border-border bg-background px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
              >
                {PROVIDER_CLI_TYPES.map((c) => (
                  <option key={c} value={c}>{t(`env.providerCliType${c.charAt(0).toUpperCase()}${c.slice(1)}`)}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-sm font-medium text-foreground mb-1">{t('env.providerPresetNameLabel')}</label>
              <Input value={importPresetName} onChange={(e) => setImportPresetName(e.target.value)} />
            </div>
            <label className="flex items-center gap-2 text-sm text-foreground">
              <input type="checkbox" checked={importOverwrite} onChange={(e) => setImportOverwrite(e.target.checked)} className="h-4 w-4" />
              {t('env.providerOverwriteLabel')}
            </label>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportTarget(null)}>{t('common:actions.cancel')}</Button>
            <Button onClick={() => importMutation.mutate()} disabled={importMutation.isPending || !importPresetName.trim()}>
              {importMutation.isPending ? t('common:actions.saving') : t('env.providerImportBtn')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
