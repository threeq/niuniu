import { useTranslation } from 'react-i18next'
import { History, RotateCcw } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { useMemoryVersions, useRollbackMemory } from '@/lib/hooks/use-memory'
import type { Memory } from '@/types/api'

interface MemoryVersionDialogProps {
  memory: Memory | null
  projectId: number
  onClose: () => void
}

function formatTime(dateStr: string): string {
  return new Date(dateStr).toLocaleString()
}

export function MemoryVersionDialog({ memory, projectId, onClose }: MemoryVersionDialogProps) {
  const { t } = useTranslation('projects')
  const { data: versions = [], isLoading } = useMemoryVersions(memory?.id ?? null)
  const rollback = useRollbackMemory(projectId)

  const handleRollback = (version: number) => {
    if (!memory) return
    rollback.mutate({ id: memory.id, version }, { onSuccess: onClose })
  }

  return (
    <Dialog open={!!memory} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <History className="w-4 h-4" />
            {t('tabs.memory.versionTitle')}
          </DialogTitle>
          <DialogDescription>{memory?.title}</DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <div className="text-sm text-muted-foreground py-4">{t('tabs.memory.loading')}</div>
        ) : (
          <ol className="space-y-2 max-h-[60vh] overflow-y-auto pr-1">
            {versions.map((v) => {
              const isCurrent = memory != null && v.version === memory.version
              return (
                <li key={v.id} className="flex gap-2">
                  <div className="flex flex-col items-center pt-1">
                    <span
                      className={`inline-flex items-center justify-center w-6 h-6 rounded-full text-[10px] font-medium ${
                        isCurrent ? 'bg-info/15 text-info' : 'bg-muted text-muted-foreground'
                      }`}
                    >
                      v{v.version}
                    </span>
                    <div className="w-px flex-1 bg-border mt-1" />
                  </div>
                  <div className="flex-1 min-w-0 border rounded-lg bg-card px-3 py-2">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <p className="text-xs font-medium text-foreground truncate">{v.title}</p>
                        {v.content && (
                          <p className="text-xs text-muted-foreground mt-0.5 line-clamp-3">{v.content}</p>
                        )}
                      </div>
                      {isCurrent ? (
                        <span className="text-[10px] text-info shrink-0">{t('tabs.memory.current')}</span>
                      ) : (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-6 px-2 text-[10px] shrink-0"
                          disabled={rollback.isPending}
                          onClick={() => handleRollback(v.version)}
                        >
                          <RotateCcw className="w-3 h-3 mr-1" />
                          {t('tabs.memory.rollback')}
                        </Button>
                      )}
                    </div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">{formatTime(v.created_at)}</div>
                  </div>
                </li>
              )
            })}
          </ol>
        )}
      </DialogContent>
    </Dialog>
  )
}
