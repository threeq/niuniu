import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Sparkles } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { apiFetch, ApiError } from '@/lib/api'
import { columnExtensionApi, type ColumnExtension } from '@/lib/project-template-api'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  column: ColumnExtension
  projectId: number
}

const PRIMITIVES = ['none', 'instruct', 'complete'] as const

type SuggestColumnOpData = { op_instruction: string; when_to_use: string }
type SuggestColumnOpEnvelope = { data: SuggestColumnOpData }

// AI-native column editor (spec §4 / §12): a column is an operation primitive +
// op_instruction + when_to_use routing hint. Engineering-standard (gate) bindings
// are configured in the separate ColumnGateSpecsDialog. The legacy executor /
// reviewer agent + auto_advance phase-run fields were retired from this surface.
export function ColumnExtensionDialog({ open, onOpenChange, column, projectId }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [primitive, setPrimitive] = useState(column.op_primitive || 'none')
  const [instruction, setInstruction] = useState(column.phase_prompt ?? '')
  const [whenToUse, setWhenToUse] = useState(column.when_to_use ?? '')
  const [suggesting, setSuggesting] = useState(false)

  // Reset the editor when a different column is selected (the parent keeps this
  // dialog mounted and swaps the `column` prop). Compare-during-render avoids the
  // set-state-in-effect anti-pattern; same approach as ColumnGateSpecsDialog.
  const [seenColumnId, setSeenColumnId] = useState(column.id)
  if (seenColumnId !== column.id) {
    setSeenColumnId(column.id)
    setPrimitive(column.op_primitive || 'none')
    setInstruction(column.phase_prompt ?? '')
    setWhenToUse(column.when_to_use ?? '')
  }

  const mut = useMutation({
    mutationFn: () =>
      columnExtensionApi.update(column.id, {
        op_primitive: primitive,
        op_instruction: instruction.trim() || null,
        when_to_use: whenToUse.trim() || null,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-columns', projectId] })
      toast.success(t('tabs.settings.columns.saved'))
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : String(e)
      toast.error(t('tabs.settings.columns.saveFailed', { message: msg }))
    },
  })

  const handleSuggest = async () => {
    setSuggesting(true)
    try {
      const resp = await apiFetch<SuggestColumnOpEnvelope>('/ai/suggest-column-op', {
        method: 'POST',
        body: JSON.stringify({
          column_name: column.name,
          gate_spec_names: column.gate_specs.map((s) => s.name),
          current_op_instruction: instruction.trim(),
          current_when_to_use: whenToUse.trim(),
        }),
        suppressError: true,
      })
      const data = resp?.data
      if (!data) {
        toast.error(t('tabs.settings.columns.aiSuggestFailed'))
        return
      }
      // Apply whichever fields Claude returned; partial results are accepted
      // (some instruction is better than none).
      if (data.op_instruction) setInstruction(data.op_instruction)
      if (data.when_to_use) setWhenToUse(data.when_to_use)
      if (!data.op_instruction && !data.when_to_use) {
        toast.error(t('tabs.settings.columns.aiSuggestFailed'))
      }
    } catch (e: unknown) {
      if (e instanceof ApiError && e.status === 503) {
        toast.error(t('tabs.settings.columns.aiSuggestCliUnavailable'))
      } else {
        toast.error(t('tabs.settings.columns.aiSuggestFailed'))
      }
    } finally {
      setSuggesting(false)
    }
  }

  const isInstruct = primitive === 'instruct'

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>
            {t('tabs.settings.columns.edit')} — {column.name}
          </DialogTitle>
          <DialogDescription>{t('tabs.settings.columns.description')}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            mut.mutate()
          }}
          className="grid gap-4 py-4"
        >
          <div className="grid gap-2">
            <Label htmlFor="op-primitive">{t('tabs.settings.columns.opPrimitive')}</Label>
            <select
              id="op-primitive"
              value={primitive}
              onChange={(e) => setPrimitive(e.target.value)}
              className="w-full rounded-md border border-warm-border bg-warm-surface px-3 py-2 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
            >
              {PRIMITIVES.map((p) => (
                <option key={p} value={p}>
                  {t(`tabs.settings.columns.opPrimitiveOption.${p}`)}
                </option>
              ))}
            </select>
            <p className="text-xs text-warm-text-muted">
              {t('tabs.settings.columns.opPrimitiveHint')}
            </p>
          </div>

          {isInstruct && (
            <div className="grid gap-2">
              <Label htmlFor="op-instruction">{t('tabs.settings.columns.opInstruction')}</Label>
              <Textarea
                id="op-instruction"
                rows={5}
                maxLength={8192}
                placeholder={t('tabs.settings.columns.opInstructionPlaceholder')}
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
              />
              <p className="text-xs text-warm-text-muted">
                {t('tabs.settings.columns.opInstructionHint')}
              </p>
            </div>
          )}

          <div className="grid gap-2">
            <Label htmlFor="when-to-use">{t('tabs.settings.columns.whenToUse')}</Label>
            <Textarea
              id="when-to-use"
              rows={3}
              maxLength={1024}
              placeholder={t('tabs.settings.columns.whenToUsePlaceholder')}
              value={whenToUse}
              onChange={(e) => setWhenToUse(e.target.value)}
            />
            <p className="text-xs text-warm-text-muted">
              {t('tabs.settings.columns.whenToUseHint')}
            </p>
          </div>

          {/* AI suggest button — generates op_instruction + when_to_use from the column name */}
          <div className="flex items-center">
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="gap-1.5 h-7 px-2 text-xs"
              disabled={suggesting || mut.isPending}
              onClick={handleSuggest}
            >
              <Sparkles className="w-3.5 h-3.5" />
              {suggesting
                ? t('tabs.settings.columns.aiSuggesting')
                : t('tabs.settings.columns.aiSuggest')}
            </Button>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={mut.isPending}>
              {mut.isPending ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
