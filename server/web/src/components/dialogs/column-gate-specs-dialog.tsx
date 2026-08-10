import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ChevronUp, ChevronDown, X, Plus } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { columnGateSpecApi } from '@/lib/project-template-api'
import type { GateApplicability } from '@/lib/project-template-api'
import { harnessSpecApi } from '@/lib/harness-api'
import type { HarnessSpec } from '@/types/harness'
import { cn } from '@/lib/utils'

interface Props {
  open: boolean
  columnId: number
  columnName: string
  projectId: number
  onOpenChange: (b: boolean) => void
}

export function ColumnGateSpecsDialog({
  open,
  columnId,
  columnName,
  projectId,
  onOpenChange,
}: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [search, setSearch] = useState('')
  const [order, setOrder] = useState<number[]>([])
  // Per-spec applicability: if_routed (column-level gate) or always (project floor).
  const [appl, setAppl] = useState<Record<number, GateApplicability>>({})

  const { data: specsResp } = useQuery({
    queryKey: ['column-gate-specs', columnId],
    queryFn: () => columnGateSpecApi.list(columnId),
    enabled: open,
  })

  const { data: allSpecs = [] } = useQuery({
    queryKey: ['harness-specs', 'global'],
    queryFn: () => harnessSpecApi.listGlobal(),
    enabled: open,
  })

  // Compare-during-render: reset order when server response changes.
  // Avoids setState-in-effect and the extra render from useEffect.
  const [prevSpecsResp, setPrevSpecsResp] = useState(specsResp)
  if (specsResp !== prevSpecsResp) {
    setPrevSpecsResp(specsResp)
    const specs = specsResp?.specs ?? []
    const sorted = specs.slice().sort((a, b) => a.position - b.position)
    setOrder(sorted.map((s) => s.spec_id))
    setAppl(Object.fromEntries(sorted.map((s) => [s.spec_id, s.applicability])))
  }

  const replaceMut = useMutation({
    mutationFn: () =>
      columnGateSpecApi.replace(
        columnId,
        order.map((id) => ({ spec_id: id, applicability: appl[id] ?? 'if_routed' })),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['column-gate-specs', columnId] })
      // Project settings table renders gate-spec badges per column row off the
      // ['project-columns', projectId] query — refresh it so the picker save
      // reflects immediately without a page reload.
      qc.invalidateQueries({ queryKey: ['project-columns', projectId] })
      toast.success(t('tabs.settings.specs.specPicker.save'))
      onOpenChange(false)
    },
    onError: (e: unknown) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const specMap = new Map(allSpecs.map((s) => [s.id, s]))
  const linked = order.map((id) => specMap.get(id)).filter(Boolean) as HarnessSpec[]
  const addable = allSpecs
    .filter((s) => !order.includes(s.id))
    .filter((s) => !search || s.name.toLowerCase().includes(search.toLowerCase()))

  const move = (idx: number, dir: -1 | 1) => {
    const next = [...order]
    const target = idx + dir
    if (target < 0 || target >= next.length) return
    ;[next[idx], next[target]] = [next[target], next[idx]]
    setOrder(next)
  }
  const remove = (id: number) => setOrder(order.filter((x) => x !== id))
  const add = (id: number) => {
    setOrder([...order, id])
    setAppl((prev) => ({ ...prev, [id]: prev[id] ?? 'if_routed' }))
  }
  const setApplicability = (id: number, value: GateApplicability) =>
    setAppl((prev) => ({ ...prev, [id]: value }))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[720px]">
        <DialogHeader>
          <DialogTitle>
            {t('tabs.settings.specs.specPicker.title', { column: columnName })}
          </DialogTitle>
        </DialogHeader>

        <div className="grid grid-cols-2 gap-4 py-4">
          {/* Left: selected specs with ordering controls */}
          <div>
            <h4 className="mb-2 text-sm font-medium text-warm-text">
              {t('tabs.settings.specs.title')}
            </h4>
            <div className="space-y-2">
              {linked.length === 0 ? (
                <p className="text-xs text-warm-text-muted">
                  {t('tabs.settings.specs.noSpecs')}
                </p>
              ) : (
                linked.map((s, i) => (
                  <div
                    key={s.id}
                    className="flex items-center gap-2 rounded-md border border-warm-border bg-warm-surface p-2"
                  >
                    <span className="flex-1 text-sm text-warm-text">
                      {s.name}
                      <Badge variant="outline" className="ml-2 text-[10px]">
                        {s.severity}
                      </Badge>
                    </span>
                    {/* Applicability toggle: if_routed (column gate) vs always (floor). */}
                    <div className="flex overflow-hidden rounded border border-warm-border text-[10px]">
                      {(['if_routed', 'always'] as const).map((mode) => (
                        <button
                          key={mode}
                          type="button"
                          onClick={() => setApplicability(s.id, mode)}
                          className={cn(
                            'px-1.5 py-0.5 transition-colors',
                            (appl[s.id] ?? 'if_routed') === mode
                              ? 'bg-brand text-white'
                              : 'bg-warm-surface text-warm-text-muted hover:bg-warm-muted',
                          )}
                        >
                          {t(`tabs.settings.specs.applicability.${mode === 'if_routed' ? 'ifRouted' : 'always'}`)}
                        </button>
                      ))}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => move(i, -1)}
                      disabled={i === 0}
                    >
                      <ChevronUp className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => move(i, 1)}
                      disabled={i === linked.length - 1}
                    >
                      <ChevronDown className="h-3.5 w-3.5" />
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => remove(s.id)}>
                      <X className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                ))
              )}
            </div>
          </div>

          {/* Right: spec picker with search */}
          <div>
            <h4 className="mb-2 text-sm font-medium text-warm-text">
              {t('tabs.settings.specs.addSpec')}
            </h4>
            <Input
              placeholder={t('tabs.settings.specs.specPicker.search')}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="mb-2"
            />
            <div className="max-h-[320px] space-y-1 overflow-y-auto">
              {addable.length === 0 ? (
                <p className="text-xs text-warm-text-muted">
                  {t('tabs.settings.specs.specPicker.noMatch')}
                </p>
              ) : (
                addable.map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    onClick={() => add(s.id)}
                    aria-label={`add ${s.name}`}
                    className="flex w-full items-center gap-2 rounded-md border border-warm-border bg-warm-surface px-2 py-1.5 text-sm text-warm-text hover:bg-warm-muted"
                  >
                    <Plus className="h-3.5 w-3.5" />
                    <span className="flex-1 text-left">{s.name}</span>
                    <Badge variant="outline" className="text-[10px]">
                      {s.category}
                    </Badge>
                  </button>
                ))
              )}
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('tabs.settings.specs.specPicker.cancel')}
          </Button>
          <Button onClick={() => replaceMut.mutate()} disabled={replaceMut.isPending}>
            {t('tabs.settings.specs.specPicker.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
