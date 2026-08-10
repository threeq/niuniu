import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Pencil, ChevronDown, ChevronRight, Package } from 'lucide-react'
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
import type { EnvPreset, CreateEnvPresetData } from '@/types/api'

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
      <div className="flex items-center justify-between">
        <div className="min-w-0">
          <p className="text-sm text-muted-foreground">
            {t('env.description')}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t('env.oneShotHint')}
          </p>
        </div>
        <Button size="sm" onClick={openCreateDialog} className="flex-shrink-0">
          <Plus className="h-3.5 w-3.5 mr-1" />
          {t('env.newPreset')}
        </Button>
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common:actions.loading')}</p>
      ) : presets.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('env.noPresets')}</p>
      ) : (
        <div className="space-y-3">
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
    </div>
  )
}
