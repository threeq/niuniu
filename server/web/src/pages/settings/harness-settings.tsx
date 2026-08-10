import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, PlayCircle, Pencil } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { confirm } from '@/lib/confirm'
import { harnessSpecApi } from '@/lib/harness-api'
import type {
  HarnessSpec,
  HarnessCategory,
  HarnessSeverity,
  HarnessKind,
  HarnessTarget,
  HarnessThresholdOp,
  HarnessTrigger,
  HarnessSpecTestResult,
} from '@/types/harness'
import {
  CATEGORY_LABEL_KEYS,
  SEVERITY_LABEL_KEYS,
  HARNESS_KINDS,
  HARNESS_THRESHOLD_OPS,
  HARNESS_TRIGGERS,
  JUDGE_MODELS,
} from '@/types/harness'

const CATEGORY_OPTIONS: HarnessCategory[] = ['commit', 'quality', 'workflow', 'agent']
const SEVERITY_OPTIONS: HarnessSeverity[] = ['error', 'warning', 'info']
const REGEX_TARGET_OPTIONS: HarnessTarget[] = ['commit_message', 'branch_name', 'agent_output']
// ai_judge reuses the same three string targets that regex_match supports —
// the judge prompt sees the raw text from whichever target is selected.
const AI_JUDGE_TARGET_OPTIONS: HarnessTarget[] = ['commit_message', 'branch_name', 'agent_output']

const DEFAULT_JUDGE_MODEL = JUDGE_MODELS[0].value

const severityBadge: Record<HarnessSeverity, string> = {
  error: 'bg-destructive/10 text-destructive dark:bg-red-950/30 dark:text-red-300',
  warning: 'bg-warning/10 text-warning dark:bg-amber-950/30 dark:text-amber-300',
  info: 'bg-info/10 text-info dark:bg-blue-950/30 dark:text-blue-300',
}

// Category is a low-importance disambiguator next to severity/kind, so all
// categories share the same neutral muted token. If we ever want per-category
// color, follow docs/design-system.md §11 to add --cat-* tokens with light +
// dark parity rather than reaching for raw Tailwind palette colors.
const categoryBadge: Record<HarnessCategory, string> = {
  commit: 'bg-muted text-muted-foreground',
  quality: 'bg-muted text-muted-foreground',
  workflow: 'bg-muted text-muted-foreground',
  agent: 'bg-muted text-muted-foreground',
}

const testStatusBadge: Record<HarnessSpecTestResult['status'], string> = {
  pass: 'bg-success/10 text-success dark:bg-emerald-950/30 dark:text-emerald-300',
  fail: 'bg-destructive/10 text-destructive dark:bg-red-950/30 dark:text-red-300',
  skip: 'bg-muted text-muted-foreground',
  error: 'bg-warning/10 text-warning dark:bg-amber-950/30 dark:text-amber-300',
}

// FormState mirrors the editable subset of HarnessSpec — driven directly by
// the dialog's controlled inputs. Kept loose (strings for numeric inputs) so
// users can type freely; converted to numbers in toPayload().
interface FormState {
  name: string
  category: HarnessCategory
  severity: HarnessSeverity
  kind: HarnessKind
  target: HarnessTarget
  pattern: string
  patternFlags: string
  command: string
  timeoutSec: string
  expectedExitCode: string
  extractRegex: string
  thresholdValue: string
  thresholdOp: Exclude<HarnessThresholdOp, ''>
  filePathsRaw: string // newline-separated UI input; serialized to JSON array
  triggerOn: HarnessTrigger
  judgePrompt: string
  judgeModel: string
}

function emptyForm(): FormState {
  return {
    name: '',
    category: 'quality',
    severity: 'warning',
    kind: 'regex_match',
    target: 'commit_message',
    pattern: '',
    patternFlags: '',
    command: '',
    timeoutSec: '120',
    expectedExitCode: '0',
    extractRegex: '',
    thresholdValue: '0',
    thresholdOp: '>=',
    filePathsRaw: '',
    triggerOn: 'phase_exit',
    judgePrompt: '',
    judgeModel: DEFAULT_JUDGE_MODEL,
  }
}

