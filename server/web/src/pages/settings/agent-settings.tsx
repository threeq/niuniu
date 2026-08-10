import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  Plus,
  Trash2,
  Pencil,
  Bot,
  ChevronDown,
  ChevronRight,
  Download,
  RefreshCw,
  Package,
  Loader2,
  Search,
  HardDrive,
  Globe,
  User,
  Sparkles,
} from 'lucide-react'
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
import { cn } from '@/lib/utils'
import { confirm } from '@/lib/confirm'
import { agentFileApi } from '@/lib/team-api'
import type { AgentFile } from '@/lib/team-api'
import { registryApi } from '@/lib/registry-api'
import { OwnerBadge } from '@/components/shared/owner-badge'
import type { AgentInfo, AgentRegistryList } from '@/types/registry'
import type { OwnerRef } from '@/types/org'

// ─── Frontmatter helpers ────────────────────────────────────────────
//
// Agent .md files are stored with a YAML frontmatter block so Claude Code
// can discover them. The `name` and `description` fields mirror the DB row
// and are fully managed by the backend (see syncFrontmatterMetadata in
// server/internal/service/frontmatter.go). The edit dialog exposes them as
// form inputs and shows the generated frontmatter block read-only so users
// don't have to touch YAML — but can still see what gets written.

const MANAGED_KEYS = new Set(['name', 'description', 'managed_by'])

// Display order for the curated catalog's 分工分类 sections: engineering-first so
// developers see the most relevant agents before marketing personas.
const CURATED_CATEGORY_ORDER = ['engineering', 'security', 'design', 'marketing']

// splitAgentContent extracts non-managed frontmatter lines ("extras") and
// the body from a markdown string. Extras preserve arbitrary author-supplied
// fields (tools, model, color, multi-line lists) so round-tripping through
// the UI doesn't drop them.
function splitAgentContent(raw: string): { extras: string[]; body: string } {
  const normalized = raw.replace(/\r\n/g, '\n')
  if (!normalized.startsWith('---\n')) {
    return { extras: [], body: raw }
  }
  const rest = normalized.slice(4)
  const endIdx = rest.indexOf('\n---')
  if (endIdx < 0) {
    return { extras: [], body: raw }
  }
  const fmText = rest.slice(0, endIdx)
  const body = rest.slice(endIdx + 4).replace(/^\n/, '')

  const extras: string[] = []
  let skipping = false
  for (const line of fmText.split('\n')) {
    const indented = line.length > 0 && (line[0] === ' ' || line[0] === '\t')
    if (!indented) {
      const colonIdx = line.indexOf(':')
      if (colonIdx >= 0) {
        const key = line.slice(0, colonIdx).trim()
        if (MANAGED_KEYS.has(key)) {
          skipping = true
          continue
        }
      }
      skipping = false
    }
    if (!skipping) extras.push(line)
  }
  return { extras, body }
}

// buildFrontmatterPreview renders the frontmatter block the backend will
// persist: managed fields on top (synced from form inputs), preserved
// extras below. Displayed read-only to make the wire format transparent.
function buildFrontmatterPreview(name: string, description: string, extras: string[]): string {
  const lines = [`name: ${name}`]
  if (description) lines.push(`description: ${description}`)
  for (const line of extras) {
    if (line.trim() !== '') lines.push(line)
  }
  return `---\n${lines.join('\n')}\n---`
}

function composeAgentContent(name: string, description: string, extras: string[], body: string): string {
  return `${buildFrontmatterPreview(name, description, extras)}\n\n${body}`
}

function matchesSearch(name: string, description: string, search: string): boolean {
  if (!search) return true
  const s = search.toLowerCase()
  return name.toLowerCase().includes(s) || (description ?? '').toLowerCase().includes(s)
}

function agentOwner(agent: AgentInfo): OwnerRef | null {
  if (agent.source === 'local' && agent.author) {
    return { type: 'user', id: 0, name: agent.author }
  }
  return null
}

function sourceIcon(source: AgentInfo['source']) {
  if (source === 'local') return <HardDrive className="h-3 w-3" />
  if (source === 'community') return <Globe className="h-3 w-3" />
  if (source === 'curated') return <Sparkles className="h-3 w-3" />
  return <User className="h-3 w-3" />
}

// ─── My Agent Card (upper zone) ─────────────────────────────────────

