import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  AlertTriangle,
  Clapperboard,
  FileJson,
  Film,
  ImageOff,
  LayoutGrid,
  Loader2,
  MessageSquarePlus,
  RefreshCw,
  Save,
  Send,
} from 'lucide-react'
import { ApiError, videoProjectApi } from '@/lib/api'
import { cn } from '@/lib/utils'
import { getFileContentUrl } from '@/lib/workspace-file-url'
import { contentTargetForPath, useWorkspacePanelStore } from '@/stores/workspace-panel-store'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/shared/empty-state'
import { Input } from '@/components/ui/input'
import { MarkdownMessage } from '@/components/shared/markdown-message'
import { Textarea } from '@/components/ui/textarea'
import type { SaveVideoProductBody, VideoChange, VideoProduct, VideoProjectResponse } from '@/types/api'

// 「视频产物」面板 (video creation · P3 layer-2 interactive product editing).
//
// Reads the frozen aggregate contract GET /workspaces/:id/video-project and
// renders the product family under <ws>/video-project/: a left tree (six
// products + change requests + outputs) and a right content pane. Small edits
// save straight to disk through the revision-guarded PUT (a 409 means an agent
// regenerated the file meanwhile → the user is told to refresh); larger
// intentions go through a change request (POST changes) that is later routed
// back into the creation flow via dispatch (POST changes/:id/dispatch).

const PRODUCT_ORDER = ['brief', 'storyline', 'script', 'characters', 'scenes', 'storyboard'] as const

// Products with a structured card preview; the remaining previews are
// formatted JSON text (storyline/script) or markdown (brief).
const CARD_KEYS = new Set<string>(['characters', 'scenes', 'storyboard'])
// Products whose default view is the raw editor — their value is prose, not
// per-field structure.
const RAW_DEFAULT_KEYS = new Set<string>(['script', 'storyline'])

const REVIEW_STATUSES = new Set(['draft', 'in-review', 'approved'])
const CHANGE_STATUSES = new Set(['pending', 'dispatched', 'regenerated', 'approved'])

// Candidate array keys used by the product family (characters.json /
// scenes.json are not schema-frozen, so probe the plausible containers).
const ASSET_LIST_KEYS = ['characters', 'roles', 'scenes', 'items', 'cards', 'list'] as const

type BadgeTone = 'neutral' | 'warning' | 'info' | 'success'

// Status chips reuse the semantic tokens (color + text, never color alone).
const TONE_CLASS: Record<BadgeTone, string> = {
  neutral: 'border-border bg-muted text-muted-foreground',
  warning: 'border-warning/30 bg-warning/15 text-warning',
  info: 'border-info/30 bg-info/15 text-info',
  success: 'border-success/30 bg-success/15 text-success',
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function baseName(p: string): string {
  const parts = p.split('/')
  return parts[parts.length - 1] || p
}

// Asset paths inside the products are relative to video-project/ (storyboard
// schema)；the workspace file endpoints need the full workspace-relative path.
function assetWsPath(p: string): string {
  const s = p.replace(/^\.\//, '').replace(/^\/+/, '')
  return s.startsWith('video-project/') ? s : `video-project/${s}`
}

// Raw editable text of a product: markdown arrives as a string, JSON products
// as a parsed object (re-serialized for the editor / PUT body).
function rawText(p: VideoProduct | null): string {
  if (!p) return ''
  if (typeof p.data === 'string') return p.data
  if (p.data === undefined || p.data === null) return ''
  try {
    return JSON.stringify(p.data, null, 2)
  } catch {
    return ''
  }
}

// Structured view of a product, tolerant of both transport shapes (parsed
// object, or a raw JSON string).
function structured(p: VideoProduct | null): unknown {
  if (!p || p.data === undefined || p.data === null) return null
  if (typeof p.data === 'string') {
    try {
      const parsed: unknown = JSON.parse(p.data)
      return parsed && typeof parsed === 'object' ? parsed : null
    } catch {
      return null
    }
  }
  return p.data
}

function shotList(data: unknown): Record<string, unknown>[] {
  if (!isRecord(data) || !Array.isArray(data.shots)) return []
  return data.shots.filter(isRecord)
}

// characters/scenes items: an array under one of the known container keys, a
// bare array, or an id-keyed object map (values all objects).
function assetItems(data: unknown): Record<string, unknown>[] {
  if (Array.isArray(data)) return data.filter(isRecord)
  if (!isRecord(data)) return []
  for (const key of ASSET_LIST_KEYS) {
    const v = data[key]
    if (Array.isArray(v)) return v.filter(isRecord)
  }
  const values = Object.values(data)
  if (values.length > 0 && values.every(isRecord)) return values
  return []
}

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  const nf = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })
  if (n >= 1024 * 1024) return `${nf.format(n / (1024 * 1024))} MB`
  if (n >= 1024) return `${nf.format(n / 1024)} KB`
  return `${n} B`
}