function specToForm(spec: HarnessSpec): FormState {
  // file_paths is a JSON array string. Parse defensively — legacy rows or
  // hand-edited DB entries may not be valid JSON; fall back to one-per-line
  // raw text so the user sees something editable.
  let filePathsRaw = ''
  try {
    const parsed = JSON.parse(spec.file_paths || '[]')
    if (Array.isArray(parsed)) {
      filePathsRaw = parsed.filter((p) => typeof p === 'string').join('\n')
    }
  } catch {
    filePathsRaw = spec.file_paths || ''
  }
  return {
    name: spec.name,
    category: spec.category,
    severity: spec.severity,
    kind: (spec.kind || 'regex_match') as HarnessKind,
    target: (spec.target || 'commit_message') as HarnessTarget,
    pattern: spec.pattern || '',
    patternFlags: spec.pattern_flags || '',
    command: spec.command || '',
    timeoutSec: String(spec.timeout_sec || 120),
    expectedExitCode: String(spec.expected_exit_code ?? 0),
    extractRegex: spec.extract_regex || '',
    thresholdValue: String(spec.threshold_value ?? 0),
    thresholdOp: (spec.threshold_op || '>=') as Exclude<HarnessThresholdOp, ''>,
    filePathsRaw,
    triggerOn: (spec.trigger_on || 'phase_exit') as HarnessTrigger,
    judgePrompt: spec.judge_prompt || '',
    judgeModel: spec.judge_model || DEFAULT_JUDGE_MODEL,
  }
}

// Build the wire payload from FormState. Always sends every typed field — the
// backend ignores irrelevant ones based on `kind`, and explicit empty values
// let the user clear a previously-set field on edit.
function toPayload(form: FormState, extra: Record<string, unknown>): Record<string, unknown> {
  const filePathsArr = form.filePathsRaw
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
  return {
    ...extra,
    name: form.name,
    category: form.category,
    severity: form.severity,
    kind: form.kind,
    target: form.target,
    pattern: form.pattern,
    pattern_flags: form.patternFlags,
    command: form.command,
    timeout_sec: Number(form.timeoutSec) || 0,
    expected_exit_code: Number(form.expectedExitCode) || 0,
    extract_regex: form.extractRegex,
    threshold_value: Number(form.thresholdValue) || 0,
    threshold_op: form.thresholdOp,
    file_paths: JSON.stringify(filePathsArr),
    trigger_on: form.triggerOn,
    judge_prompt: form.judgePrompt,
    judge_model: form.judgeModel,
  }
}

// Shared className for native <select>. Matches the existing harness-settings
// styling — no shadcn Select component is installed in this repo, so we stay
// on the native element styled with the same tokens as <Input>.
const selectClass =
  'w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent'