function MyAgentCard({
  agent,
  onEdit,
  onDelete,
}: {
  agent: AgentFile
  onEdit: (a: AgentFile) => void
  onDelete: (a: AgentFile) => void
}) {
  return (
    <div className="border border-border rounded-lg p-4 hover:border-info/40 transition-colors">
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-2 min-w-0">
          <Bot className="h-4 w-4 text-info flex-shrink-0" />
          <span className="font-medium text-sm text-foreground truncate">{agent.name}</span>
          <span className="text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground">
            {agent.driver}
          </span>
        </div>
        <div className="flex items-center gap-1 flex-shrink-0 ml-2">
          <button onClick={() => onEdit(agent)} className="p-1 text-muted-foreground hover:text-info">
            <Pencil className="h-3.5 w-3.5" />
          </button>
          <button onClick={() => onDelete(agent)} className="p-1 text-muted-foreground hover:text-destructive">
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      {agent.description && (
        <p className="mt-1 text-xs text-muted-foreground line-clamp-2">{agent.description}</p>
      )}
    </div>
  )
}

// ─── Registry Agent Card (lower zone) ───────────────────────────────

function RegistryAgentCard({
  agent,
  imported,
  onImport,
}: {
  agent: AgentInfo
  imported: boolean
  onImport: (a: AgentInfo) => void
}) {
  const { t } = useTranslation('settings')
  const displayName =
    agent.display_name || (agent.name.includes(':') ? agent.name.split(':').pop()! : agent.name)
  const owner = agentOwner(agent)

  return (
    <div
      className={cn(
        'border rounded-lg p-4 transition-colors',
        imported ? 'border-border bg-muted opacity-60' : 'border-border hover:border-info/40'
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 min-w-0">
            {agent.emoji ? (
              <span className="text-sm flex-shrink-0 leading-none" aria-hidden>{agent.emoji}</span>
            ) : (
              <Bot className="h-4 w-4 text-info flex-shrink-0" />
            )}
            <h3 className="text-sm font-medium text-foreground truncate">{displayName}</h3>
            <span
              className="flex items-center gap-1 text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground flex-shrink-0"
              title={t('agent.registry.sourceTitle', { source: agent.source })}
            >
              {sourceIcon(agent.source)}
              {agent.source}
            </span>
          </div>
          <p className="mt-1 text-xs text-muted-foreground line-clamp-2">
            {agent.description || t('agent.registry.noDescription')}
          </p>
          {owner && (
            <div className="mt-2">
              <OwnerBadge owner={owner} />
            </div>
          )}
        </div>
        {imported ? (
          <span className="text-xs text-muted-foreground flex-shrink-0 mt-1">{t('agent.registry.imported')}</span>
        ) : (
          <Button
            size="sm"
            variant="outline"
            className="flex-shrink-0"
            onClick={() => onImport(agent)}
          >
            <Download className="h-3.5 w-3.5 mr-1" />
            {t('agent.registry.import')}
          </Button>
        )}
      </div>
    </div>
  )
}

// ─── Registry Section ───────────────────────────────────────────────

function RegistrySection({
  title,
  icon,
  agents,
  importedNames,
  onImport,
  onRefresh,
  refreshing,
}: {
  title: string
  icon: React.ReactNode
  agents: AgentInfo[]
  importedNames: Set<string>
  onImport: (a: AgentInfo) => void
  onRefresh?: () => void
  refreshing?: boolean
}) {
  const { t } = useTranslation('settings')
  if (agents.length === 0) return null
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
          {icon}
          {title}
          <span className="text-xs text-muted-foreground font-normal bg-muted px-1.5 py-0.5 rounded-full">
            {agents.length}
          </span>
        </h3>
        {onRefresh && (
          <Button size="sm" variant="outline" onClick={onRefresh} disabled={refreshing}>
            <RefreshCw className={cn('h-3.5 w-3.5 mr-1', refreshing && 'animate-spin')} />
            {t('common:actions.refresh')}
          </Button>
        )}
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
        {agents.map((agent) => (
          <RegistryAgentCard
            key={`${agent.source}:${agent.name}`}
            agent={agent}
            imported={importedNames.has(agent.name)}
            onImport={onImport}
          />
        ))}
      </div>
    </div>
  )
}

// ─── Main Component ─────────────────────────────────────────────────

export function AgentSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()

  // ── Search ──
  const [search, setSearch] = useState('')

  // ── My Agents (DB-backed) ──
  const { data: agents = [], isLoading: agentsLoading } = useQuery({
    queryKey: ['agents-file'],
    queryFn: () => agentFileApi.list(),
  })

  // ── Registry (all sources) ──
  const { data: registry, isLoading: registryLoading } = useQuery<AgentRegistryList>({
    queryKey: ['agent-registry'],
    queryFn: () => registryApi.list(),
  })

  // ── Dialog state ──
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingAgent, setEditingAgent] = useState<AgentFile | null>(null)
  const [formName, setFormName] = useState('')
  const [formDesc, setFormDesc] = useState('')
  // formBody holds the markdown body only (after frontmatter). Frontmatter is
  // reconstructed on save from formName/formDesc + formExtras.
  const [formBody, setFormBody] = useState('')
  const [formExtras, setFormExtras] = useState<string[]>([])

  const frontmatterPreview = useMemo(
    () => buildFrontmatterPreview(formName, formDesc, formExtras),
    [formName, formDesc, formExtras],
  )

  // ── Collapsible sections ──
  const [myAgentsCollapsed, setMyAgentsCollapsed] = useState(false)
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())

  // ── Derived: set of imported agent names for matching ──
  const importedNames = useMemo(() => new Set(agents.map((a) => a.name)), [agents])

  // ── Search-filtered lists ──
  const filteredMyAgents = useMemo(
    () => agents.filter((a) => matchesSearch(a.name, a.description, search)),
    [agents, search]
  )

  // Wrap source arrays in useMemo so the `?? []` fallback keeps a stable
  // reference across renders and downstream useMemos don't recompute on every
  // loading-state render.
  const localAgents = useMemo<AgentInfo[]>(() => registry?.['local'] ?? [], [registry])
  const communityAgents = useMemo<AgentInfo[]>(() => registry?.['community'] ?? [], [registry])
  const customAgents = useMemo<AgentInfo[]>(() => registry?.['custom'] ?? [], [registry])
  const curatedAgents = useMemo<AgentInfo[]>(() => registry?.['curated'] ?? [], [registry])

  // Curated catalog (精选目录) grouped by 分工分类 (the first tag). The order keeps
  // engineering-first so devs see the most relevant agents before marketing.
  const groupedCuratedAgents = useMemo(() => {
    const filtered = curatedAgents.filter((a) =>
      matchesSearch(a.display_name || a.name, a.description, search)
    )
    if (filtered.length === 0) return []
    const groups: Record<string, AgentInfo[]> = {}
    for (const agent of filtered) {
      const cat = agent.tags?.[0] || 'other'
      ;(groups[cat] ??= []).push(agent)
    }
    return Object.entries(groups).sort(([a], [b]) => {
      const ia = CURATED_CATEGORY_ORDER.indexOf(a)
      const ib = CURATED_CATEGORY_ORDER.indexOf(b)
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
    })
  }, [curatedAgents, search])

  const groupedLocalAgents = useMemo(() => {
    const filtered = localAgents.filter((a) => matchesSearch(a.name, a.description, search))
    if (filtered.length === 0) return []
    const groups: Record<string, AgentInfo[]> = {}
    for (const agent of filtered) {
      const group = agent.author || 'builtin'
      ;(groups[group] ??= []).push(agent)
    }
    return Object.entries(groups).sort(([a], [b]) => {
      if (a === 'builtin') return 1
      if (b === 'builtin') return -1
      return a.localeCompare(b)
    })
  }, [localAgents, search])

  const filteredCommunityAgents = useMemo(
    () => communityAgents.filter((a) => matchesSearch(a.name, a.description, search)),
    [communityAgents, search]
  )

  const filteredCustomAgents = useMemo(
    () => customAgents.filter((a) => matchesSearch(a.name, a.description, search)),
    [customAgents, search]
  )

  const registryTotal =
    localAgents.length + communityAgents.length + customAgents.length + curatedAgents.length
  const registryFilteredTotal =
    groupedLocalAgents.reduce((acc, [, gs]) => acc + gs.length, 0) +
    groupedCuratedAgents.reduce((acc, [, gs]) => acc + gs.length, 0) +
    filteredCommunityAgents.length +
    filteredCustomAgents.length

  // ── Agent CRUD mutations ──
  const saveMutation = useMutation({
    mutationFn: async () => {
      const content = composeAgentContent(formName, formDesc, formExtras, formBody)
      if (editingAgent) {
        await agentFileApi.update(editingAgent.id, {
          description: formDesc,
          content,
        })
      } else {
        await agentFileApi.create({
          name: formName,
          description: formDesc,
          content,
        })
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agents-file'] })
      setDialogOpen(false)
      resetForm()
    },
    onError: (err: Error) => {
      toast.error(t('agent.saveFailed', { message: err.message }))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => agentFileApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agents-file'] })
    },
    onError: (err: Error) => {
      toast.error(t('agent.deleteFailed', { message: err.message }))
    },
  })

  // ── Registry mutations ──
  const refreshMutation = useMutation({
    mutationFn: (source: string) => registryApi.refresh(source),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agent-registry'] })
    },
    onError: (err: Error) => {
      toast.error(t('agent.refreshFailed', { message: err.message }))
    },
  })

  const importMutation = useMutation({
    mutationFn: async (agent: AgentInfo) => {
      const detail = await registryApi.get(agent.source, agent.name)
      await agentFileApi.create({
        name: agent.name,
        description: detail.description || agent.description,
        content: detail.content,
        // Record provenance (e.g. the upstream agency-agents URL for curated
        // imports) so the installed agent can be traced back to its source.
        source_url: detail.source_url || agent.source_url || undefined,
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agents-file'] })
    },
    onError: (err: Error) => {
      toast.error(t('agent.importFailed', { message: err.message }))
    },
  })

  // ── Helpers ──
  function resetForm() {
    setEditingAgent(null)
    setFormName('')
    setFormDesc('')
    setFormBody('')
    setFormExtras([])
  }

  function openCreate() {
    resetForm()
    setDialogOpen(true)
  }

  function openEdit(agent: AgentFile) {
    agentFileApi
      .get(agent.id)
      .then((detail) => {
        const { extras, body } = splitAgentContent(detail.content)
        setEditingAgent(agent)
        setFormName(agent.name)
        setFormDesc(detail.description)
        setFormBody(body)
        setFormExtras(extras)
        setDialogOpen(true)
      })
      .catch((err: Error) => {
        toast.error(t('agent.loadDetailFailed', { message: err.message }))
      })
  }

  async function handleDelete(agent: AgentFile) {
    if (!(await confirm(t('agent.deleteConfirm', { name: agent.name })))) return
    deleteMutation.mutate(agent.id)
  }

  function toggleGroup(group: string) {
    setCollapsedGroups((prev) => {
      const next = new Set(prev)
      if (next.has(group)) next.delete(group)
      else next.add(group)
      return next
    })
  }

  const refreshingSource = refreshMutation.isPending
    ? (refreshMutation.variables as string | undefined)
    : undefined

  return (
    <div className="py-4 space-y-6">
      {/* ═══ Search ═══ */}
      <div className="relative">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground pointer-events-none" />
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('agent.search')}
          className="pl-9"
        />
      </div>

      {/* ═══ Upper Zone: 我的 Agent ═══ */}
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <button
            onClick={() => setMyAgentsCollapsed(!myAgentsCollapsed)}
            className="flex items-center gap-1.5 text-sm font-semibold text-foreground hover:text-foreground/80 transition-colors"
          >
            {myAgentsCollapsed ? (
              <ChevronRight className="w-4 h-4" />
            ) : (
              <ChevronDown className="w-4 h-4" />
            )}
            {t('agent.myAgents.title')}
            <span className="text-xs text-muted-foreground font-normal bg-muted px-1.5 py-0.5 rounded-full">
              {search ? `${filteredMyAgents.length}/${agents.length}` : agents.length}
            </span>
          </button>
          <Button size="sm" variant="outline" onClick={openCreate}>
            <Plus className="h-3.5 w-3.5 mr-1" />
            {t('agent.myAgents.newAgent')}
          </Button>
        </div>

        {!myAgentsCollapsed && (
          agentsLoading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
              <span className="ml-2 text-sm text-muted-foreground">{t('agent.myAgents.loading')}</span>
            </div>
          ) : agents.length === 0 ? (
            <p className="text-sm text-muted-foreground py-8 text-center">{t('agent.myAgents.empty')}</p>
          ) : filteredMyAgents.length === 0 ? (
            <p className="text-sm text-muted-foreground py-8 text-center">
              {t('agent.myAgents.noMatch')}
            </p>
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
              {filteredMyAgents.map((agent) => (
                <MyAgentCard
                  key={agent.id}
                  agent={agent}
                  onEdit={openEdit}
                  onDelete={handleDelete}
                />
              ))}
            </div>
          )
        )}
      </div>

      {/* ═══ Divider ═══ */}
      <hr className="border-border" />

      {/* ═══ Lower Zone: Agent 注册表 ═══ */}
      <div className="space-y-6">
        <div>
          <h2 className="text-sm font-semibold text-foreground">{t('agent.registry.title')}</h2>
          <p className="text-xs text-muted-foreground mt-0.5">
            {search
              ? t('agent.registry.summaryFiltered', { filtered: registryFilteredTotal, total: registryTotal })
              : t('agent.registry.summary', { total: registryTotal })}
          </p>
        </div>

        {registryLoading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
            <span className="ml-2 text-sm text-muted-foreground">{t('agent.registry.loading')}</span>
          </div>
        ) : registryTotal === 0 ? (
          <div className="text-center py-8 text-muted-foreground">
            <Bot className="h-8 w-8 mx-auto mb-3 opacity-40" />
            <p className="text-sm">{t('agent.registry.empty')}</p>
            <p className="text-xs mt-1">{t('agent.registry.emptyHint')}</p>
          </div>
        ) : search && registryFilteredTotal === 0 ? (
          <p className="text-sm text-muted-foreground py-8 text-center">
            {t('agent.registry.noMatch')}
          </p>
        ) : (
          <div className="space-y-8">
            {/* ── 精选目录 (curated catalog, grouped by 分工分类) ── */}
            {groupedCuratedAgents.length > 0 && (
              <div className="space-y-3">
                <div>
                  <h3 className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
                    <Sparkles className="h-4 w-4 text-info" />
                    {t('agent.registry.curated')}
                    <span className="text-xs text-muted-foreground font-normal bg-muted px-1.5 py-0.5 rounded-full">
                      {groupedCuratedAgents.reduce((acc, [, gs]) => acc + gs.length, 0)}
                    </span>
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">{t('agent.registry.curatedHint')}</p>
                </div>
                <div className="space-y-4">
                  {groupedCuratedAgents.map(([category, categoryAgents]) => {
                    const groupKey = `curated:${category}`
                    const isCollapsed = collapsedGroups.has(groupKey)
                    return (
                      <div key={groupKey}>
                        <button
                          onClick={() => toggleGroup(groupKey)}
                          className="flex items-center gap-1.5 mb-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors"
                        >
                          {isCollapsed ? (
                            <ChevronRight className="w-4 h-4" />
                          ) : (
                            <ChevronDown className="w-4 h-4" />
                          )}
                          {t(`agent.registry.category.${category}`, { defaultValue: category })}
                          <span className="text-xs text-muted-foreground font-normal bg-muted px-1.5 py-0.5 rounded-full">
                            {categoryAgents.length}
                          </span>
                        </button>
                        {!isCollapsed && (
                          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
                            {categoryAgents.map((agent) => (
                              <RegistryAgentCard
                                key={`${agent.source}:${agent.name}`}
                                agent={agent}
                                imported={importedNames.has(agent.name)}
                                onImport={(a) => importMutation.mutate(a)}
                              />
                            ))}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              </div>
            )}

            {/* ── 本地 Agent (grouped by author) ── */}
            {groupedLocalAgents.length > 0 && (
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <h3 className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
                    <HardDrive className="h-4 w-4" />
                    {t('agent.registry.local')}
                    <span className="text-xs text-muted-foreground font-normal bg-muted px-1.5 py-0.5 rounded-full">
                      {groupedLocalAgents.reduce((acc, [, gs]) => acc + gs.length, 0)}
                    </span>
                  </h3>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => refreshMutation.mutate('local')}
                    disabled={refreshingSource === 'local'}
                  >
                    <RefreshCw
                      className={cn(
                        'h-3.5 w-3.5 mr-1',
                        refreshingSource === 'local' && 'animate-spin'
                      )}
                    />
                    {t('common:actions.refresh')}
                  </Button>
                </div>
                <div className="space-y-4">
                  {groupedLocalAgents.map(([group, groupAgents]) => {
                    const isCollapsed = collapsedGroups.has(group)
                    return (
                      <div key={group}>
                        <button
                          onClick={() => toggleGroup(group)}
                          className="flex items-center gap-1.5 mb-2 text-sm font-medium text-muted-foreground hover:text-foreground transition-colors"
                        >
                          {isCollapsed ? (
                            <ChevronRight className="w-4 h-4" />
                          ) : (
                            <ChevronDown className="w-4 h-4" />
                          )}
                          <Package className="w-4 h-4" />
                          {group === 'builtin' ? t('agent.registry.builtin') : group}
                          <span className="text-xs text-muted-foreground font-normal bg-muted px-1.5 py-0.5 rounded-full">
                            {groupAgents.length}
                          </span>
                        </button>
                        {!isCollapsed && (
                          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
                            {groupAgents.map((agent) => (
                              <RegistryAgentCard
                                key={`${agent.source}:${agent.name}`}
                                agent={agent}
                                imported={importedNames.has(agent.name)}
                                onImport={(a) => importMutation.mutate(a)}
                              />
                            ))}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              </div>
            )}

            {/* ── 社区 Agent ── */}
            <RegistrySection
              title={t('agent.registry.community')}
              icon={<Globe className="h-4 w-4" />}
              agents={filteredCommunityAgents}
              importedNames={importedNames}
              onImport={(a) => importMutation.mutate(a)}
              onRefresh={() => refreshMutation.mutate('community')}
              refreshing={refreshingSource === 'community'}
            />

            {/* ── 自定义 Agent ── */}
            <RegistrySection
              title={t('agent.registry.custom')}
              icon={<User className="h-4 w-4" />}
              agents={filteredCustomAgents}
              importedNames={importedNames}
              onImport={(a) => importMutation.mutate(a)}
            />
          </div>
        )}
      </div>

      {/* ═══ Create/Edit Agent Dialog ═══ */}
      <Dialog
        open={dialogOpen}
        onOpenChange={(open) => {
          setDialogOpen(open)
          if (!open) resetForm()
        }}
      >
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editingAgent ? t('agent.dialog.editTitle') : t('agent.dialog.createTitle')}</DialogTitle>
            <DialogDescription>
              {editingAgent
                ? t('agent.dialog.editDescription')
                : t('agent.dialog.createDescription')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div>
              <label className="text-sm font-medium text-foreground">
                {t('agent.dialog.nameLabel')} <span className="text-destructive">*</span>
              </label>
              <Input
                value={formName}
                onChange={(e) => setFormName(e.target.value)}
                placeholder={t('agent.dialog.namePlaceholder')}
                disabled={!!editingAgent}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-sm font-medium text-foreground">{t('agent.dialog.descLabel')}</label>
              <Input
                value={formDesc}
                onChange={(e) => setFormDesc(e.target.value)}
                placeholder={t('agent.dialog.descPlaceholder')}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-sm font-medium text-foreground">{t('agent.dialog.frontmatterLabel')}</label>
              <div className="mt-1 relative">
                <pre
                  aria-readonly="true"
                  className="w-full px-3 py-2 border border-border rounded-md text-xs font-mono bg-muted text-muted-foreground whitespace-pre-wrap select-text overflow-auto max-h-40"
                >
                  {frontmatterPreview}
                </pre>
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {t('agent.dialog.frontmatterHint')}
              </p>
            </div>
            <div>
              <label className="text-sm font-medium text-foreground">
                {t('agent.dialog.bodyLabel')} <span className="text-destructive">*</span>
              </label>
              <textarea
                value={formBody}
                onChange={(e) => setFormBody(e.target.value)}
                placeholder={t('agent.dialog.bodyPlaceholder')}
                className="mt-1 w-full h-64 px-3 py-2 border border-border rounded-md text-sm font-mono resize-y bg-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              onClick={() => saveMutation.mutate()}
              disabled={
                (!editingAgent && !formName.trim()) ||
                !formBody.trim() ||
                saveMutation.isPending
              }
            >
              {saveMutation.isPending ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
