import { useTranslation } from 'react-i18next'
import { useQuery, useMutation } from '@tanstack/react-query'
import { Pencil, Trash2, Plus } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { useInvalidateKnowledgeBases } from '@/lib/hooks/use-knowledge-bases'
import { updateKnowledgeBase, type KBBinding, type KnowledgeBase } from '@/lib/kb-api'
import type { Project } from '@/types/api'

interface Props {
  kb: KnowledgeBase
  onEdit: () => void
}

/**
 * Per-KB settings: identity (delegated to the edit dialog) and project
 * visibility, editable in place.
 *
 * Bindings used to be reachable only by opening the edit dialog, which made the
 * most consequential setting on a KB — whether any agent can see it at all — feel
 * incidental. Here each add/remove is its own PATCH, so the change lands
 * immediately rather than being staged behind a Save.
 */
export function KBSettingsTab({ kb, onEdit }: Props) {
  const { t } = useTranslation('knowledge')
  const invalidate = useInvalidateKnowledgeBases()

  const projects = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active' } }),
  })

  const setBindings = useMutation({
    mutationFn: (next: KBBinding[]) => updateKnowledgeBase(kb.id, { bindings: next }),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const labelFor = (b: KBBinding): string => {
    const hit = (projects.data ?? []).find((p) => p.id === b.target_id)
    return hit ? hit.name : `#${b.target_id}`
  }

  const available = (projects.data ?? []).filter(
    (p) => !kb.bindings.some((b) => b.target_id === p.id),
  )

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="flex flex-col gap-4 max-w-3xl">
        <section className="rounded-lg border border-warm-border bg-warm-surface p-3 flex flex-col gap-3">
          <div className="flex items-start justify-between gap-2">
            <div>
              <h2 className="text-sm font-medium">{t('settings.identity')}</h2>
              <p className="text-xs text-muted-foreground mt-1">
                {t('settings.identityHint')}
              </p>
            </div>
            <Button size="sm" variant="outline" onClick={onEdit}>
              <Pencil className="size-4 mr-1" aria-hidden />
              {t('row.edit')}
            </Button>
          </div>
          <dl className="flex flex-col gap-1.5 text-sm">
            <div className="flex gap-2">
              <dt className="text-muted-foreground w-24 shrink-0">
                {t('dialog.nameLabel')}
              </dt>
              <dd className="min-w-0 truncate">{kb.name}</dd>
            </div>
            <div className="flex gap-2">
              <dt className="text-muted-foreground w-24 shrink-0">
                {t('dialog.descriptionLabel')}
              </dt>
              <dd className="min-w-0">
                {kb.description || (
                  <span className="text-muted-foreground">{t('settings.noDescription')}</span>
                )}
              </dd>
            </div>
          </dl>
        </section>

        <section className="rounded-lg border border-warm-border bg-warm-surface p-3 flex flex-col gap-3">
          <div>
            <h2 className="text-sm font-medium">{t('bindings.title')}</h2>
            <p className="text-xs text-muted-foreground mt-1">{t('bindings.hint')}</p>
          </div>

          {kb.bindings.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t('bindings.empty')}</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {kb.bindings.map((b) => (
                <li
                  key={`${b.target_type}:${b.target_id}`}
                  className="flex items-center gap-2 text-sm"
                >
                  <span className="shrink-0 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
                    {t('bindings.typeProject')}
                  </span>
                  <span className="flex-1 min-w-0 truncate">{labelFor(b)}</span>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t('bindings.remove')}
                    title={t('bindings.remove')}
                    disabled={setBindings.isPending}
                    onClick={() =>
                      setBindings.mutate(
                        kb.bindings.filter((x) => x.target_id !== b.target_id),
                      )
                    }
                  >
                    <Trash2 className="size-4" aria-hidden />
                  </Button>
                </li>
              ))}
            </ul>
          )}

          {available.length > 0 && (
            <div className="flex items-center gap-2">
              <label htmlFor="kb-bind-add" className="sr-only">
                {t('bindings.add')}
              </label>
              <select
                id="kb-bind-add"
                value=""
                disabled={setBindings.isPending}
                onChange={(e) => {
                  const id = Number(e.target.value)
                  if (!id) return
                  setBindings.mutate([
                    ...kb.bindings,
                    { target_type: 'project', target_id: id },
                  ])
                }}
                className="h-9 flex-1 max-w-xs rounded-md border border-input bg-background px-3 py-1 text-sm"
              >
                <option value="">{t('bindings.selectTarget')}</option>
                {available.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
              <Plus className="size-4 text-muted-foreground" aria-hidden />
            </div>
          )}
          {available.length === 0 && kb.bindings.length > 0 && (
            <p className="text-xs text-muted-foreground">{t('project.allBound')}</p>
          )}
        </section>
      </div>
    </div>
  )
}