function reviewTone(status: string): BadgeTone {
  if (status === 'approved') return 'success'
  if (status === 'in-review') return 'warning'
  return 'neutral'
}

function changeTone(status: string): BadgeTone {
  if (status === 'dispatched') return 'info'
  if (status === 'approved') return 'success'
  if (status === 'pending') return 'warning'
  return 'neutral'
}

function StatusBadge({ label, tone }: { label: string; tone: BadgeTone }) {
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center rounded border px-1.5 py-0.5 text-[10px] leading-none',
        TONE_CLASS[tone],
      )}
    >
      {label}
    </span>
  )
}

// One storyboard shot card: number / duration / action / narration / subtitle /
// prompt / candidates, with the high-value text fields edited in place.
function ShotCard({
  index,
  shot,
  onPatch,
  onOpenAsset,
}: {
  index: number
  shot: Record<string, unknown>
  onPatch: (mutate: (shot: Record<string, unknown>) => void) => void
  onOpenAsset: (path: string) => void
}) {
  const { t } = useTranslation('workspaces')
  const shotId = String(shot.id ?? index + 1)
  const visual = isRecord(shot.visual) ? shot.visual : {}
  const candidates = Array.isArray(visual.candidates) ? visual.candidates.filter((c) => typeof c === 'string') : []
  const selected = str(visual.selected) || str(visual.asset)
  const tier = str(visual.tier)
  const duration = shot.duration_sec === undefined || shot.duration_sec === null ? '' : String(shot.duration_sec)

  const fieldLabel = (field: string) => `${t(field)} #${shotId}`

  return (
    <div className="rounded-lg border border-border bg-warm-surface p-3 shadow-sm">
      <div className="flex items-center gap-2">
        <span className="shrink-0 font-mono text-xs tabular-nums text-muted-foreground">#{shotId}</span>
        {tier && <StatusBadge label={tier} tone="neutral" />}
        {shot.transition !== undefined && <StatusBadge label={String(shot.transition)} tone="neutral" />}
        <div className="ml-auto flex items-center gap-1.5">
          <Input
            value={duration}
            aria-label={fieldLabel('videoProduct.shot.duration')}
            onChange={(e) => {
              const next = e.target.value
              onPatch((s) => {
                if (next.trim() === '') {
                  delete s.duration_sec
                } else {
                  const n = Number(next)
                  s.duration_sec = Number.isFinite(n) ? n : next
                }
              })
            }}
            className="h-7 w-16 text-xs tabular-nums"
          />
          <span className="text-xs text-muted-foreground">s</span>
        </div>
      </div>

      <div className="mt-2 flex items-start gap-2">
        <span className="w-14 shrink-0 pt-1.5 text-xs text-muted-foreground">
          {t('videoProduct.shot.action')}
        </span>
        <Textarea
          value={str(shot.action) || str(shot.camera)}
          aria-label={fieldLabel('videoProduct.shot.action')}
          onChange={(e) => onPatch((s) => { s.action = e.target.value })}
          className="min-h-0 text-xs"
          rows={2}
        />
      </div>

      <div className="mt-2 flex items-start gap-2">
        <span className="w-14 shrink-0 pt-1.5 text-xs text-muted-foreground">
          {t('videoProduct.shot.narration')}
        </span>
        <Textarea
          value={str(shot.narration)}
          aria-label={fieldLabel('videoProduct.shot.narration')}
          onChange={(e) => onPatch((s) => { s.narration = e.target.value })}
          className="min-h-0 text-xs"
          rows={2}
        />
      </div>

      <div className="mt-2 flex items-start gap-2">
        <span className="w-14 shrink-0 pt-1.5 text-xs text-muted-foreground">
          {t('videoProduct.shot.subtitle')}
        </span>
        <Textarea
          value={str(shot.subtitle)}
          aria-label={fieldLabel('videoProduct.shot.subtitle')}
          onChange={(e) => onPatch((s) => { s.subtitle = e.target.value })}
          className="min-h-0 text-xs"
          rows={2}
        />
      </div>

      <div className="mt-2 flex items-start gap-2">
        <span className="w-14 shrink-0 pt-1.5 text-xs text-muted-foreground">
          {t('videoProduct.shot.prompt')}
        </span>
        <Textarea
          value={str(visual.prompt)}
          aria-label={fieldLabel('videoProduct.shot.prompt')}
          onChange={(e) => onPatch((s) => {
            const next = e.target.value
            const v = isRecord(s.visual) ? s.visual : {}
            v.prompt = next
            s.visual = v
          })}
          className="min-h-0 font-mono text-xs"
          rows={3}
        />
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-1">
        <span className="text-xs text-muted-foreground">{t('videoProduct.shot.candidates')}</span>
        {candidates.length === 0 ? (
          <span className="text-xs text-muted-foreground">{t('videoProduct.shot.noCandidates')}</span>
        ) : (
          candidates.map((c) => (
            <Button
              key={c}
              variant="ghost"
              size="sm"
              className={cn(
                'h-6 gap-1 px-1.5 font-mono text-[10px] font-normal',
                c === selected ? 'text-brand' : 'text-info',
              )}
              title={c}
              onClick={() => onOpenAsset(c)}
            >
              <Film className="size-3" aria-hidden="true" />
              {baseName(c)}
            </Button>
          ))
        )}
      </div>

      {selected && (
        <div className="mt-1 flex items-center gap-1">
          <span className="text-[10px] text-muted-foreground">{t('videoProduct.shot.selected')}</span>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 gap-1 px-1.5 font-mono text-[10px] font-normal text-brand"
            title={selected}
            onClick={() => onOpenAsset(selected)}
          >
            <Film className="size-3" aria-hidden="true" />
            {baseName(selected)}
          </Button>
        </div>
      )}
    </div>
  )
}

