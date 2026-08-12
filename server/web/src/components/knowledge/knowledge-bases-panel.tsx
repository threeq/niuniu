import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Library,
  Plus,
  Pencil,
  Trash2,
  Search,
  RefreshCw,
  AlertCircle,
  FolderOpen,
  Upload,
  Globe,
  Plug,
} from 'lucide-react'
import { toast } from 'sonner'
import { confirm } from '@/lib/confirm'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { integrationApi } from '@/lib/integration-api'
import { useConfigStore } from '@/stores/config-store'
import { DirectoryPicker } from '@/components/shared/directory-picker'
import type { Project } from '@/types/api'
import {
  listKnowledgeBases,
  createKnowledgeBase,
  updateKnowledgeBase,
  removeKnowledgeBase,
  uploadKnowledgeBaseFiles,
  retryKnowledgeBaseIngest,
  listKBPresets,
  isKBBusy,
  type KnowledgeBase,
  type KBSourceKind,
  type KBBinding,
  type KBPreset,
} from '@/lib/kb-api'
import { KnowledgeBaseBrowserDialog } from './knowledge-base-browser-dialog'

const SOURCE_KINDS: KBSourceKind[] = ['local', 'upload', 'url', 'mcp']

const SOURCE_ICON: Record<KBSourceKind, typeof FolderOpen> = {
  local: FolderOpen,
  upload: Upload,
  url: Globe,
  mcp: Plug,
}

export function KnowledgeBasesPanel() {
  const { t } = useTranslation('knowledge')
  const { data, isLoading, isError } = useQuery({
    queryKey: ['knowledge-bases'],
    queryFn: listKnowledgeBases,
    // Poll while any KB is mid-ingest so the progress bar advances live, then
    // fall idle once everything is terminal (ready/failed/disabled). 2s is fast
    // enough for a live bar without hammering the list endpoint for the whole
    // (potentially multi-minute) download window.
    refetchInterval: (query) => {
      const items = query.state.data as KnowledgeBase[] | undefined
      return items?.some(isKBBusy) ? 2000 : false
    },
  })
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<KnowledgeBase | null>(null)
  const [browsing, setBrowsing] = useState<KnowledgeBase | null>(null)

  const handleAdd = () => {
    setEditing(null)
    setDialogOpen(true)
  }
  const handleEdit = (kb: KnowledgeBase) => {
    setEditing(kb)
    setDialogOpen(true)
  }

  return (
    <section className="flex flex-col gap-3 rounded border border-warm-border p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">{t('panel.title')}</h2>
          <p className="text-xs text-warm-text-muted mt-1">
            {t('panel.description')}
          </p>
        </div>
        <Button size="sm" onClick={handleAdd}>
          <Plus className="size-4 mr-1" />
          {t('panel.add')}
        </Button>
      </div>

      {isLoading && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('panel.loading')}
        </div>
      )}

      {isError && !isLoading && (
        <div className="flex items-center gap-2 text-sm text-destructive py-2">
          <AlertCircle className="size-4" aria-hidden />
          {t('panel.loadError')}
        </div>
      )}

      {!isLoading && !isError && (!data || data.length === 0) && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('panel.empty')}
        </div>
      )}

      {!isLoading && data && data.length > 0 && (
        <div className="flex flex-col divide-y divide-warm-border/60">
          {data.map((kb) => (
            <KnowledgeBaseRow
              key={kb.id}
              kb={kb}
              onEdit={() => handleEdit(kb)}
              onBrowse={() => setBrowsing(kb)}
            />
          ))}
        </div>
      )}

      <KnowledgeBaseDialog
        open={dialogOpen}
        onOpenChange={(next) => {
          setDialogOpen(next)
          if (!next) setEditing(null)
        }}
        editing={editing}
      />

      <KnowledgeBaseBrowserDialog
        kb={browsing}
        onOpenChange={(open) => {
          if (!open) setBrowsing(null)
        }}
      />
    </section>
  )
}

interface RowProps {
  kb: KnowledgeBase
  onEdit: () => void
  onBrowse: () => void
}

