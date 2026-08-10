import { useEffect, useState, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { GripVertical, Pencil, Trash2, Check, X, Send, Combine } from 'lucide-react'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import type { QueueItem } from '@/types/api'
import { isIMEComposing } from '@/lib/ime'

// ---------------------------------------------------------------------------
// SortableItem
// ---------------------------------------------------------------------------

interface SortableItemProps {
  item: QueueItem
  onDelete: (id: number) => void
  onEdit: (id: number, content: string) => void
  onSendToInput?: (content: string) => void
  showSendButton: boolean
  // Fusion send (interrupt): stops the agent — which also clears any pending
  // background wakeup holding the queue — concatenates the last sent message
  // with this queued item, and refills chat-input. Offered in every session
  // state so a queued message stuck behind an idle background wait can still be
  // interrupted and run now (not only while a turn is live).
  onFuseToInput?: (content: string) => void
  showFuseButton: boolean
}

function SortableItem({ item, onDelete, onEdit, onSendToInput, showSendButton, onFuseToInput, showFuseButton }: SortableItemProps) {
  const { t } = useTranslation('workspaces')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(item.content)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: item.id })

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
  }

  useEffect(() => {
    if (editing && textareaRef.current) {
      textareaRef.current.focus()
      textareaRef.current.select()
    }
  }, [editing])

  const handleSave = () => {
    const trimmed = draft.trim()
    if (trimmed && trimmed !== item.content) {
      onEdit(item.id, trimmed)
    }
    setEditing(false)
  }

  const handleCancel = () => {
    setDraft(item.content)
    setEditing(false)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (isIMEComposing(e)) return
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSave()
    } else if (e.key === 'Escape') {
      handleCancel()
    }
  }

  const actionBtn = 'h-6 w-6 rounded text-warm-text-muted'

  return (
    <div
      ref={setNodeRef}
      style={style}
      className={cn(
        'group flex items-start gap-2 rounded-md border border-warm-border bg-warm-surface px-2 py-1.5 text-sm shadow-sm',
        'transition-shadow duration-150 hover:shadow-md',
        isDragging && 'opacity-50 shadow-lg',
      )}
    >
      {/* Drag handle */}
      <button
        type="button"
        aria-label={t('panels.queue.drag')}
        className="mt-0.5 cursor-grab rounded text-warm-text-muted/70 hover:text-warm-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="h-4 w-4" aria-hidden="true" />
      </button>

      {/* Content */}
      <div className="flex-1 min-w-0">
        {editing ? (
          <div className="flex flex-col gap-1">
            <textarea
              ref={textareaRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={handleKeyDown}
              rows={2}
              className={cn(
                'w-full resize-none rounded border border-warm-border bg-warm-canvas px-2 py-1 text-sm text-warm-text',
                'focus:outline-none focus:ring-2 focus:ring-ring',
              )}
            />
            <div className="flex gap-1">
              <Button
                variant="ghost"
                size="icon"
                onClick={handleSave}
                aria-label={t('panels.queue.save')}
                className="h-6 w-6 rounded text-success hover:bg-success/10 hover:text-success"
              >
                <Check className="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                onClick={handleCancel}
                aria-label={t('panels.queue.cancel')}
                className="h-6 w-6 rounded text-warm-text-muted"
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex items-start gap-1.5">
            <span className="whitespace-pre-wrap break-words leading-snug line-clamp-3 text-warm-text">
              {item.content}
            </span>
            {item.source === 'retry' && (
              <span className="mt-0.5 inline-flex shrink-0 items-center rounded px-1 py-0.5 text-[10px] font-medium leading-none bg-warning/15 text-warning border border-warning/30">
                {t('panels.queue.retryBadge')}
              </span>
            )}
          </div>
        )}
      </div>

      {/* Actions (visible on hover) */}
      {!editing && (
        <div className="flex shrink-0 gap-0.5 opacity-0 transition-opacity duration-150 group-hover:opacity-100 focus-within:opacity-100">
          {showSendButton && onSendToInput && (
            <Button
              variant="ghost"
              size="icon"
              onClick={() => {
                onSendToInput(item.content)
                onDelete(item.id)
              }}
              aria-label={t('panels.queue.sendToInput')}
              title={t('panels.queue.sendToInput')}
              className={cn(actionBtn, 'hover:text-info hover:bg-info/10')}
            >
              <Send className="h-3.5 w-3.5" aria-hidden="true" />
            </Button>
          )}
          {showFuseButton && onFuseToInput && (
            <Button
              variant="ghost"
              size="icon"
              onClick={() => {
                onFuseToInput(item.content)
                onDelete(item.id)
              }}
              aria-label={t('panels.queue.fuseToInput')}
              title={t('panels.queue.fuseToInput')}
              className={cn(actionBtn, 'hover:text-brand hover:bg-brand-soft')}
            >
              <Combine className="h-3.5 w-3.5" aria-hidden="true" />
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            onClick={() => {
              setDraft(item.content)
              setEditing(true)
            }}
            aria-label={t('panels.queue.edit')}
            title={t('panels.queue.edit')}
            className={cn(actionBtn, 'hover:text-brand hover:bg-brand-soft')}
          >
            <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={() => onDelete(item.id)}
            aria-label={t('panels.queue.delete')}
            title={t('panels.queue.delete')}
            className={cn(actionBtn, 'hover:text-destructive hover:bg-destructive/10')}
          >
            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// QueueList
// ---------------------------------------------------------------------------

interface QueueListProps {
  workspaceId: string
  sessionState?: 'idle' | 'running' | 'waiting_confirm'
  onSendToInput?: (content: string) => void
  onFuseToInput?: (content: string) => void
}

export function QueueList({ workspaceId, sessionState, onSendToInput, onFuseToInput }: QueueListProps) {
  const { t } = useTranslation('workspaces')
  const queryClient = useQueryClient()

  const { data: items = [] } = useQuery({
    queryKey: ['workspace', workspaceId, 'queue'],
    queryFn: () => api.get<QueueItem[]>(`/workspaces/${workspaceId}/queue`).then((res) => res ?? []),
  })

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  const deleteMutation = useMutation({
    mutationFn: (itemId: number) => api.delete(`/workspaces/${workspaceId}/queue/${itemId}`),
    onMutate: async (itemId) => {
      await queryClient.cancelQueries({ queryKey: ['workspace', workspaceId, 'queue'] });
      const previous = queryClient.getQueryData<QueueItem[]>(['workspace', workspaceId, 'queue']);
      queryClient.setQueryData<QueueItem[]>(
        ['workspace', workspaceId, 'queue'],
        (old) => old?.filter((i) => i.id !== itemId) ?? [],
      );
      return { previous };
    },
    onError: (_err, _itemId, context) => {
      if (context?.previous) {
        queryClient.setQueryData(['workspace', workspaceId, 'queue'], context.previous);
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId, 'queue'] });
    },
  })

  const editMutation = useMutation({
    mutationFn: ({ itemId, content }: { itemId: number; content: string }) =>
      api.put(`/workspaces/${workspaceId}/queue/${itemId}`, { content }),
    onMutate: async ({ itemId, content }) => {
      await queryClient.cancelQueries({ queryKey: ['workspace', workspaceId, 'queue'] });
      const previous = queryClient.getQueryData<QueueItem[]>(['workspace', workspaceId, 'queue']);
      queryClient.setQueryData<QueueItem[]>(
        ['workspace', workspaceId, 'queue'],
        (old) => old?.map((i) => (i.id === itemId ? { ...i, content } : i)) ?? [],
      );
      return { previous };
    },
    onError: (_err, _variables, context) => {
      if (context?.previous) {
        queryClient.setQueryData(['workspace', workspaceId, 'queue'], context.previous);
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId, 'queue'] });
    },
  })

  const reorderMutation = useMutation({
    mutationFn: (payload: { id: number; position: number }[]) =>
      api.put(`/workspaces/${workspaceId}/queue/reorder`, { items: payload }),
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId, 'queue'] });
    },
  })

  const handleDelete = (itemId: number) => {
    deleteMutation.mutate(itemId);
  }

  const handleEdit = (itemId: number, content: string) => {
    editMutation.mutate({ itemId, content });
  }

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event
    if (!over || active.id === over.id) return

    const oldIndex = items.findIndex((i) => i.id === active.id)
    const newIndex = items.findIndex((i) => i.id === over.id)
    if (oldIndex === -1 || newIndex === -1) return

    const reordered = arrayMove(items, oldIndex, newIndex)

    // Optimistic update
    queryClient.setQueryData<QueueItem[]>(
      ['workspace', workspaceId, 'queue'],
      reordered,
    )

    const payload = reordered.map((item, idx) => ({
      id: item.id,
      position: (idx + 1) * 1000,
    }))
    reorderMutation.mutate(payload)
  }

  // Show nothing when empty
  if (items.length === 0) return null

  return (
    <div className="px-4 pb-2 pt-2 border-t border-warm-border">
      <div className="max-w-3xl mx-auto">
        <div className="flex items-center gap-1.5 mb-1.5">
          <span className="text-xs font-medium text-warm-text-muted">
            {t('panels.queue.title')}
          </span>
          <span className="text-[10px] tabular-nums text-warm-text-muted/70">
            ({items.length})
          </span>
        </div>
        <DndContext
          sensors={sensors}
          collisionDetection={closestCenter}
          onDragEnd={handleDragEnd}
        >
          <SortableContext
            items={items.map((i) => i.id)}
            strategy={verticalListSortingStrategy}
          >
            <div className="space-y-1 max-h-32 overflow-y-auto">
              {items.map((item) => (
                <SortableItem
                  key={item.id}
                  item={item}
                  onDelete={handleDelete}
                  onEdit={handleEdit}
                  onSendToInput={onSendToInput}
                  showSendButton={sessionState !== 'running'}
                  onFuseToInput={onFuseToInput}
                  // Interrupt-and-send is offered in EVERY state, not just while a
                  // turn is live: when agent_status is idle but a background wait
                  // (a pending ScheduleWakeup) is holding the queue, this is the
                  // only way to break out and run the message now. The handler
                  // stops the agent (which clears the pending wakeup) and refills
                  // the input, so the subsequent send runs immediately.
                  showFuseButton={true}
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>
      </div>
    </div>
  )
}
