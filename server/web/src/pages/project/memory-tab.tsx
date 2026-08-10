import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Plus, Pencil, Trash2, Check, X, History, FileText, Settings2, Play } from 'lucide-react'
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
import {
  useProjectMemories,
  useCreateMemory,
  useUpdateMemory,
  useDeleteMemory,
  useMemorySchedule,
  useRunMaintenanceOnce,
} from '@/lib/hooks/use-memory'
import { MemoryVersionDialog } from './memory-version-dialog'
import { MemoryMaintenancePanel } from './memory-maintenance-panel'
import { SOURCE_BADGE_CLASS } from '@/lib/memory-source-badge'
import type { Memory, MemoryType } from '@/types/api'
import type { OwnerRef } from '@/types/org'

interface ProjectMemoryTabProps {
  projectId: number
  owner?: OwnerRef
}

const TYPE_ORDER: MemoryType[] = ['decision', 'pattern', 'gotcha', 'error_fix', 'note', 'reference']

const TYPE_COLOR: Record<MemoryType, string> = {
  gotcha: 'bg-destructive/10 text-destructive',
  pattern: 'bg-info/10 text-info',
  decision: 'bg-success/10 text-success',
  error_fix: 'bg-warning/10 text-warning',
  note: 'bg-muted text-muted-foreground',
  reference: 'bg-accent text-accent-foreground',
}

const emptyForm = { mem_type: 'note' as string, title: '', content: '', source_path: '' }