function KnowledgeBaseRow({ kb, onEdit, onBrowse }: RowProps) {
  const { t } = useTranslation('knowledge')
  const queryClient = useQueryClient()
  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['knowledge-bases'] })

  const toggle = useMutation({
    mutationFn: () =>
      updateKnowledgeBase(kb.id, {
        status: kb.status === 'enabled' ? 'disabled' : 'enabled',
      }),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const retry = useMutation({
    mutationFn: () => retryKnowledgeBaseIngest(kb.id),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const del = useMutation({
    mutationFn: () => removeKnowledgeBase(kb.id),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const handleDelete = async () => {
    if (!(await confirm(t('row.deleteConfirm', { name: kb.name })))) return
    del.mutate()
  }

  const busy = isKBBusy(kb)
  const failed = kb.ingest_status === 'failed'
  const ready = kb.ingest_status === 'ready'
  const SourceIcon = SOURCE_ICON[kb.source_kind]

  return (
    <div className="flex flex-col gap-2 py-3">
      <div className="flex items-center gap-3">
        <Library className="size-5 text-warm-text shrink-0" aria-hidden />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium truncate">{kb.name}</span>
            {kb.status === 'disabled' && (
              <span className="shrink-0 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
                {t('row.disabledBadge')}
              </span>
            )}
            {kb.bindings.length === 0 && (
              <span className="shrink-0 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
                {t('row.unboundBadge')}
              </span>
            )}
          </div>
          <div className="flex items-center gap-1.5 text-xs text-warm-text-muted truncate">
            <SourceIcon className="size-3.5 shrink-0" aria-hidden />
            <span>{t(`sourceKind.${kb.source_kind}`)}</span>
            {ready && (
              <span className="truncate">
                {' · '}
                {t('row.counts', { docs: kb.doc_count, chunks: kb.chunk_count })}
              </span>
            )}
          </div>
        </div>

        {/* Enable/disable — only meaningful once the corpus is indexed. */}
        {ready && (
          <Switch
            aria-label={
              kb.status === 'enabled' ? t('row.disable') : t('row.enable')
            }
            checked={kb.status === 'enabled'}
            onCheckedChange={() => toggle.mutate()}
          />
        )}

        {ready && (
          <Button size="sm" variant="outline" onClick={onBrowse}>
            <Search className="size-4 mr-1" />
            {t('row.browse')}
          </Button>
        )}

        {failed && (
          <Button
            size="sm"
            variant="outline"
            disabled={retry.isPending}
            onClick={() => retry.mutate()}
          >
            <RefreshCw className="size-4 mr-1" />
            {t('row.retry')}
          </Button>
        )}

        <Button
          size="sm"
          variant="outline"
          aria-label={t('row.edit')}
          onClick={onEdit}
        >
          <Pencil className="size-4" />
        </Button>
        <Button
          size="sm"
          variant="ghost"
          aria-label={t('row.delete')}
          disabled={del.isPending}
          onClick={handleDelete}
        >
          <Trash2 className="size-4" />
        </Button>
      </div>

      {busy && (
        <div className="pl-8 flex flex-col gap-1">
          <div className="flex items-center justify-between text-xs text-warm-text-muted">
            <span>{t(`ingest.${kb.ingest_status}`)}</span>
            <span>{Math.round(kb.ingest_progress)}%</span>
          </div>
          <ProgressBar value={kb.ingest_progress} />
        </div>
      )}

      {failed && kb.ingest_error && (
        <p className="pl-8 text-xs text-destructive break-words">
          {kb.ingest_error}
        </p>
      )}
    </div>
  )
}

/** Token-only progress bar (no arbitrary colors; width is an inline style). */
function ProgressBar({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(100, value))
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-warm-muted">
      <div
        className="h-full rounded-full bg-info transition-[width] duration-500"
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}

interface DialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  editing: KnowledgeBase | null
}

// Outer wrapper keys the inner form on (open, editing.id) so it fully remounts
// with fresh useState initializers — same pattern as DataSourceDialog.
function KnowledgeBaseDialog({ open, onOpenChange, editing }: DialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh]">
        {open && (
          <KnowledgeBaseForm
            key={editing ? `edit-${editing.id}` : 'create'}
            editing={editing}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

interface FormProps {
  editing: KnowledgeBase | null
  onClose: () => void
}

function KnowledgeBaseForm({ editing, onClose }: FormProps) {
  const { t } = useTranslation('knowledge')
  const queryClient = useQueryClient()
  const isEdit = Boolean(editing)

  const [name, setName] = useState(editing?.name ?? '')
  const [description, setDescription] = useState(editing?.description ?? '')
  const [sourceKind, setSourceKind] = useState<KBSourceKind>(
    editing?.source_kind ?? 'local',
  )
  const [localPath, setLocalPath] = useState(
    editing?.source_kind === 'local' ? editing.source_location : '',
  )
  const [url, setUrl] = useState(
    editing?.source_kind === 'url' ? editing.source_location : '',
  )
  const [mcpEndpoint, setMcpEndpoint] = useState(
    editing?.source_kind === 'mcp' ? editing.source_location : '',
  )
  const [mcpToken, setMcpToken] = useState('')
  const [presetId, setPresetId] = useState('')
  const [mirrorUrls, setMirrorUrls] = useState<string[]>([])
  const [files, setFiles] = useState<File[]>([])
  const [bindings, setBindings] = useState<KBBinding[]>(editing?.bindings ?? [])
  // Server-side directory browse is only meaningful in the personal/local edition
  // (the server's filesystem is the user's own machine); hosted deployments hide it.
  const personalMode = useConfigStore((s) => s.personalMode)
  const [pickerOpen, setPickerOpen] = useState(false)

  // Network presets (#500). Only needed for the url source; the query is cheap
  // and cached, so fetch unconditionally and surface in the dropdown.
  const presets = useQuery({
    queryKey: ['kb-presets'],
    queryFn: listKBPresets,
  })

  const onPickPreset = (id: string) => {
    setPresetId(id)
    const p = (presets.data ?? []).find((x: KBPreset) => x.id === id)
    if (!p) {
      setMirrorUrls([])
      return
    }
    // A preset is a pure shortcut: fill the primary URL (still editable) and
    // remember the remaining mirrors as ordered fallbacks for the backend.
    setUrl(p.urls[0] ?? '')
    setMirrorUrls(p.urls.slice(1))
    if (!name.trim()) setName(p.name)
  }

  const canSave = useMemo(() => {
    if (name.trim().length === 0) return false
    if (isEdit) return true // source is immutable; only name/desc/bindings change
    switch (sourceKind) {
      case 'local':
        return localPath.trim().length > 0
      case 'url':
        return url.trim().length > 0
      case 'upload':
        return files.length > 0
      case 'mcp':
        return mcpEndpoint.trim().length > 0 && mcpToken.trim().length > 0
    }
  }, [name, isEdit, sourceKind, localPath, url, files, mcpEndpoint, mcpToken])

  const save = useMutation({
    mutationFn: async () => {
      if (isEdit && editing) {
        return updateKnowledgeBase(editing.id, {
          name: name.trim(),
          description: description.trim(),
          bindings,
        })
      }
      const created = await createKnowledgeBase({
        name: name.trim(),
        description: description.trim(),
        source_kind: sourceKind,
        source_location:
          sourceKind === 'local'
            ? localPath.trim()
            : sourceKind === 'url'
              ? url.trim()
              : sourceKind === 'mcp'
                ? mcpEndpoint.trim()
                : undefined,
        mirror_urls:
          sourceKind === 'url' && mirrorUrls.length > 0 ? mirrorUrls : undefined,
        preset_id: sourceKind === 'url' && presetId ? presetId : undefined,
        bindings,
      })
      // Upload kind: the KB exists but is empty until its files land; push them
      // now so the backend can start ingesting.
      if (sourceKind === 'upload' && files.length > 0) {
        await uploadKnowledgeBaseFiles(created.id, files)
      }
      // mcp kind: the API token lives in credstore under the per-KB alias the
      // projection resolves into the projected MCP server's auth header.
      if (sourceKind === 'mcp') {
        await integrationApi.createCredential({
          provider: 'knowledge-base',
          alias: `kb-${created.id}`,
          config: { token: mcpToken.trim() },
        })
      }
      return created
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['knowledge-bases'] })
      toast.success(isEdit ? t('dialog.saveSuccess') : t('dialog.createSuccess'))
      onClose()
    },
    onError: (e) =>
      toast.error(
        t('dialog.saveFailed', {
          error: e instanceof Error ? e.message : String(e),
        }),
      ),
  })

  const presetItems = presets.data ?? []

  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <Library className="size-4" />
          {isEdit ? t('dialog.editTitle') : t('dialog.createTitle')}
        </DialogTitle>
      </DialogHeader>

      <div className="flex flex-col gap-3 overflow-y-auto">
        <div className="flex flex-col gap-2">
          <Label htmlFor="kb-name">{t('dialog.nameLabel')}</Label>
          <Input
            id="kb-name"
            value={name}
            placeholder={t('dialog.namePlaceholder')}
            onChange={(e) => setName(e.target.value)}
          />
        </div>

        <div className="flex flex-col gap-2">
          <Label htmlFor="kb-description">{t('dialog.descriptionLabel')}</Label>
          <Textarea
            id="kb-description"
            value={description}
            rows={2}
            placeholder={t('dialog.descriptionPlaceholder')}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>

        {/* Source picker — segmented control. Immutable after creation. */}
        <div className="flex flex-col gap-2">
          <Label>{t('dialog.sourceKindLabel')}</Label>
          <div className="grid grid-cols-2 gap-2">
            {SOURCE_KINDS.map((k) => {
              const Icon = SOURCE_ICON[k]
              const active = sourceKind === k
              return (
                <Button
                  key={k}
                  type="button"
                  variant={active ? 'default' : 'outline'}
                  size="sm"
                  disabled={isEdit}
                  aria-pressed={active}
                  className="justify-center font-normal"
                  onClick={() => setSourceKind(k)}
                >
                  <Icon className="size-4 mr-1" aria-hidden />
                  {t(`sourceKind.${k}`)}
                </Button>
              )
            })}
          </div>
          {isEdit && (
            <p className="text-xs text-warm-text-muted">
              {t('dialog.editSourceNote')}
            </p>
          )}
        </div>

        {!isEdit && sourceKind === 'local' && (
          <div className="flex flex-col gap-2">
            <Label htmlFor="kb-local-path">{t('dialog.localPathLabel')}</Label>
            <div className="flex gap-2">
              <Input
                id="kb-local-path"
                value={localPath}
                placeholder={t('dialog.localPathPlaceholder')}
                onChange={(e) => setLocalPath(e.target.value)}
              />
              {personalMode && (
                <Button
                  type="button"
                  variant="outline"
                  className="shrink-0 font-normal"
                  onClick={() => setPickerOpen(true)}
                >
                  <FolderOpen className="size-4 mr-1" aria-hidden />
                  {t('dialog.localPathBrowse')}
                </Button>
              )}
            </div>
            <p className="text-xs text-warm-text-muted">
              {t('dialog.localPathHint')}
            </p>
            {personalMode && (
              <DirectoryPicker
                open={pickerOpen}
                onOpenChange={setPickerOpen}
                onSelect={(p) => {
                  setLocalPath(p)
                  setPickerOpen(false)
                }}
                t={t}
              />
            )}
          </div>
        )}

        {!isEdit && sourceKind === 'upload' && (
          <div className="flex flex-col gap-2">
            <Label htmlFor="kb-upload">{t('dialog.uploadLabel')}</Label>
            <Input
              id="kb-upload"
              type="file"
              multiple
              onChange={(e) => setFiles(Array.from(e.target.files ?? []))}
            />
            {files.length > 0 && (
              <p className="text-xs text-warm-text-muted">
                {t('dialog.uploadSelected', { count: files.length })}
              </p>
            )}
            <p className="text-xs text-warm-text-muted">
              {t('dialog.uploadHint')}
            </p>
          </div>
        )}

        {!isEdit && sourceKind === 'url' && (
          <>
            <div className="flex flex-col gap-2">
              <Label htmlFor="kb-preset">{t('dialog.urlPresetLabel')}</Label>
              <select
                id="kb-preset"
                value={presetId}
                onChange={(e) => onPickPreset(e.target.value)}
                className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
              >
                <option value="">{t('dialog.urlPresetNone')}</option>
                {presetItems.length === 0 && (
                  <option value="" disabled>
                    {t('dialog.urlPresetEmpty')}
                  </option>
                )}
                {presetItems.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="kb-url">{t('dialog.urlLabel')}</Label>
              <Input
                id="kb-url"
                value={url}
                placeholder={t('dialog.urlPlaceholder')}
                onChange={(e) => setUrl(e.target.value)}
              />
              <p className="text-xs text-warm-text-muted">
                {t('dialog.urlHint')}
              </p>
              {mirrorUrls.length > 0 && (
                <p className="text-xs text-warm-text-muted">
                  {t('dialog.urlMirrorHint', { count: mirrorUrls.length })}
                </p>
              )}
            </div>
          </>
        )}

        {!isEdit && sourceKind === 'mcp' && (
          <>
            <div className="flex flex-col gap-2">
              <Label htmlFor="kb-mcp-endpoint">
                {t('dialog.mcpEndpointLabel')}
              </Label>
              <Input
                id="kb-mcp-endpoint"
                value={mcpEndpoint}
                placeholder="https://kb.example.com/mcp"
                onChange={(e) => setMcpEndpoint(e.target.value)}
                className="font-mono text-xs"
              />
              <p className="text-xs text-warm-text-muted">
                {t('dialog.mcpEndpointHint')}
              </p>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="kb-mcp-token">{t('dialog.mcpTokenLabel')}</Label>
              <Input
                id="kb-mcp-token"
                type="password"
                value={mcpToken}
                placeholder={t('dialog.mcpTokenPh')}
                onChange={(e) => setMcpToken(e.target.value)}
                className="font-mono text-xs"
              />
              <p className="text-xs text-warm-text-muted">
                {t('dialog.mcpTokenHint')}
              </p>
            </div>
          </>
        )}

        <BindingEditor bindings={bindings} onChange={setBindings} />
      </div>

      <DialogFooter>
        <Button variant="outline" onClick={onClose} disabled={save.isPending}>
          {t('dialog.cancel')}
        </Button>
        <Button
          onClick={() => save.mutate()}
          disabled={!canSave || save.isPending}
        >
          {save.isPending
            ? isEdit
              ? t('dialog.saving')
              : t('dialog.creating')
            : isEdit
              ? t('dialog.save')
              : t('dialog.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

interface BindingEditorProps {
  bindings: KBBinding[]
  onChange: (next: KBBinding[]) => void
}

// Manages a KB's visibility — which PROJECTS may see it. A KB is exposed to an
// agent only through the project its workspace belongs to. Empty list = the KB
// is invisible to every agent. Mirrors DataSourcesPanel's BindingEditor.
function BindingEditor({ bindings, onChange }: BindingEditorProps) {
  const { t } = useTranslation('knowledge')

  const projects = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () =>
      api.get<Project[]>('/projects', { params: { status: 'active' } }),
  })

  const labelFor = (b: KBBinding): string => {
    const hit = (projects.data ?? []).find((p) => p.id === b.target_id)
    return hit ? hit.name : `#${b.target_id}`
  }

  const available = (projects.data ?? []).filter(
    (p) => !bindings.some((b) => b.target_id === p.id),
  )

  const add = (id: number) => {
    if (!id) return
    if (bindings.some((b) => b.target_id === id)) return
    onChange([...bindings, { target_type: 'project', target_id: id }])
  }

  const remove = (b: KBBinding) =>
    onChange(bindings.filter((x) => x.target_id !== b.target_id))

  return (
    <div className="rounded border border-warm-border/60 p-3 flex flex-col gap-3">
      <div>
        <h3 className="text-xs font-medium text-warm-text">
          {t('bindings.title')}
        </h3>
        <p className="text-xs text-warm-text-muted mt-1">{t('bindings.hint')}</p>
      </div>

      {bindings.length === 0 ? (
        <p className="text-xs text-warm-text-muted">{t('bindings.empty')}</p>
      ) : (
        <ul className="flex flex-col gap-1">
          {bindings.map((b) => (
            <li
              key={`${b.target_type}:${b.target_id}`}
              className="flex items-center gap-2 text-sm"
            >
              <span className="shrink-0 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
                {t('bindings.typeProject')}
              </span>
              <span className="flex-1 min-w-0 truncate">{labelFor(b)}</span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                aria-label={t('bindings.remove')}
                onClick={() => remove(b)}
              >
                <Trash2 className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <select
        aria-label={t('bindings.add')}
        value=""
        onChange={(e) => {
          if (e.target.value) add(Number(e.target.value))
        }}
        className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
      >
        <option value="">{t('bindings.selectTarget')}</option>
        {available.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
    </div>
  )
}
