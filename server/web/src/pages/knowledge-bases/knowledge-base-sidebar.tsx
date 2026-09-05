import { useState, useMemo } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Plus, Search, Library } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useKnowledgeBases } from '@/lib/hooks/use-knowledge-bases'
import { KnowledgeBaseDialog } from '@/components/knowledge/knowledge-base-dialog'
import { KBStatusDot } from '@/components/knowledge/kb-status'
import { isKBBusy, type KnowledgeBase } from '@/lib/kb-api'

/**
 * Master list for /knowledge-bases, mirroring RepositorySidebar so a KB reads as
 * the same class of object as a repository: persistent list, filter box, and the
 * create action anchored in the header rather than buried in a settings panel.
 */
export function KnowledgeBaseSidebar() {
  const { t } = useTranslation('knowledge')
  const { knowledgeBases, isLoading } = useKnowledgeBases()
  const [search, setSearch] = useState('')
  const [createOpen, setCreateOpen] = useState(false)

  const params = useParams({ strict: false })
  const activeId = (params as Record<string, string | undefined>).id

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    const items = knowledgeBases ?? []
    if (!q) return items
    // Match name or description: a corpus is often easier to recall by what it
    // contains than by the name it was given.
    return items.filter(
      (kb) =>
        kb.name.toLowerCase().includes(q) ||
        kb.description.toLowerCase().includes(q),
    )
  }, [knowledgeBases, search])

  return (
    <aside className="border-r bg-muted flex flex-col h-full">
      <div className="flex items-center justify-between px-3 py-2 border-b">
        <span className="text-sm font-semibold text-foreground">
          {t('panel.title')}
        </span>
        <button
          className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
          aria-label={t('panel.add')}
          title={t('panel.add')}
          onClick={() => setCreateOpen(true)}
        >
          <Plus className="w-4 h-4" />
        </button>
      </div>

      <div className="px-2 py-1.5 border-b">
        <div className="flex items-center gap-1.5 bg-background border rounded-md px-2 py-1">
          <Search
            className="w-3.5 h-3.5 text-muted-foreground shrink-0"
            aria-hidden
          />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('list.searchPlaceholder')}
            aria-label={t('list.searchPlaceholder')}
            className="flex-1 text-xs outline-none bg-transparent text-foreground placeholder:text-muted-foreground"
          />
        </div>
      </div>

      <div className="flex-1 overflow-y-auto py-1.5">
        {isLoading ? (
          <div className="px-3 py-4 text-xs text-muted-foreground text-center">
            {t('common:actions.loading')}
          </div>
        ) : filtered.length === 0 ? (
          <div className="px-3 py-4 text-xs text-muted-foreground text-center">
            {search ? t('common:status.noMatchingResults') : t('list.empty')}
          </div>
        ) : (
          filtered.map((kb) => (
            <KBSidebarItem key={kb.id} kb={kb} active={activeId === String(kb.id)} />
          ))
        )}
      </div>

      <KnowledgeBaseDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        editing={null}
      />
    </aside>
  )
}

function KBSidebarItem({ kb, active }: { kb: KnowledgeBase; active: boolean }) {
  const { t } = useTranslation('knowledge')
  const busy = isKBBusy(kb)

  return (
    <Link
      to="/knowledge-bases/$id"
      params={{ id: String(kb.id) }}
      className={cn(
        'flex flex-col px-3 py-1.5 mx-1 rounded-r-md transition-colors border-l-2',
        active
          ? 'text-foreground bg-brand-soft border-brand'
          : 'text-foreground hover:bg-accent border-l-transparent',
      )}
    >
      <span className="flex items-center gap-1.5 min-w-0">
        <Library className="w-3.5 h-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium truncate flex-1">{kb.name}</span>
        <KBStatusDot kb={kb} />
      </span>
      <span className="text-xs text-muted-foreground truncate pl-5 tabular-nums">
        {busy
          ? t(`ingest.${kb.ingest_status}`)
          : t('row.counts', { docs: kb.doc_count, chunks: kb.chunk_count })}
      </span>
    </Link>
  )
}
