import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { KeyRound, Pencil, Plus, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { EmptyState } from '@/components/shared/empty-state'
import { api } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import type {
  CapabilityBackend,
  CapabilityCapabilitySchema,
  CapabilityModule,
  SaveCapabilityBackendData,
} from '@/types/api'

// Settings → 能力配置: third-party generation-service accounts per capability
// module (video-gen), fully schema-driven — the module registry's
// config_schema decides which capability families and backend implementations
// exist, and each backend option declares the fields its form renders
// (type "secret" → password input whose blank value means "keep the stored
// key"). Independent of the env Providers domain: this config feeds NN_CAP_*
// env injection into capability-module tool processes.
//
// Frozen REST contract: docs/superpowers/plans/2026-09-27-video-creation-implementation.md §1.1.
// The server never returns a plaintext api_key — `has_api_key` is the only
// signal the UI may show.
export function CapabilitySettings() {
  const { t } = useTranslation('settings')
  const { data, isLoading } = useQuery({
    queryKey: ['capability-modules'],
    queryFn: () => api.listCapabilityModules(),
  })
  const modules = data?.modules ?? []

  return (
    <div className="space-y-6 py-2">
      <div>
        <h2 className="flex items-center gap-2 text-lg font-semibold text-warm-text">
          <KeyRound className="h-5 w-5 text-warm-text-muted" aria-hidden="true" />
          {t('capability.title')}
        </h2>
        <p className="mt-1 text-sm text-warm-text-muted">{t('capability.description')}</p>
      </div>

      {isLoading ? (
        <p className="text-sm text-warm-text-muted">{t('common:actions.loading')}</p>
      ) : modules.length === 0 ? (
        <EmptyState
          size="compact"
          icon={<KeyRound className="h-12 w-12 text-muted-foreground" aria-hidden="true" />}
          title={t('capability.noModules.title')}
          description={t('capability.noModules.description')}
        />
      ) : (
        modules.map((module) => <CapabilityModuleSection key={module.name} module={module} />)
      )}
    </div>
  )
}

interface DialogState {
  capability: CapabilityCapabilitySchema
  editing: CapabilityBackend | null
}

// One capability module: every capability family from the module's config
// schema renders an account list plus an add/edit dialog driven by the
// selected backend implementation's field schema.
function CapabilityModuleSection({ module }: { module: CapabilityModule }) {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const queryKey = ['capability-backends', module.name] as const

  const { data, isLoading } = useQuery({
    queryKey,
    queryFn: () => api.listCapabilityBackends(module.name),
  })
  const backends = data?.backends ?? []

  const [dialog, setDialog] = useState<DialogState | null>(null)

  const invalidate = () => queryClient.invalidateQueries({ queryKey })

  const saveMutation = useMutation({
    mutationFn: (vars: { id?: number; data: SaveCapabilityBackendData }) =>
      vars.id
        ? api.updateCapabilityBackend(vars.id, vars.data)
        : api.createCapabilityBackend(vars.data),
    onSuccess: (_saved, vars) => {
      invalidate()
      setDialog(null)
      toast.success(vars.id ? t('capability.saved') : t('capability.created'))
    },
    onError: (err: Error) => {
      toast.error(t('capability.saveFailed', { message: err.message }))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.deleteCapabilityBackend(id),
    onSuccess: () => {
      invalidate()
      toast.success(t('capability.deleted'))
    },
    onError: (err: Error) => {
      toast.error(t('capability.deleteFailed', { message: err.message }))
    },
  })

  // Flip enabled via the full PUT body (api_key omitted → stored key kept).
  const toggleMutation = useMutation({
    mutationFn: (backend: CapabilityBackend) =>
      api.updateCapabilityBackend(backend.id, {
        module: backend.module,
        capability: backend.capability,
        backend: backend.backend,
        name: backend.name,
        base_url: backend.base_url,
        extra_config: backend.extra_config,
        enabled: !backend.enabled,
      }),
    onSuccess: invalidate,
    onError: (err: Error) => {
      toast.error(t('capability.saveFailed', { message: err.message }))
    },
  })

  const handleDelete = async (backend: CapabilityBackend) => {
    const ok = await confirm({
      description: t('capability.deleteConfirm', { name: backend.name }),
      destructive: true,
    })
    if (ok) deleteMutation.mutate(backend.id)
  }

  return (
    <section className="space-y-4 rounded-lg border border-warm-border bg-warm-surface p-4">
      <h3 className="text-sm font-semibold text-warm-text">{module.display_name}</h3>

      {isLoading ? (
        <p className="text-sm text-warm-text-muted">{t('common:actions.loading')}</p>
      ) : (
        module.config_schema.capabilities.map((capability) => {
          // Server assigns `position`; render in that order (stable display).
          const rows = backends
            .filter((backend) => backend.capability === capability.key)
            .sort((a, b) => a.position - b.position)
          return (
            <div key={capability.key} className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <h4 className="text-sm font-medium text-warm-text">{capability.label}</h4>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setDialog({ capability, editing: null })}
                >
                  <Plus className="mr-1 h-3.5 w-3.5" aria-hidden="true" />
                  {t('capability.addAccount')}
                </Button>
              </div>

              {rows.length === 0 ? (
                <p className="rounded-md border border-dashed border-warm-border p-3 text-xs text-warm-text-muted">
                  {t('capability.emptyHint', { module: module.display_name })}
                  {' '}
                  {t('capability.keySourceHint')}
                </p>
              ) : (
                <div className="space-y-2">
                  {rows.map((backend) => {
                    const optionLabel =
                      capability.backends.find((option) => option.value === backend.backend)?.label ??
                      backend.backend
                    return (
                      <div
                        key={backend.id}
                        className="flex items-center justify-between gap-3 rounded-md border border-warm-border p-3"
                      >
                        <div className="min-w-0 flex-1 space-y-1">
                          <div className="flex min-w-0 flex-wrap items-center gap-2">
                            <span className="truncate text-sm font-medium text-warm-text">
                              {backend.name}
                            </span>
                            <Badge variant="secondary">{optionLabel}</Badge>
                            {!backend.enabled && (
                              <span className="text-xs text-warm-text-muted">
                                {t('capability.disabled')}
                              </span>
                            )}
                          </div>
                          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-warm-text-muted">
                            {backend.base_url && (
                              <span className="truncate font-mono">{backend.base_url}</span>
                            )}
                            <span className="inline-flex items-center gap-1">
                              <KeyRound className="h-3 w-3" aria-hidden="true" />
                              {backend.has_api_key
                                ? t('capability.keySet')
                                : t('capability.keyUnset')}
                            </span>
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-1">
                          <Switch
                            checked={backend.enabled}
                            disabled={toggleMutation.isPending}
                            onCheckedChange={() => toggleMutation.mutate(backend)}
                            aria-label={t('capability.toggleAria', { name: backend.name })}
                          />
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8"
                            onClick={() => setDialog({ capability, editing: backend })}
                            title={t('capability.editAria')}
                            aria-label={t('capability.editAria')}
                          >
                            <Pencil className="h-4 w-4" aria-hidden="true" />
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8"
                            onClick={() => void handleDelete(backend)}
                            title={t('capability.deleteAria')}
                            aria-label={t('capability.deleteAria')}
                          >
                            <Trash2 className="h-4 w-4" aria-hidden="true" />
                          </Button>
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })
      )}

      {dialog && (
        <CapabilityBackendDialog
          key={`${dialog.capability.key}:${dialog.editing?.id ?? 'new'}`}
          moduleName={module.name}
          capability={dialog.capability}
          editing={dialog.editing}
          saving={saveMutation.isPending}
          onClose={() => setDialog(null)}
          onSubmit={(data) => saveMutation.mutate({ id: dialog.editing?.id, data })}
        />
      )}
    </section>
  )
}

interface CapabilityBackendDialogProps {
  moduleName: string
  capability: CapabilityCapabilitySchema
  editing: CapabilityBackend | null
  saving: boolean
  onClose: () => void
  onSubmit: (data: SaveCapabilityBackendData) => void
}

function CapabilityBackendDialog({
  moduleName,
  capability,
  editing,
  saving,
  onClose,
  onSubmit,
}: CapabilityBackendDialogProps) {
  const { t } = useTranslation('settings')
  const [backendValue, setBackendValue] = useState(
    editing?.backend ?? capability.backends[0]?.value ?? '',
  )
  const [name, setName] = useState(editing?.name ?? '')
  const [enabled, setEnabled] = useState(editing?.enabled ?? true)
  const [fieldValues, setFieldValues] = useState<Record<string, string>>(() =>
    initialFieldValues(capability, editing?.backend ?? capability.backends[0]?.value, editing),
  )

  const option = capability.backends.find((entry) => entry.value === backendValue)
  const fields = option?.fields ?? []

  const setField = (key: string, value: string) =>
    setFieldValues((prev) => ({ ...prev, [key]: value }))

  const handleSubmit = () => {
    if (!option || !name.trim()) return
    // Schema-driven body assembly: base_url maps to its own column, api_key to
    // the secret column (blank = omit → keep the stored key on edit), and every
    // other declared field lands in extra_config.
    let baseUrl = ''
    let apiKey = ''
    const extraConfig: Record<string, string> = {}
    for (const field of fields) {
      const value = (fieldValues[field.key] ?? '').trim()
      if (field.key === 'api_key') {
        apiKey = value
      } else if (field.key === 'base_url') {
        baseUrl = value
      } else if (value !== '') {
        extraConfig[field.key] = value
      }
    }
    const data: SaveCapabilityBackendData = {
      module: moduleName,
      capability: capability.key,
      backend: option.value,
      name: name.trim(),
      base_url: baseUrl,
      extra_config: extraConfig,
      enabled,
    }
    if (apiKey) data.api_key = apiKey
    onSubmit(data)
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {editing ? t('capability.form.editTitle') : t('capability.form.createTitle')}
          </DialogTitle>
          <DialogDescription>{t('capability.form.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <label className="block text-sm font-medium text-warm-text" htmlFor="capability-name">
              {t('capability.form.nameLabel')}
            </label>
            <Input
              id="capability-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('capability.form.namePlaceholder')}
            />
          </div>

          <div className="space-y-1.5">
            <label className="block text-sm font-medium text-warm-text" htmlFor="capability-backend">
              {t('capability.form.backendLabel')}
            </label>
            <Select value={backendValue} onValueChange={setBackendValue}>
              <SelectTrigger id="capability-backend" aria-label={t('capability.form.backendLabel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {capability.backends.map((entry) => (
                  <SelectItem key={entry.value} value={entry.value}>
                    {entry.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {fields.map((field) => {
            const inputId = `capability-field-${field.key}`
            const isSecret = field.type === 'secret'
            return (
              <div key={field.key} className="space-y-1.5">
                <label className="block text-sm font-medium text-warm-text" htmlFor={inputId}>
                  {field.label}
                </label>
                <Input
                  id={inputId}
                  type={isSecret ? 'password' : 'text'}
                  autoComplete={isSecret ? 'new-password' : 'off'}
                  value={fieldValues[field.key] ?? ''}
                  onChange={(e) => setField(field.key, e.target.value)}
                  placeholder={
                    isSecret
                      ? editing?.has_api_key
                        ? t('capability.form.secretKeepPlaceholder')
                        : t('capability.form.secretPlaceholder')
                      : undefined
                  }
                />
                {isSecret && editing && (
                  <p className="text-xs text-warm-text-muted">
                    {t('capability.form.secretKeepHint')}
                  </p>
                )}
              </div>
            )
          })}

          <div className="flex items-center gap-2">
            <Switch
              id="capability-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
              aria-label={t('capability.form.enabledLabel')}
            />
            <label className="text-sm text-warm-text" htmlFor="capability-enabled">
              {t('capability.form.enabledLabel')}
            </label>
          </div>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            type="button"
            onClick={handleSubmit}
            disabled={saving || !name.trim() || !option}
          >
            {saving ? t('common:actions.saving') : t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// Seed the schema-driven form: base_url from its column, api_key always blank
// (never echoed), everything else from extra_config.
function initialFieldValues(
  capability: CapabilityCapabilitySchema,
  backendValue: string | undefined,
  editing: CapabilityBackend | null,
): Record<string, string> {
  const values: Record<string, string> = {}
  const option = capability.backends.find((entry) => entry.value === backendValue)
  for (const field of option?.fields ?? []) {
    if (field.key === 'api_key') values[field.key] = ''
    else if (field.key === 'base_url') values[field.key] = editing?.base_url ?? ''
    else values[field.key] = editing?.extra_config?.[field.key] ?? ''
  }
  return values
}
