import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Library, Plus, Trash2, ExternalLink } from 'lucide-react'
import { toast } from 'sonner'
import { confirm } from '@/lib/confirm'
import { Button } from '@/components/ui/button'
import {
  listProjectKnowledgeBases,
  addProjectKnowledgeBase,
  removeProjectKnowledgeBase,
  listKnowledgeBases,
} from '@/lib/kb-api'

interface Props {
  projectId: number
}

// Settings sub-section: bind owner-level knowledge bases to a project so the
// project's workspace agents can search + direct-read them. Mirrors
// ProjectDataSourcesPanel — KBs themselves are created in Settings →
// Integrations; here we only bind/unbind existing ones.
export function ProjectKnowledgeBasesPanel({ projectId }: Props) {
  const { t } = useTranslation('knowledge')
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [selectId, setSelectId] = useState('')

  const { data: bound = [], isLoading } = useQuery({
    queryKey: ['project-knowledge-bases', projectId],
    queryFn: () => listProjectKnowledgeBases(projectId),
  })
  const { data: all = [] } = useQuery({
    queryKey: ['knowledge-bases'],
    queryFn: listKnowledgeBases,
  })

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['project-knowledge-bases', projectId] })
    qc.invalidateQueries({ queryKey: ['knowledge-bases'] })
  }

  const add = useMutation({
    mutationFn: (kbId: number) => addProjectKnowledgeBase(projectId, kbId),
    onSuccess: () => {
      invalidate()
      setSelectId('')
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })
  const remove = useMutation({
    mutationFn: (kbId: number) => removeProjectKnowledgeBase(projectId, kbId),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const boundIds = new Set(bound.map((s) => s.id))
  const available = all.filter((s) => !boundIds.has(s.id))

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="flex-1 min-w-0">
        <h3 className="text-sm font-semibold text-warm-text">
          {t('project.title')}
        </h3>
        <p className="text-xs text-warm-text-muted mt-1">
          {t('project.description')}
        </p>
      </div>

      {isLoading ? (
        <p className="text-xs text-warm-text-muted text-center py-2">
          {t('project.loading')}
        </p>
      ) : bound.length === 0 ? (
        <p className="text-xs text-warm-text-muted text-center py-4">
          {t('project.empty')}
        </p>
      ) : (
        <div className="border border-warm-border rounded-md divide-y divide-warm-border">
          {bound.map((s) => (
            <div key={s.id} className="flex items-center gap-3 p-3">
              <Library
                className="h-4 w-4 text-warm-text-muted flex-shrink-0"
                aria-hidden="true"
              />
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium text-warm-text truncate">
                  {s.name}
                </p>
                <p className="text-xs text-warm-text-muted">
                  {t(`sourceKind.${s.source_kind}`)}
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={async () => {
                  if (
                    await confirm(t('project.removeConfirm', { name: s.name }))
                  ) {
                    remove.mutate(s.id)
                  }
                }}
                disabled={remove.isPending}
                className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                aria-label={t('project.remove')}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      {all.length === 0 ? (
        <div className="rounded-md border border-warm-border bg-warm-muted/50 p-3 text-sm text-warm-text">
          <p>{t('project.configureFirst')}</p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="mt-2"
            onClick={() => navigate({ to: '/settings', search: { tab: 'integrations' } })}
          >
            <ExternalLink className="h-3.5 w-3.5 mr-1" aria-hidden="true" />
            {t('project.goToIntegrations')}
          </Button>
        </div>
      ) : (
        <div className="flex items-center gap-2">
          <select
            value={selectId}
            onChange={(e) => setSelectId(e.target.value)}
            disabled={available.length === 0 || add.isPending}
            className="h-9 flex-1 rounded-md border border-input bg-background px-3 py-1 text-sm"
          >
            <option value="">
              {available.length === 0
                ? t('project.allBound')
                : t('project.pick')}
            </option>
            {available.map((s) => (
              <option key={s.id} value={String(s.id)}>
                {s.name}
              </option>
            ))}
          </select>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!selectId || add.isPending}
            onClick={() => selectId && add.mutate(Number(selectId))}
          >
            <Plus className="h-4 w-4 mr-1" aria-hidden="true" />
            {t('project.add')}
          </Button>
        </div>
      )}
    </div>
  )
}