// One characters/scenes card: name + description + reference-image thumbnail
// (served through the existing workspace file-content endpoint).
function AssetCard({ item, workspaceId }: { item: Record<string, unknown>; workspaceId: string }) {
  const { t } = useTranslation('workspaces')
  const name = str(item.name) || str(item.id)
  const description = str(item.description) || str(item.appearance_prompt) || str(item.summary) || str(item.setting)
  const image = str(item.reference_image) || str(item.reference) || str(item.image) || str(item.image_path)

  return (
    <div className="flex items-start gap-3 rounded-lg border border-border bg-warm-surface p-3 shadow-sm">
      {image ? (
        <img
          src={getFileContentUrl(workspaceId, assetWsPath(image), 'raw')}
          alt={name}
          className="size-16 shrink-0 rounded-md border border-border object-cover"
        />
      ) : (
        <span
          className="flex size-16 shrink-0 items-center justify-center rounded-md border border-dashed border-border text-muted-foreground"
          title={t('videoProduct.card.noReferenceImage')}
        >
          <ImageOff className="size-4" aria-hidden="true" />
        </span>
      )}
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{name || '—'}</p>
        {description && (
          <p className="mt-1 whitespace-pre-wrap break-words text-xs text-muted-foreground">{description}</p>
        )}
      </div>
    </div>
  )
}