export function ProjectMemoryTab({ projectId, owner }: ProjectMemoryTabProps) {
  const { t } = useTranslation('projects')
  const { data: memories = [], isLoading } = useProjectMemories(projectId)
  const createMutation = useCreateMemory(projectId)
  const updateMutation = useUpdateMemory(projectId)
  const deleteMutation = useDeleteMemory(projectId)

  const [showCreate, setShowCreate] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editForm, setEditForm] = useState(emptyForm)
  const [createForm, setCreateForm] = useState(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState<Memory | null>(null)
  const [versionTarget, setVersionTarget] = useState<Memory | null>(null)
  const [showMaintenance, setShowMaintenance] = useState(false)
  const { data: schedule } = useMemorySchedule(projectId)
  const maintEnabled = !!schedule?.cron?.trim()
  const runOnce = useRunMaintenanceOnce(projectId)

  const typeLabel = (key: MemoryType) => t(`tabs.memory.types.${key}`)

  const grouped = TYPE_ORDER.map((key) => ({
    key,
    items: memories.filter((m: Memory) => m.mem_type === key),
  })).filter((g) => g.items.length > 0)

  const startEdit = (m: Memory) => {
    setEditingId(m.id)
    setEditForm({ mem_type: m.mem_type, title: m.title, content: m.content, source_path: m.source_path })
  }

  const saveEdit = (id: number) => {
    if (!editForm.title.trim()) return
    updateMutation.mutate({ id, ...editForm }, { onSuccess: () => setEditingId(null) })
  }

  const handleCreate = () => {
    if (!createForm.title.trim()) return
    createMutation.mutate(
      { ...createForm, project_id: projectId, owner },
      {
        onSuccess: () => {
          setShowCreate(false)
          setCreateForm(emptyForm)
        },
      },
    )
  }

  const handleDelete = () => {
    if (!deleteTarget) return
    deleteMutation.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) })
  }

  const jumpSource = (path: string) => {
    void navigator.clipboard?.writeText(path)
    toast.success(t('tabs.memory.sourceCopied', { path }))
  }

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">{t('tabs.memory.loading')}</div>
  }

  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-medium text-foreground">
          {t('tabs.memory.title')}
          {memories.length > 0 && (
            <span className="ml-2 text-xs text-muted-foreground/70">({memories.length})</span>
          )}
        </h3>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setShowCreate(true)}
            className="inline-flex items-center gap-1 px-2.5 py-1 text-xs bg-card border rounded-md text-foreground hover:bg-accent transition-colors"
          >
            <Plus className="w-3 h-3" />
            {t('tabs.memory.add')}
          </button>
          <button
            onClick={() => setShowMaintenance((v) => !v)}
            title={t('tabs.memory.maintenance.entry')}
            className={`inline-flex items-center gap-1 px-2.5 py-1 text-xs border rounded-md transition-colors ${
              showMaintenance ? 'bg-accent text-foreground' : 'bg-card text-foreground hover:bg-accent'
            }`}
          >
            <Settings2 className="w-3 h-3" />
            {t('tabs.memory.maintenance.entry')}
          </button>
          <span
            className={`inline-flex items-center gap-1 text-xs ${
              maintEnabled ? 'text-success' : 'text-muted-foreground/70'
            }`}
          >
            <span className={`w-1.5 h-1.5 rounded-full ${maintEnabled ? 'bg-success' : 'bg-muted-foreground/40'}`} />
            {maintEnabled ? t('tabs.memory.maintenance.statusOn') : t('tabs.memory.maintenance.statusOff')}
          </span>
          <button
            onClick={() => runOnce.mutate()}
            disabled={runOnce.isPending}
            title={t('tabs.memory.maintenance.runOnce')}
            className="inline-flex items-center gap-1 px-2.5 py-1 text-xs bg-card border rounded-md text-foreground hover:bg-accent transition-colors disabled:opacity-50"
          >
            <Play className="w-3 h-3" />
            {runOnce.isPending ? t('tabs.memory.maintenance.running') : t('tabs.memory.maintenance.runOnce')}
          </button>
        </div>
      </div>

      {/* Inline maintenance panel: slides open/closed above the memory list. */}
      <div
        className={`grid !mt-0 transition-[grid-template-rows] duration-200 ease-out ${
          showMaintenance ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'
        }`}
      >
        <div className="overflow-hidden min-h-0">
          <div className="pt-6">
            <MemoryMaintenancePanel projectId={projectId} />
          </div>
        </div>
      </div>

      {showCreate && (
        <div className="border rounded-lg p-3 space-y-2 bg-muted">
          <select
            value={createForm.mem_type}
            onChange={(e) => setCreateForm({ ...createForm, mem_type: e.target.value })}
            className="w-full text-xs border rounded px-2 py-1.5 bg-background"
          >
            {TYPE_ORDER.map((key) => (
              <option key={key} value={key}>{typeLabel(key)}</option>
            ))}
          </select>
          <input
            type="text"
            placeholder={t('tabs.memory.titlePlaceholder')}
            value={createForm.title}
            onChange={(e) => setCreateForm({ ...createForm, title: e.target.value })}
            className="w-full text-xs border rounded px-2 py-1.5 bg-background"
          />
          <textarea
            placeholder={t('tabs.memory.contentPlaceholder')}
            rows={3}
            value={createForm.content}
            onChange={(e) => setCreateForm({ ...createForm, content: e.target.value })}
            className="w-full text-xs border rounded px-2 py-1.5 resize-none bg-background"
          />
          <input
            type="text"
            placeholder={t('tabs.memory.sourcePathPlaceholder')}
            value={createForm.source_path}
            onChange={(e) => setCreateForm({ ...createForm, source_path: e.target.value })}
            className="w-full text-xs border rounded px-2 py-1.5 bg-background font-mono"
          />
          <div className="flex gap-1.5 justify-end">
            <button
              onClick={() => { setShowCreate(false); setCreateForm(emptyForm) }}
              className="px-2 py-1 text-xs text-muted-foreground hover:bg-accent rounded"
            >
              {t('tabs.memory.cancel')}
            </button>
            <button
              onClick={handleCreate}
              disabled={!createForm.title.trim() || createMutation.isPending}
              className="px-2 py-1 text-xs bg-primary text-primary-foreground rounded hover:bg-primary/90 disabled:opacity-50"
            >
              {t('tabs.memory.save')}
            </button>
          </div>
        </div>
      )}

      {grouped.length === 0 && !showCreate && (
        <div className="text-sm text-muted-foreground text-center py-8">{t('tabs.memory.empty')}</div>
      )}

      {grouped.map((group) => (
        <div key={group.key} className="space-y-2">
          <div>
            <span className={`inline-flex items-center px-2 py-0.5 rounded text-[10px] font-medium ${TYPE_COLOR[group.key]}`}>
              {typeLabel(group.key)}
            </span>
          </div>
          <div className="space-y-1.5">
            {group.items.map((memory: Memory) => (
              <div key={memory.id} className="border rounded-lg px-3 py-2 bg-card text-xs group">
                {editingId === memory.id ? (
                  <div className="space-y-1.5">
                    <select
                      value={editForm.mem_type}
                      onChange={(e) => setEditForm({ ...editForm, mem_type: e.target.value })}
                      className="w-full text-xs border rounded px-2 py-1 bg-background"
                    >
                      {TYPE_ORDER.map((key) => (
                        <option key={key} value={key}>{typeLabel(key)}</option>
                      ))}
                    </select>
                    <input
                      type="text"
                      value={editForm.title}
                      onChange={(e) => setEditForm({ ...editForm, title: e.target.value })}
                      className="w-full text-xs border rounded px-2 py-1 bg-background"
                    />
                    <textarea
                      value={editForm.content}
                      onChange={(e) => setEditForm({ ...editForm, content: e.target.value })}
                      className="w-full text-xs border rounded px-2 py-1 resize-none bg-background"
                      rows={3}
                    />
                    <input
                      type="text"
                      value={editForm.source_path}
                      onChange={(e) => setEditForm({ ...editForm, source_path: e.target.value })}
                      placeholder={t('tabs.memory.sourcePathPlaceholder')}
                      className="w-full text-xs border rounded px-2 py-1 bg-background font-mono"
                    />
                    <div className="flex gap-1 justify-end">
                      <button onClick={() => setEditingId(null)} className="p-1 text-muted-foreground hover:text-foreground">
                        <X className="w-3 h-3" />
                      </button>
                      <button
                        onClick={() => saveEdit(memory.id)}
                        disabled={updateMutation.isPending}
                        className="p-1 text-info hover:text-info/80"
                      >
                        <Check className="w-3 h-3" />
                      </button>
                    </div>
                  </div>
                ) : (
                  <>
                    <div className="flex items-start justify-between gap-2">
                      <div className="flex-1 min-w-0">
                        <span className="font-medium text-foreground">{memory.title}</span>
                        {memory.content && (
                          <p className="text-muted-foreground mt-0.5 line-clamp-3 whitespace-pre-wrap">{memory.content}</p>
                        )}
                      </div>
                      <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
                        <button
                          onClick={() => setVersionTarget(memory)}
                          title={t('tabs.memory.versionTitle')}
                          className="p-0.5 text-muted-foreground hover:text-foreground"
                        >
                          <History className="w-3 h-3" />
                        </button>
                        <button onClick={() => startEdit(memory)} className="p-0.5 text-muted-foreground hover:text-foreground">
                          <Pencil className="w-3 h-3" />
                        </button>
                        <button
                          onClick={() => setDeleteTarget(memory)}
                          className="p-0.5 text-muted-foreground hover:text-destructive"
                        >
                          <Trash2 className="w-3 h-3" />
                        </button>
                      </div>
                    </div>
                    <div className="flex items-center flex-wrap gap-2 mt-1.5">
                      <span className={`inline-flex items-center px-1.5 py-0.5 rounded border text-[9px] ${SOURCE_BADGE_CLASS[memory.source] || ''}`}>
                        {t(`tabs.memory.sources.${memory.source}`)}
                      </span>
                      <span className="text-[10px] text-muted-foreground/70">v{memory.version}</span>
                      {memory.source_path && (
                        <button
                          onClick={() => jumpSource(memory.source_path)}
                          title={memory.source_path}
                          className="inline-flex items-center gap-0.5 text-[10px] text-info hover:underline max-w-[16rem] truncate font-mono"
                        >
                          <FileText className="w-2.5 h-2.5 shrink-0" />
                          <span className="truncate">{memory.source_path}</span>
                        </button>
                      )}
                    </div>
                  </>
                )}
              </div>
            ))}
          </div>
        </div>
      ))}

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('tabs.memory.deleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('tabs.memory.deleteDescription', { title: deleteTarget?.title ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('tabs.memory.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete} className="bg-destructive hover:bg-destructive/90">
              {t('tabs.memory.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <MemoryVersionDialog memory={versionTarget} projectId={projectId} onClose={() => setVersionTarget(null)} />
    </div>
  )
}
