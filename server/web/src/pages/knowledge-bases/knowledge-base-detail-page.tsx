import { useState } from 'react'
import { useParams } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useMutation } from '@tanstack/react-query'
import {
  Library,
  LayoutGrid,
  FileText,
  Search,
  Settings,
  RefreshCw,
  Pencil,
  Trash2,
  AlertCircle,
  RotateCcw,
} from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { confirm } from '@/lib/confirm'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { EmptyState } from '@/components/shared/empty-state'
import {
  useKnowledgeBase,
  useInvalidateKnowledgeBases,
} from '@/lib/hooks/use-knowledge-bases'
import { KnowledgeBaseDialog } from '@/components/knowledge/knowledge-base-dialog'
import {
  KBStatusBadge,
  KBProgressBar,
} from '@/components/knowledge/kb-status'
import { KB_SOURCE_ICON } from '@/components/knowledge/kb-source-meta'
import { KBOverviewTab } from './tabs/kb-overview-tab'
import { KBDocumentsTab } from './tabs/kb-documents-tab'
import { KBSearchTab } from './tabs/kb-search-tab'
import { KBSettingsTab } from './tabs/kb-settings-tab'
import {
  isKBBusy,
  updateKnowledgeBase,
  removeKnowledgeBase,
  retryKnowledgeBaseIngest,
  reindexKnowledgeBase,
} from '@/lib/kb-api'

type Tab = 'overview' | 'documents' | 'search' | 'settings'

/**
 * Detail view for one knowledge base — the counterpart to RepositoryDetailPage.
 *
 * Everything that used to be crammed into one list row (browse, search, enable,
 * retry, edit, delete, bindings) now has room here, and the two capabilities the
 * old UI never surfaced at all — reading a document's text, and rebuilding the
 * index — are first-class actions.
 */