interface VideoProductPanelProps {
  workspaceId: string
}

export function VideoProductPanel({ workspaceId }: VideoProductPanelProps) {
  const { t } = useTranslation('workspaces')
  const queryClient = useQueryClient()
  const openContentViewer = useWorkspacePanelStore((s) => s.openContentViewer)

  const queryKey = ['video-project', workspaceId] as const
  const { data, isLoading, isError, isFetching, refetch } = useQuery({
    queryKey,
    queryFn: () => videoProjectApi.get(workspaceId),
    retry: 1,
  })

  const [selectedKey, setSelectedKey] = useState<string>('storyboard')
  // Per-product edit state: raw text drafts and parsed working copies, so
  // switching between products never silently drops unsaved edits.
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [working, setWorking] = useState<Record<string, unknown>>({})
  const [mode, setMode] = useState<'preview' | 'raw'>('preview')
  const [conflict, setConflict] = useState(false)
  const [saving, setSaving] = useState(false)
  const [noteTarget, setNoteTarget] = useState<string | null>(null)
  const [noteContent, setNoteContent] = useState('')
  const [noteBusy, setNoteBusy] = useState(false)
  const [dispatchingId, setDispatchingId] = useState<string | null>(null)

  const products = data?.products ?? []
  const changes = data?.changes ?? []
  const outputs = data?.outputs ?? []
  const active = products.find((p) => p.key === selectedKey) ?? null
  const activeKey = active?.key ?? ''
  const draft = active ? drafts[active.key] : undefined
  const dirty = draft !== undefined
  const displayData = active ? working[active.key] ?? structured(active) : null
  const activeFile = active ? baseName(active.file) : ''
  const targetValue = noteTarget ?? activeFile

  const updateProductInCache = (updated: VideoProduct) => {
    queryClient.setQueryData(queryKey, (prev: VideoProjectResponse | undefined) =>
      prev ? { ...prev, products: prev.products.map((p) => (p.key === updated.key ? updated : p)) } : prev,
    )
  }

  const selectProduct = (key: string) => {
    setSelectedKey(key)
    setNoteTarget(null)
    setConflict(false)
    setMode(RAW_DEFAULT_KEYS.has(key) ? 'raw' : 'preview')
  }

  const handleRefresh = () => {
    setDrafts({})
    setWorking({})
    setConflict(false)
    void refetch()
  }

  const handleDraftChange = (text: string) => {
    if (!activeKey) return
    setDrafts((d) => ({ ...d, [activeKey]: text }))
  }

  // Card-field edit: patch the working copy, keeping the raw draft in sync so
  // the two views never disagree.
  const patchActive = (mutate: (data: Record<string, unknown>) => void) => {
    if (!activeKey || !isRecord(displayData)) return
    const next = JSON.parse(JSON.stringify(displayData)) as Record<string, unknown>
    mutate(next)
    setWorking((w) => ({ ...w, [activeKey]: next }))
    setDrafts((d) => ({ ...d, [activeKey]: JSON.stringify(next, null, 2) }))
  }

  const toggleMode = () => {
    if (mode === 'raw' && draft !== undefined && active?.kind !== 'markdown') {
      try {
        const parsed: unknown = JSON.parse(draft)
        setWorking((w) => ({ ...w, [activeKey]: parsed }))
      } catch {
        toast.error(t('videoProduct.invalidJson'))
        return
      }
    }
    setMode((m) => (m === 'raw' ? 'preview' : 'raw'))
  }

  const handleSave = async () => {
    if (!active || !dirty || saving) return
    setSaving(true)
    try {
      const body: SaveVideoProductBody = { content: draft ?? '' }
      if (active.kind !== 'markdown') body.expected_revision = active.revision
      const updated = await videoProjectApi.saveProduct(workspaceId, active.key, body)
      updateProductInCache(updated)
      setDrafts((d) => {
        const next = { ...d }
        delete next[active.key]
        return next
      })
      setWorking((w) => {
        const next = { ...w }
        delete next[active.key]
        return next
      })
      setConflict(false)
      toast.success(t('videoProduct.saved'))
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // The agent regenerated the product between load and save: the local
        // revision guard is stale, so the only safe move is refresh + redo.
        setConflict(true)
        toast.error(t('videoProduct.saveConflict'))
      } else {
        toast.error(err instanceof Error && err.message ? err.message : t('videoProduct.saveFailed'))
      }
    } finally {
      setSaving(false)
    }
  }

  const handleAddNote = async () => {
    const content = noteContent.trim()
    if (!content || noteBusy || !targetValue) return
    setNoteBusy(true)
    try {
      const change = await videoProjectApi.createChange(workspaceId, {
        target: targetValue,
        kind: 'annotation',
        content,
      })
      // The POST (201) returns the new change object. The server lists changes
      // newest-first, so prepend to keep the same order until the next refetch.
      queryClient.setQueryData(queryKey, (prev: VideoProjectResponse | undefined) =>
        prev ? { ...prev, changes: [change, ...prev.changes] } : prev,
      )
      setNoteContent('')
      toast.success(t('videoProduct.note.added'))
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t('videoProduct.note.failed'))
    } finally {
      setNoteBusy(false)
    }
  }

  const handleDispatch = async (change: VideoChange) => {
    if (dispatchingId) return
    setDispatchingId(change.id)
    try {
      const res = await videoProjectApi.dispatchChange(workspaceId, change.id)
      // The response is the source of truth (never optimistically mark it
      // dispatched): when the issue is blocked the change intentionally stays
      // pending on disk, so don't claim it was routed.
      queryClient.setQueryData(queryKey, (prev: VideoProjectResponse | undefined) =>
        prev
          ? { ...prev, changes: prev.changes.map((c) => (c.id === res.change.id ? res.change : c)) }
          : prev,
      )
      if (res.change.status === 'dispatched') {
        toast.success(t('videoProduct.dispatched', { title: res.issue_title }))
      } else {
        toast.warning(t('videoProduct.dispatchPending'))
      }
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t('videoProduct.dispatchFailed'))
    } finally {
      setDispatchingId(null)
    }
  }

  const openAsset = (path: string) => {
    openContentViewer(workspaceId, contentTargetForPath(assetWsPath(path), baseName(path)))
  }

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
        {t('panels.loading')}
      </div>
    )
  }

  if (isError || !data) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
        <AlertTriangle className="size-6 text-destructive" aria-hidden="true" />
        <p className="text-sm text-muted-foreground">{t('videoProduct.loadFailed')}</p>
        <Button variant="secondary" size="sm" onClick={() => void refetch()}>
          {t('videoProduct.refresh')}
        </Button>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-card">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
        <Clapperboard className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <h2 className="truncate text-sm font-medium">{t('videoProduct.title')}</h2>
        <Button
          variant="ghost"
          size="icon"
          className="ml-auto size-7 shrink-0"
          onClick={handleRefresh}
          aria-label={t('videoProduct.refresh')}
          title={t('videoProduct.refresh')}
        >
          {isFetching ? (
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <RefreshCw className="size-3.5" aria-hidden="true" />
          )}
        </Button>
      </div>

      {!data.exists ? (
        <EmptyState
          className="flex-1"
          icon={<Clapperboard className="h-12 w-12 text-muted-foreground" />}
          title={t('videoProduct.empty.title')}
          description={t('videoProduct.empty.description')}
        />
      ) : (
        <div className="flex min-h-0 flex-1">
          {/* Left: product tree + change requests + deliverables. */}
          <div className="flex w-40 shrink-0 flex-col overflow-y-auto border-r border-border py-1">
            <p className="px-2 pb-0.5 pt-1 text-[10px] font-medium text-muted-foreground">
              {t('videoProduct.productsHeading')}
            </p>
            {PRODUCT_ORDER.map((key) => {
              const product = products.find((p) => p.key === key)
              const selected = key === selectedKey
              const status = product?.present && REVIEW_STATUSES.has(product.review_status) ? product.review_status : ''
              return (
                <Button
                  key={key}
                  variant="ghost"
                  size="sm"
                  aria-current={selected ? 'true' : undefined}
                  onClick={() => selectProduct(key)}
                  className={cn(
                    'h-auto w-full justify-start gap-1.5 rounded-none px-2 py-1 text-left font-normal',
                    selected ? 'bg-brand-soft text-brand hover:bg-brand-soft hover:text-brand' : 'text-foreground',
                  )}
                >
                  <FileJson
                    className={cn('size-3.5 shrink-0', selected ? 'text-brand' : 'text-muted-foreground')}
                    aria-hidden="true"
                  />
                  <span className="min-w-0 flex-1 truncate text-xs">{t(`videoProduct.products.${key}`)}</span>
                  {product?.present && product.revision > 0 && (
                    <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                      {t('videoProduct.revision', { n: product.revision })}
                    </span>
                  )}
                  {status && <StatusBadge label={t(`videoProduct.reviewStatus.${status}`)} tone={reviewTone(status)} />}
                </Button>
              )
            })}

            <p className="px-2 pb-0.5 pt-2 text-[10px] font-medium text-muted-foreground">
              {t('videoProduct.changesHeading')}
            </p>
            {changes.length === 0 ? (
              <p className="px-2 py-0.5 text-xs text-muted-foreground">{t('videoProduct.changesEmpty')}</p>
            ) : (
              changes.map((change) => {
                const status = CHANGE_STATUSES.has(change.status)
                  ? t(`videoProduct.changeStatus.${change.status}`)
                  : change.status
                return (
                  <div key={change.id} className="px-2 py-1">
                    <div className="flex items-center gap-1">
                      <span
                        className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground"
                        title={change.target}
                      >
                        {change.target}
                      </span>
                      <StatusBadge label={status} tone={changeTone(change.status)} />
                    </div>
                    <p className="truncate text-xs" title={change.content}>
                      {change.content}
                    </p>
                    <div className="flex items-center gap-1">
                      {change.status === 'pending' && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-6 gap-1 px-1 text-[10px] font-normal text-info"
                          disabled={dispatchingId !== null}
                          onClick={() => void handleDispatch(change)}
                        >
                          <Send className="size-3" aria-hidden="true" />
                          {t('videoProduct.dispatch')}
                        </Button>
                      )}
                      {change.dispatched_to_issue > 0 && (
                        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
                          #{change.dispatched_to_issue}
                        </span>
                      )}
                    </div>
                  </div>
                )
              })
            )}

            <p className="px-2 pb-0.5 pt-2 text-[10px] font-medium text-muted-foreground">
              {t('videoProduct.outputsHeading')}
            </p>
            {outputs.length === 0 ? (
              <p className="px-2 py-0.5 text-xs text-muted-foreground">{t('videoProduct.outputsEmpty')}</p>
            ) : (
              outputs.map((output) => (
                <Button
                  key={output.path}
                  variant="ghost"
                  size="sm"
                  className="h-auto w-full justify-start gap-1.5 rounded-none px-2 py-1 text-left font-normal"
                  title={output.path}
                  onClick={() =>
                    openContentViewer(workspaceId, contentTargetForPath(output.path, baseName(output.path)))
                  }
                >
                  <Film className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate text-xs">{baseName(output.path)}</span>
                  <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                    {formatBytes(output.size)}
                  </span>
                </Button>
              ))
            )}

            <p className="mt-auto px-2 py-1 text-[10px] text-muted-foreground">
              {t('videoProduct.shards', {
                quotes: data.shards?.quotes ?? 0,
                qc: data.shards?.qc ?? 0,
              })}
            </p>
          </div>

          {/* Right: preview / editor for the selected product + note composer. */}
          <div className="flex min-w-0 flex-1 flex-col">
            {!active ? (
              <div className="flex flex-1 items-center justify-center p-4 text-sm text-muted-foreground">
                {t('videoProduct.selectHint')}
              </div>
            ) : (
              <>
                {conflict && (
                  <div className="flex shrink-0 items-center gap-2 border-b border-destructive/40 bg-destructive/10 px-3 py-1.5 text-xs text-destructive">
                    <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
                    <span className="min-w-0 flex-1">{t('videoProduct.saveConflict')}</span>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-6 shrink-0 px-1.5 text-[10px] font-normal text-destructive"
                      onClick={handleRefresh}
                    >
                      {t('videoProduct.refresh')}
                    </Button>
                  </div>
                )}

                <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-3 py-1.5">
                  <span className="truncate text-xs font-medium">{t(`videoProduct.products.${active.key}`)}</span>
                  {active.revision > 0 && (
                    <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                      {t('videoProduct.revision', { n: active.revision })}
                    </span>
                  )}
                  {REVIEW_STATUSES.has(active.review_status) && (
                    <StatusBadge
                      label={t(`videoProduct.reviewStatus.${active.review_status}`)}
                      tone={reviewTone(active.review_status)}
                    />
                  )}
                  <div className="ml-auto flex shrink-0 items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-6 gap-1 px-1.5 text-[10px] font-normal text-muted-foreground"
                      onClick={toggleMode}
                    >
                      {mode === 'raw' ? (
                        <LayoutGrid className="size-3" aria-hidden="true" />
                      ) : (
                        <FileJson className="size-3" aria-hidden="true" />
                      )}
                      {mode === 'raw' ? t('videoProduct.view.preview') : t('videoProduct.view.raw')}
                    </Button>
                    <Button
                      variant={dirty ? 'default' : 'secondary'}
                      size="sm"
                      className="h-6 gap-1 px-1.5 text-[10px]"
                      disabled={!dirty || saving}
                      onClick={() => void handleSave()}
                    >
                      {saving ? (
                        <Loader2 className="size-3 animate-spin" aria-hidden="true" />
                      ) : (
                        <Save className="size-3" aria-hidden="true" />
                      )}
                      {t('videoProduct.save')}
                    </Button>
                  </div>
                </div>

                <div
                  className={cn(
                    'min-h-0 flex-1 overflow-y-auto px-3 py-2',
                    conflict && 'bg-destructive/5',
                  )}
                >
                  {!active.present ? (
                    <EmptyState
                      size="compact"
                      title={t('videoProduct.notGenerated')}
                      description={t('videoProduct.notGeneratedHint')}
                    />
                  ) : (
                    <>
                      {active.error ? (
                        <p className="rounded-md border border-destructive/40 bg-destructive/10 px-2 py-1.5 text-xs text-destructive">
                          {active.error}
                        </p>
                      ) : null}
                      {mode === 'raw' ? (
                        <Textarea
                          value={draft ?? rawText(active)}
                          aria-label={t('videoProduct.view.raw')}
                          onChange={(e) => handleDraftChange(e.target.value)}
                          className="min-h-64 font-mono text-xs"
                        />
                      ) : active.key === 'brief' ? (
                        <div className="text-sm">
                          <MarkdownMessage content={rawText(active)} role="assistant" />
                        </div>
                      ) : active.key === 'storyboard' ? (
                        <StoryboardView
                          data={displayData}
                          onPatch={patchActive}
                          onOpenAsset={openAsset}
                        />
                      ) : CARD_KEYS.has(active.key) ? (
                        <AssetCardsView data={displayData} workspaceId={workspaceId} />
                      ) : (
                        <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">
                          {rawText(active)}
                        </pre>
                      )}
                    </>
                  )}
                </div>

                {/* Note composer: annotation change request on the selected
                    product (target editable for a finer JSON-path anchor). */}
                <div className="shrink-0 space-y-1.5 border-t border-border p-2">
                  <div className="flex items-center gap-1.5">
                    <MessageSquarePlus className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                    <span className="text-xs font-medium">{t('videoProduct.note.title')}</span>
                  </div>
                  <Input
                    value={targetValue}
                    aria-label={t('videoProduct.note.targetLabel')}
                    onChange={(e) => setNoteTarget(e.target.value)}
                    className="h-7 font-mono text-xs"
                  />
                  <Textarea
                    value={noteContent}
                    placeholder={t('videoProduct.note.placeholder')}
                    aria-label={t('videoProduct.note.placeholder')}
                    onChange={(e) => setNoteContent(e.target.value)}
                    className="min-h-12 text-xs"
                    rows={2}
                  />
                  <div className="flex justify-end">
                    <Button
                      variant="secondary"
                      size="sm"
                      className="h-6 gap-1 px-1.5 text-[10px]"
                      disabled={noteBusy || noteContent.trim() === ''}
                      onClick={() => void handleAddNote()}
                    >
                      {noteBusy ? (
                        <Loader2 className="size-3 animate-spin" aria-hidden="true" />
                      ) : (
                        <MessageSquarePlus className="size-3" aria-hidden="true" />
                      )}
                      {t('videoProduct.note.submit')}
                    </Button>
                  </div>
                </div>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// Storyboard preview: one card per shot (number / duration / action /
// narration / subtitle / prompt / candidate assets).
function StoryboardView({
  data,
  onPatch,
  onOpenAsset,
}: {
  data: unknown
  onPatch: (mutate: (data: Record<string, unknown>) => void) => void
  onOpenAsset: (path: string) => void
}) {
  const { t } = useTranslation('workspaces')
  const shots = shotList(data)
  const meta = isRecord(data) ? data : {}
  const metaLine = [str(meta.aspect_ratio), meta.fps ? `${String(meta.fps)}fps` : '', str(meta.resolution)]
    .filter(Boolean)
    .join(' · ')

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium">{str(meta.title) || '—'}</span>
        {metaLine && (
          <span className="font-mono text-[10px] tabular-nums text-muted-foreground">{metaLine}</span>
        )}
        <span className="text-[10px] text-muted-foreground">
          {t('videoProduct.shot.count', { count: shots.length })}
        </span>
      </div>
      {shots.map((shot, index) => (
        <ShotCard
          key={String(shot.id ?? index)}
          index={index}
          shot={shot}
          // The panel patches the product root; ShotCard edits one shot, so
          // re-scope the mutation to that shot inside the cloned root. The
          // original list and the clone are filtered by the same shotList, so
          // the index lines up.
          onPatch={(mutate) =>
            onPatch((root) => {
              const target = shotList(root)[index]
              if (target) mutate(target)
            })
          }
          onOpenAsset={onOpenAsset}
        />
      ))}
    </div>
  )
}

// characters/scenes preview: name + description + reference thumbnail per item.
function AssetCardsView({ data, workspaceId }: { data: unknown; workspaceId: string }) {
  const { t } = useTranslation('workspaces')
  const items = assetItems(data)
  if (items.length === 0) {
    return <p className="text-sm text-muted-foreground">{t('videoProduct.card.noItems')}</p>
  }
  return (
    <div className="space-y-2">
      {items.map((item, index) => (
        <AssetCard key={str(item.id) || String(index)} item={item} workspaceId={workspaceId} />
      ))}
    </div>
  )
}
