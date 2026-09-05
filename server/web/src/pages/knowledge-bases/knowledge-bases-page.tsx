import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Library, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/shared/empty-state'
import { useKnowledgeBases } from '@/lib/hooks/use-knowledge-bases'
import { KnowledgeBaseDialog } from '@/components/knowledge/knowledge-base-dialog'

/**
 * Index route for /knowledge-bases. Like RepositoryListPage this is mostly a
 * redirector: with any KB present it forwards to the first one so the detail pane
 * is never blank, and it only renders for a genuinely empty account — where it
 * becomes the onboarding surface.
 */
export function KnowledgeBasesPage() {
  const { t } = useTranslation('knowledge')
  const { knowledgeBases, isLoading } = useKnowledgeBases()
  const navigate = useNavigate()
  const [createOpen, setCreateOpen] = useState(false)

  useEffect(() => {
    if (isLoading || !knowledgeBases || knowledgeBases.length === 0) return
    navigate({
      to: '/knowledge-bases/$id',
      params: { id: String(knowledgeBases[0].id) },
      replace: true,
    })
  }, [isLoading, knowledgeBases, navigate])

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground text-sm">
        {t('common:actions.loading')}
      </div>
    )
  }

  // Non-empty: the effect above redirects, so render nothing rather than
  // flashing the empty state on the way out.
  if (knowledgeBases && knowledgeBases.length > 0) {
    return <div className="flex h-full" />
  }

  return (
    <div className="flex h-full items-center justify-center">
      <EmptyState
        icon={<Library className="h-12 w-12 text-muted-foreground" />}
        title={t('empty.title')}
        description={t('panel.empty')}
        suggestions={[
          t('empty.hintLocal'),
          t('empty.hintUpload'),
          t('empty.hintUrl'),
        ]}
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="size-4 mr-1" aria-hidden />
            {t('panel.add')}
          </Button>
        }
      />
      <KnowledgeBaseDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        editing={null}
      />
    </div>
  )
}