export function KnowledgeBaseDetailPage() {
  const { t } = useTranslation('knowledge')
  const params = useParams({ strict: false })
  const id = Number((params as Record<string, string | undefined>).id)
  const { kb, isLoading } = useKnowledgeBase(Number.isFinite(id) ? id : undefined)
  const invalidate = useInvalidateKnowledgeBases()

  const [activeTab, setActiveTab] = useState<Tab>('overview')
  const [editOpen, setEditOpen] = useState(false)

  const toggle = useMutation({
    mutationFn: (next: boolean) =>
      updateKnowledgeBase(id, { status: next ? 'enabled' : 'disabled' }),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const retry = useMutation({
    mutationFn: () => retryKnowledgeBaseIngest(id),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const reindex = useMutation({
    mutationFn: () => reindexKnowledgeBase(id),
    onSuccess: () => {
      invalidate()
      toast.success(t('detail.reindexStarted'))
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const del = useMutation({
    mutationFn: () => removeKnowledgeBase(id),
    onSuccess: () => {
      invalidate()
      toast.success(t('detail.deleted'))
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground text-sm">
        {t('common:actions.loading')}
      </div>
    )
  }

  if (!kb) {
    return (
      <div className="flex h-full items-center justify-center">
        <EmptyState
          icon={<AlertCircle className="h-12 w-12 text-muted-foreground" />}
          title={t('detail.notFound')}
          description={t('detail.notFoundHint')}
          size="compact"
        />
      </div>
    )
  }

  const tabs: { id: Tab; label: string; icon: typeof FileText }[] = [
    { id: 'overview', label: t('detail.tabs.overview'), icon: LayoutGrid },
    { id: 'documents', label: t('detail.tabs.documents'), icon: FileText },
    { id: 'search', label: t('detail.tabs.search'), icon: Search },
    { id: 'settings', label: t('detail.tabs.settings'), icon: Settings },
  ]

  const busy = isKBBusy(kb)
  const failed = kb.ingest_status === 'failed'
  const ready = kb.ingest_status === 'ready'
  // An mcp KB is a remote endpoint: there is no local corpus to browse or index.
  const isRemote = kb.source_kind === 'mcp'
  const SourceIcon = KB_SOURCE_ICON[kb.source_kind]

  const handleDelete = async () => {
    if (!(await confirm(t('row.deleteConfirm', { name: kb.name })))) return
    del.mutate()
  }

  return (
    <div className="h-full flex flex-col bg-background">
      {/* Header */}
      <div className="flex items-center justify-between gap-3 px-4 py-3 bg-card border-b">
        <div className="flex items-center gap-3 min-w-0">
          <Library className="w-4 h-4 text-muted-foreground shrink-0" aria-hidden />
          <h1 className="font-semibold text-foreground truncate">{kb.name}</h1>
          <KBStatusBadge kb={kb} />
          <span className="flex items-center gap-1 text-xs text-muted-foreground shrink-0">
            <SourceIcon className="w-3.5 h-3.5" aria-hidden />
            {t(`sourceKind.${kb.source_kind}`)}
          </span>
        </div>

        <div className="flex items-center gap-2 shrink-0">
          {ready && (
            <span className="text-xs text-muted-foreground tabular-nums mr-1">
              {t('row.counts', { docs: kb.doc_count, chunks: kb.chunk_count })}
            </span>
          )}

          {/* Enable/disable gates agent visibility; only meaningful once indexed. */}
          {ready && (
            <Switch
              aria-label={kb.status === 'enabled' ? t('row.disable') : t('row.enable')}
              checked={kb.status === 'enabled'}
              disabled={toggle.isPending}
              onCheckedChange={(next) => toggle.mutate(next)}
            />
          )}

          {failed && (
            <Button
              variant="outline"
              size="sm"
              disabled={retry.isPending}
              onClick={() => retry.mutate()}
            >
              <RefreshCw className="w-4 h-4 mr-1" aria-hidden />
              {t('row.retry')}
            </Button>
          )}

          {/* Re-read the source from disk. Not offered for a remote KB (nothing
              local to index) or mid-ingest (the server rejects a second run). */}
          {!isRemote && !busy && (
            <Button
              variant="ghost"
              size="sm"
              className="h-8 w-8 p-0"
              title={t('detail.reindex')}
              aria-label={t('detail.reindex')}
              disabled={reindex.isPending}
              onClick={() => reindex.mutate()}
            >
              <RotateCcw className="w-4 h-4" aria-hidden />
            </Button>
          )}

          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0"
            title={t('row.edit')}
            aria-label={t('row.edit')}
            onClick={() => setEditOpen(true)}
          >
            <Pencil className="w-4 h-4" aria-hidden />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0"
            title={t('row.delete')}
            aria-label={t('row.delete')}
            disabled={del.isPending}
            onClick={handleDelete}
          >
            <Trash2 className="w-4 h-4" aria-hidden />
          </Button>
        </div>
      </div>

      {/* Live ingest progress — the one thing that must stay visible across tabs. */}
      {busy && (
        <div className="px-4 py-2 border-b bg-card flex flex-col gap-1">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>{t(`ingest.${kb.ingest_status}`)}</span>
            <span className="tabular-nums">{Math.round(kb.ingest_progress)}%</span>
          </div>
          <KBProgressBar value={kb.ingest_progress} />
        </div>
      )}

      {failed && kb.ingest_error && (
        <div className="px-4 py-2 border-b bg-card flex items-start gap-2">
          <AlertCircle
            className="w-4 h-4 text-destructive shrink-0 mt-0.5"
            aria-hidden
          />
          <p className="text-xs text-destructive break-words">{kb.ingest_error}</p>
        </div>
      )}

      {/* Tabs */}
      <div className="flex items-center gap-1 px-4 bg-card border-b">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              'flex items-center gap-2 px-3 py-2 text-sm font-medium border-b-2 -mb-px transition-colors',
              activeTab === tab.id
                ? 'border-brand text-brand'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )}
          >
            <tab.icon className="w-4 h-4" aria-hidden />
            {tab.label}
          </button>
        ))}
      </div>

      <div className="flex-1 min-h-0 overflow-hidden">
        {activeTab === 'overview' && <KBOverviewTab kb={kb} />}
        {activeTab === 'documents' && <KBDocumentsTab kb={kb} />}
        {activeTab === 'search' && <KBSearchTab kb={kb} />}
        {activeTab === 'settings' && <KBSettingsTab kb={kb} onEdit={() => setEditOpen(true)} />}
      </div>

      <KnowledgeBaseDialog
        open={editOpen}
        onOpenChange={setEditOpen}
        editing={kb}
      />
    </div>
  )
}