export function HarnessSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()

  const { data: specs = [], isLoading: specsLoading } = useQuery({
    queryKey: ['harness-specs', 'global'],
    queryFn: () => harnessSpecApi.listGlobal(),
  })

  const [specDialogOpen, setSpecDialogOpen] = useState(false)
  // editingSpec === null  → create mode
  // editingSpec === spec  → edit mode (enables Test button)
  const [editingSpec, setEditingSpec] = useState<HarnessSpec | null>(null)
  const [form, setForm] = useState<FormState>(emptyForm())
  const [testResult, setTestResult] = useState<HarnessSpecTestResult | null>(null)
  const [testError, setTestError] = useState<string | null>(null)
  const [formDirty, setFormDirty] = useState(false)

  const createSpecMutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => harnessSpecApi.create(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['harness-specs', 'global'] })
      setSpecDialogOpen(false)
    },
  })

  const updateSpecMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: Record<string, unknown> }) =>
      harnessSpecApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['harness-specs', 'global'] })
      setSpecDialogOpen(false)
    },
  })

  const deleteSpecMutation = useMutation({
    mutationFn: (id: number) => harnessSpecApi.delete(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['harness-specs', 'global'] }),
  })

  const testSpecMutation = useMutation({
    mutationFn: (id: number) => harnessSpecApi.test(id),
    onSuccess: (res) => {
      setTestResult(res)
      setTestError(null)
    },
    onError: (err: Error) => {
      setTestResult(null)
      setTestError(err.message || t('harness.specs.dialog.testFailedGeneric'))
    },
  })

  const updateForm = (patch: Partial<FormState>) => {
    setForm((prev) => ({ ...prev, ...patch }))
    setFormDirty(true)
  }

  const openCreateSpec = () => {
    setEditingSpec(null)
    setForm(emptyForm())
    setTestResult(null)
    setTestError(null)
    setFormDirty(false)
    setSpecDialogOpen(true)
  }

  const openEditSpec = (spec: HarnessSpec) => {
    setEditingSpec(spec)
    setForm(specToForm(spec))
    setTestResult(null)
    setTestError(null)
    setFormDirty(false)
    setSpecDialogOpen(true)
  }

  const handleSaveSpec = () => {
    if (editingSpec) {
      updateSpecMutation.mutate({
        id: editingSpec.id,
        data: toPayload(form, {
          enabled: editingSpec.enabled ? 1 : 0,
          config: editingSpec.config || '{}',
        }),
      })
    } else {
      createSpecMutation.mutate(
        toPayload(form, {
          enabled: 1,
          config: '{}',
        }),
      )
    }
  }

  const handleTestSpec = () => {
    if (!editingSpec || formDirty) return
    testSpecMutation.mutate(editingSpec.id)
  }

  const toggleSpec = (spec: HarnessSpec) => {
    const newEnabled = spec.enabled ? 0 : 1
    // Preserve every typed field — UpdateSpec is a full replace, not a patch.
    updateSpecMutation.mutate({
      id: spec.id,
      data: {
        enabled: newEnabled,
        severity: spec.severity,
        config: spec.config,
        kind: spec.kind,
        target: spec.target,
        pattern: spec.pattern,
        pattern_flags: spec.pattern_flags,
        command: spec.command,
        timeout_sec: spec.timeout_sec,
        expected_exit_code: spec.expected_exit_code,
        extract_regex: spec.extract_regex,
        threshold_value: spec.threshold_value,
        threshold_op: spec.threshold_op,
        file_paths: spec.file_paths,
        trigger_on: spec.trigger_on,
        judge_prompt: spec.judge_prompt,
        judge_model: spec.judge_model,
      },
    })
  }

  const saving = createSpecMutation.isPending || updateSpecMutation.isPending
  const canTest = !!editingSpec && !formDirty
  const testButtonLabel = testSpecMutation.isPending
    ? t('harness.specs.dialog.testing')
    : t('harness.specs.dialog.test')

  return (
    <div className="space-y-8">
      {/* Global Specs */}
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-base font-semibold text-foreground">{t('harness.specs.title')}</h2>
            <p className="text-sm text-muted-foreground mt-0.5">{t('harness.specs.subtitle')}</p>
          </div>
          <Button size="sm" onClick={openCreateSpec}>
            <Plus className="h-3.5 w-3.5 mr-1" />
            {t('harness.specs.newSpec')}
          </Button>
        </div>

        {specsLoading ? (
          <p className="text-sm text-muted-foreground">{t('common:actions.loading')}</p>
        ) : specs.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('harness.specs.noSpecs')}</p>
        ) : (
          <div className="space-y-2">
            {specs.map((spec) => (
              <div key={spec.id} className="border border-border rounded-lg p-3 flex items-center gap-3">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-sm font-medium text-foreground">{spec.name}</span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded font-medium ${categoryBadge[spec.category]}`}>
                      {t(CATEGORY_LABEL_KEYS[spec.category])}
                    </span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded font-medium ${severityBadge[spec.severity]}`}>
                      {t(SEVERITY_LABEL_KEYS[spec.severity])}
                    </span>
                    {spec.kind ? (
                      <span className="text-[10px] px-1.5 py-0.5 rounded font-medium bg-muted text-muted-foreground">
                        {t(`harness.specs.kind.${spec.kind}`)}
                      </span>
                    ) : null}
                  </div>
                </div>
                <div className="flex items-center gap-2 flex-shrink-0">
                  {/* Toggle switch */}
                  <button
                    onClick={() => toggleSpec(spec)}
                    className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors ${
                      spec.enabled ? 'bg-info' : 'bg-muted'
                    }`}
                    aria-label={t('harness.specs.toggleAria')}
                  >
                    <span
                      className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${
                        spec.enabled ? 'translate-x-5' : 'translate-x-0.5'
                      }`}
                    />
                  </button>
                  <button
                    onClick={() => openEditSpec(spec)}
                    className="p-1 text-muted-foreground hover:text-foreground"
                    aria-label={t('harness.specs.editAria')}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </button>
                  <button
                    onClick={async () => {
                      if (!(await confirm(t('harness.specs.deleteConfirm', { name: spec.name })))) return
                      deleteSpecMutation.mutate(spec.id)
                    }}
                    className="p-1 text-muted-foreground hover:text-destructive"
                    aria-label={t('harness.specs.deleteAria')}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Create / edit spec dialog */}
      <Dialog open={specDialogOpen} onOpenChange={setSpecDialogOpen}>
        <DialogContent className="max-w-xl max-h-[85vh]">
          <DialogHeader>
            <DialogTitle>
              {editingSpec ? t('harness.specs.dialog.editTitle') : t('harness.specs.dialog.title')}
            </DialogTitle>
            <DialogDescription>{t('harness.specs.dialog.description')}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-1">
              <Label htmlFor="spec-name">{t('harness.specs.dialog.nameLabel')}</Label>
              <Input
                id="spec-name"
                value={form.name}
                onChange={(e) => updateForm({ name: e.target.value })}
                placeholder={t('harness.specs.dialog.namePlaceholder')}
              />
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <Label htmlFor="spec-category">{t('harness.specs.dialog.categoryLabel')}</Label>
                <select
                  id="spec-category"
                  value={form.category}
                  onChange={(e) => updateForm({ category: e.target.value as HarnessCategory })}
                  className={selectClass}
                >
                  {CATEGORY_OPTIONS.map((c) => (
                    <option key={c} value={c}>{t(CATEGORY_LABEL_KEYS[c])}</option>
                  ))}
                </select>
              </div>
              <div className="space-y-1">
                <Label htmlFor="spec-severity">{t('harness.specs.dialog.severityLabel')}</Label>
                <select
                  id="spec-severity"
                  value={form.severity}
                  onChange={(e) => updateForm({ severity: e.target.value as HarnessSeverity })}
                  className={selectClass}
                >
                  {SEVERITY_OPTIONS.map((s) => (
                    <option key={s} value={s}>{t(SEVERITY_LABEL_KEYS[s])}</option>
                  ))}
                </select>
              </div>
            </div>

            <div className="space-y-1">
              <Label htmlFor="spec-kind">{t('harness.specs.dialog.kindLabel')}</Label>
              <select
                id="spec-kind"
                value={form.kind}
                onChange={(e) => updateForm({ kind: e.target.value as HarnessKind })}
                className={selectClass}
              >
                {HARNESS_KINDS.map((k) => (
                  <option key={k} value={k}>{t(`harness.specs.kind.${k}`)}</option>
                ))}
              </select>
              <p className="text-xs text-muted-foreground">{t(`harness.specs.kindHint.${form.kind}`)}</p>
              {form.kind === 'ai_judge' && (
                <p className="text-xs text-info">{t('harness.specs.dialog.aiJudgeApiKeyHint')}</p>
              )}
            </div>

            {/* Trigger picker — applies to every kind. on_schedule is wired up
                in Phase C; we surface it now so authors can author scheduled
                specs ahead of the runtime binding landing. */}
            <div className="space-y-1">
              <Label htmlFor="spec-trigger">{t('harness.specs.dialog.triggerLabel')}</Label>
              <select
                id="spec-trigger"
                value={form.triggerOn}
                onChange={(e) => updateForm({ triggerOn: e.target.value as HarnessTrigger })}
                className={selectClass}
              >
                {HARNESS_TRIGGERS.map((tr) => (
                  <option key={tr} value={tr}>{t(`harness.specs.trigger.${tr}`)}</option>
                ))}
              </select>
              {form.triggerOn === 'on_schedule' && (
                <p className="text-xs text-muted-foreground">{t('harness.specs.dialog.triggerOnScheduleHint')}</p>
              )}
            </div>

            {/* Kind-specific fields. Conditionals are intentionally flat (no
                nested switch) so the layout stays scannable in a diff. */}
            {form.kind === 'regex_match' && (
              <>
                <div className="space-y-1">
                  <Label htmlFor="spec-target">{t('harness.specs.dialog.targetLabel')}</Label>
                  <select
                    id="spec-target"
                    value={form.target}
                    onChange={(e) => updateForm({ target: e.target.value as HarnessTarget })}
                    className={selectClass}
                  >
                    {REGEX_TARGET_OPTIONS.map((tgt) => (
                      <option key={tgt} value={tgt}>{t(`harness.specs.target.${tgt}`)}</option>
                    ))}
                  </select>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-pattern">{t('harness.specs.dialog.patternLabel')}</Label>
                  <Input
                    id="spec-pattern"
                    value={form.pattern}
                    onChange={(e) => updateForm({ pattern: e.target.value })}
                    placeholder={t('harness.specs.dialog.patternPlaceholder')}
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-pattern-flags">{t('harness.specs.dialog.patternFlagsLabel')}</Label>
                  <Input
                    id="spec-pattern-flags"
                    value={form.patternFlags}
                    onChange={(e) => updateForm({ patternFlags: e.target.value })}
                    placeholder={t('harness.specs.dialog.patternFlagsPlaceholder')}
                  />
                </div>
              </>
            )}

            {form.kind === 'command_exit_code' && (
              <>
                <div className="space-y-1">
                  <Label htmlFor="spec-command">{t('harness.specs.dialog.commandLabel')}</Label>
                  <Input
                    id="spec-command"
                    value={form.command}
                    onChange={(e) => updateForm({ command: e.target.value })}
                    placeholder={t('harness.specs.dialog.commandPlaceholder')}
                  />
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1">
                    <Label htmlFor="spec-timeout">{t('harness.specs.dialog.timeoutLabel')}</Label>
                    <Input
                      id="spec-timeout"
                      type="number"
                      min={1}
                      value={form.timeoutSec}
                      onChange={(e) => updateForm({ timeoutSec: e.target.value })}
                    />
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="spec-exit-code">{t('harness.specs.dialog.expectedExitCodeLabel')}</Label>
                    <Input
                      id="spec-exit-code"
                      type="number"
                      value={form.expectedExitCode}
                      onChange={(e) => updateForm({ expectedExitCode: e.target.value })}
                    />
                  </div>
                </div>
              </>
            )}

            {form.kind === 'command_output_match' && (
              <>
                <div className="space-y-1">
                  <Label htmlFor="spec-command">{t('harness.specs.dialog.commandLabel')}</Label>
                  <Input
                    id="spec-command"
                    value={form.command}
                    onChange={(e) => updateForm({ command: e.target.value })}
                    placeholder={t('harness.specs.dialog.commandPlaceholder')}
                  />
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1">
                    <Label htmlFor="spec-timeout">{t('harness.specs.dialog.timeoutLabel')}</Label>
                    <Input
                      id="spec-timeout"
                      type="number"
                      min={1}
                      value={form.timeoutSec}
                      onChange={(e) => updateForm({ timeoutSec: e.target.value })}
                    />
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="spec-threshold-op">{t('harness.specs.dialog.thresholdOpLabel')}</Label>
                    <select
                      id="spec-threshold-op"
                      value={form.thresholdOp}
                      onChange={(e) =>
                        updateForm({ thresholdOp: e.target.value as Exclude<HarnessThresholdOp, ''> })
                      }
                      className={selectClass}
                    >
                      {HARNESS_THRESHOLD_OPS.map((op) => (
                        <option key={op} value={op}>{op}</option>
                      ))}
                    </select>
                  </div>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-extract-regex">{t('harness.specs.dialog.extractRegexLabel')}</Label>
                  <Input
                    id="spec-extract-regex"
                    value={form.extractRegex}
                    onChange={(e) => updateForm({ extractRegex: e.target.value })}
                    placeholder={t('harness.specs.dialog.extractRegexPlaceholder')}
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-threshold-value">{t('harness.specs.dialog.thresholdValueLabel')}</Label>
                  <Input
                    id="spec-threshold-value"
                    type="number"
                    step="any"
                    value={form.thresholdValue}
                    onChange={(e) => updateForm({ thresholdValue: e.target.value })}
                  />
                </div>
              </>
            )}

            {form.kind === 'file_exists' && (
              <div className="space-y-1">
                <Label htmlFor="spec-file-paths">{t('harness.specs.dialog.filePathsLabel')}</Label>
                <Textarea
                  id="spec-file-paths"
                  value={form.filePathsRaw}
                  onChange={(e) => updateForm({ filePathsRaw: e.target.value })}
                  placeholder={t('harness.specs.dialog.filePathsPlaceholder')}
                  rows={4}
                />
                <p className="text-xs text-muted-foreground">{t('harness.specs.dialog.filePathsHint')}</p>
              </div>
            )}

            {form.kind === 'ai_judge' && (
              <>
                <div className="space-y-1">
                  <Label htmlFor="spec-target">{t('harness.specs.dialog.targetLabel')}</Label>
                  <select
                    id="spec-target"
                    value={form.target}
                    onChange={(e) => updateForm({ target: e.target.value as HarnessTarget })}
                    className={selectClass}
                  >
                    {AI_JUDGE_TARGET_OPTIONS.map((tgt) => (
                      <option key={tgt} value={tgt}>{t(`harness.specs.target.${tgt}`)}</option>
                    ))}
                  </select>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-judge-model">{t('harness.specs.dialog.judgeModelLabel')}</Label>
                  <select
                    id="spec-judge-model"
                    value={form.judgeModel}
                    onChange={(e) => updateForm({ judgeModel: e.target.value })}
                    className={selectClass}
                  >
                    {JUDGE_MODELS.map((m) => (
                      <option key={m.value} value={m.value}>
                        {t(`harness.specs.judgeModelLabels.${m.labelKey}`)}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-judge-prompt">{t('harness.specs.dialog.judgePromptLabel')}</Label>
                  <Textarea
                    id="spec-judge-prompt"
                    value={form.judgePrompt}
                    onChange={(e) => updateForm({ judgePrompt: e.target.value })}
                    placeholder={t('harness.specs.dialog.judgePromptPlaceholder')}
                    rows={6}
                  />
                  <p className="text-xs text-muted-foreground">{t('harness.specs.dialog.judgePromptHint')}</p>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="spec-timeout">{t('harness.specs.dialog.timeoutLabel')}</Label>
                  <Input
                    id="spec-timeout"
                    type="number"
                    min={1}
                    value={form.timeoutSec}
                    onChange={(e) => updateForm({ timeoutSec: e.target.value })}
                  />
                </div>
              </>
            )}

            {/* Test panel — only meaningful for an already-persisted spec.
                Disabled while edits are pending so the result reflects the
                stored row, not the in-memory form. */}
            {editingSpec && (
              <div className="space-y-2 border-t border-border pt-4">
                <div className="flex items-center justify-between">
                  <Label>{t('harness.specs.dialog.testSectionLabel')}</Label>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={handleTestSpec}
                    disabled={!canTest || testSpecMutation.isPending}
                  >
                    <PlayCircle className="h-3.5 w-3.5 mr-1" />
                    {testButtonLabel}
                  </Button>
                </div>
                {formDirty && (
                  <p className="text-xs text-muted-foreground">{t('harness.specs.dialog.testSaveFirst')}</p>
                )}
                {testResult && (
                  <div className="rounded-md border border-border p-3 space-y-1.5">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className={`text-[10px] px-1.5 py-0.5 rounded font-medium ${testStatusBadge[testResult.status]}`}>
                        {t(`harness.specs.dialog.testStatus.${testResult.status}`)}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {t('harness.specs.dialog.testDuration', { ms: testResult.duration_ms })}
                      </span>
                      {/* cost_usd is omitempty on the wire — only present for
                          ai_judge runs that actually invoked the API. Render to
                          6 decimal places so sub-cent costs (the common case for
                          haiku) are visible. */}
                      {typeof testResult.cost_usd === 'number' && testResult.cost_usd > 0 && (
                        <span className="text-xs text-muted-foreground">
                          {t('harness.specs.dialog.testCost', { cost: testResult.cost_usd.toFixed(6) })}
                        </span>
                      )}
                    </div>
                    {testResult.message && (
                      <p className="text-sm text-foreground">{testResult.message}</p>
                    )}
                    {testResult.details && (
                      <pre className="text-xs text-muted-foreground whitespace-pre-wrap break-words">
                        {testResult.details}
                      </pre>
                    )}
                  </div>
                )}
                {testError && (
                  <p className="text-xs text-destructive">{testError}</p>
                )}
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSpecDialogOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button
              onClick={handleSaveSpec}
              disabled={saving || !form.name.trim()}
            >
              {saving ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
