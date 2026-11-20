import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  Coins,
  EyeOff,
  Library,
  Pencil,
  Plus,
  Search,
  Trash2,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { EmptyState } from '@/components/shared/empty-state'
import { useProjects } from '@/lib/hooks/use-projects'
import {
  deleteModel,
  deleteProjectBudget,
  getBudget,
  listModels,
  setBudget,
  setProjectBudget,
  updateModel,
  upsertModel,
} from '@/lib/model-api'
import type { ModelBudget, ModelItem, ModelProjectBudget } from '@/types/model'
import { ModelUsagePanel } from '@/components/models/model-usage-panel'
import { cn } from '@/lib/utils'

type Section = 'library' | 'usage' | 'budget'

const MODELS_QUERY_KEY = ['models', 'library']
const BUDGET_QUERY_KEY = ['model-budget']

function errorDetail(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * Settings → 模型: the owner-wide shared model library plus the monthly token
 * budget. The library is not bound to any provider; providers and CLIs all read
 * the same names. System rows are immutable — they can only be hidden by
 * shadowing them with a disabled owner row.
 */
export function ModelSettings() {
  const { t } = useTranslation('settings')
  const [section, setSection] = useState<Section>('library')

  const sections: { id: Section; labelKey: string }[] = [
    { id: 'library', labelKey: 'models.tabs.library' },
    { id: 'usage', labelKey: 'models.tabs.usage' },
    { id: 'budget', labelKey: 'models.tabs.budget' },
  ]

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-warm-text">{t('models.title')}</h2>
        <p className="mt-1 text-sm text-warm-text-muted">{t('models.description')}</p>
      </div>

      <div
        className="inline-flex rounded-md bg-warm-muted p-1"
        role="tablist"
        aria-label={t('models.title')}
      >
        {sections.map((item) => (
          <Button
            key={item.id}
            type="button"
            role="tab"
            size="sm"
            variant="ghost"
            aria-selected={section === item.id}
            onClick={() => setSection(item.id)}
            className={cn(
              'text-warm-text-muted hover:bg-transparent hover:text-warm-text',
              section === item.id && 'bg-warm-surface text-warm-text shadow-sm'
            )}
          >
            {t(item.labelKey)}
          </Button>
        ))}
      </div>

      {section === 'library' && <LibraryPanel />}
      {section === 'usage' && <ModelUsagePanel />}
      {section === 'budget' && <BudgetPanel />}
    </div>
  )
}

function LibraryPanel() {
  const { t } = useTranslation('settings')
  const qc = useQueryClient()
  const [search, setSearch] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [editing, setEditing] = useState<ModelItem | null>(null)
  const [pendingDelete, setPendingDelete] = useState<ModelItem | null>(null)

  const { data: models = [], isLoading, isError, refetch } = useQuery({
    queryKey: MODELS_QUERY_KEY,
    queryFn: () => listModels(true),
  })

  const invalidate = () => qc.invalidateQueries({ queryKey: MODELS_QUERY_KEY })

  const hideMutation = useMutation({
    mutationFn: (model: ModelItem) =>
      upsertModel(model.name, model.display_name, false),
    onSuccess: () => {
      invalidate()
      toast.success(t('models.library.hidden'))
    },
    onError: (err) =>
      toast.error(t('models.library.saveFailed', { detail: errorDetail(err) })),
  })

  const toggleMutation = useMutation({
    mutationFn: (model: ModelItem) =>
      updateModel(model.id, { enabled: !model.enabled }),
    onSuccess: () => invalidate(),
    onError: (err) =>
      toast.error(t('models.library.saveFailed', { detail: errorDetail(err) })),
  })

  const deleteMutation = useMutation({
    mutationFn: (model: ModelItem) => deleteModel(model.id),
    onSuccess: () => {
      invalidate()
      setPendingDelete(null)
      toast.success(t('models.library.deleted'))
    },
    onError: (err) =>
      toast.error(t('models.library.deleteFailed', { detail: errorDetail(err) })),
  })

  const filtered = useMemo(() => {
    const query = search.trim().toLowerCase()
    if (!query) return models
    return models.filter(
      (model) =>
        model.name.toLowerCase().includes(query) ||
        model.display_name.toLowerCase().includes(query)
    )
  }, [models, search])

  return (
    <Card className="border-warm-border bg-warm-surface">
      <CardHeader className="flex flex-row items-start justify-between gap-4 space-y-0 p-4">
        <div>
          <CardTitle className="text-base font-semibold text-warm-text">
            {t('models.library.title')}
          </CardTitle>
          <p className="mt-1 text-sm text-warm-text-muted">
            {t('models.library.description')}
          </p>
        </div>
        <Button size="sm" onClick={() => setAddOpen(true)}>
          <Plus className="mr-1 h-4 w-4" aria-hidden="true" />
          {t('models.library.add')}
        </Button>
      </CardHeader>
      <CardContent className="space-y-4 p-4 pt-0">
        <div className="relative max-w-sm">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-warm-text-muted"
            aria-hidden="true"
          />
          <Input
            className="pl-8"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('models.library.searchPlaceholder')}
            aria-label={t('models.library.searchPlaceholder')}
          />
        </div>

        {isLoading ? (
          <div className="space-y-2" aria-hidden="true">
            {[0, 1, 2].map((row) => (
              <div
                key={row}
                className="h-10 animate-pulse rounded-md bg-warm-muted"
              />
            ))}
          </div>
        ) : isError ? (
          <div className="flex flex-col items-start gap-3 rounded-md border border-destructive/30 bg-destructive/10 p-4">
            <p className="text-sm text-destructive-text">
              {t('models.library.loadFailed')}
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => refetch()}
            >
              {t('models.library.retry')}
            </Button>
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            title={
              search.trim()
                ? t('models.library.noResults')
                : t('models.library.empty')
            }
            description={
              search.trim()
                ? t('models.library.noResultsHint')
                : t('models.library.emptyHint')
            }
            icon={<Library className="h-12 w-12 text-warm-text-muted" />}
            size="compact"
          />
        ) : (
          <div className="overflow-hidden rounded-md border border-warm-border">
            <Table>
              <TableHeader className="bg-warm-muted">
                <TableRow className="hover:bg-transparent">
                  <TableHead className="text-xs font-medium text-warm-text-muted">
                    {t('models.library.columns.name')}
                  </TableHead>
                  <TableHead className="text-xs font-medium text-warm-text-muted">
                    {t('models.library.columns.displayName')}
                  </TableHead>
                  <TableHead className="text-xs font-medium text-warm-text-muted">
                    {t('models.library.columns.enabled')}
                  </TableHead>
                  <TableHead className="text-xs font-medium text-warm-text-muted">
                    {t('models.library.columns.source')}
                  </TableHead>
                  <TableHead className="text-right text-xs font-medium text-warm-text-muted">
                    {t('models.library.columns.actions')}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((model) => {
                  const isSystem = model.owner_id === 0
                  return (
                    <TableRow key={`${model.owner_type}-${model.owner_id}-${model.id}`}>
                      <TableCell className="font-mono text-sm text-warm-text">
                        {model.name}
                      </TableCell>
                      <TableCell className="text-sm text-warm-text">
                        {model.display_name || model.name}
                      </TableCell>
                      <TableCell>
                        {isSystem ? (
                          <span className="inline-flex items-center rounded border border-success/30 bg-success/15 px-1.5 py-0.5 text-xs text-success">
                            {t('models.library.statusEnabled')}
                          </span>
                        ) : (
                          <Switch
                            checked={model.enabled}
                            disabled={toggleMutation.isPending}
                            onCheckedChange={() => toggleMutation.mutate(model)}
                            aria-label={t('models.library.toggleEnabled', {
                              name: model.name,
                            })}
                          />
                        )}
                      </TableCell>
                      <TableCell className="text-xs text-warm-text-muted">
                        {t(`models.library.sources.${model.source}`, {
                          defaultValue: model.source,
                        })}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center justify-end gap-1">
                          {isSystem ? (
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              disabled={hideMutation.isPending}
                              aria-label={t('models.library.hide', {
                                name: model.name,
                              })}
                              onClick={() => hideMutation.mutate(model)}
                            >
                              <EyeOff className="mr-1 h-4 w-4" aria-hidden="true" />
                              {t('models.library.hide')}
                            </Button>
                          ) : (
                            <>
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon"
                                aria-label={t('models.library.edit', {
                                  name: model.name,
                                })}
                                onClick={() => setEditing(model)}
                              >
                                <Pencil className="h-4 w-4" aria-hidden="true" />
                              </Button>
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon"
                                aria-label={t('models.library.delete', {
                                  name: model.name,
                                })}
                                onClick={() => setPendingDelete(model)}
                              >
                                <Trash2
                                  className="h-4 w-4 text-destructive-text"
                                  aria-hidden="true"
                                />
                              </Button>
                            </>
                          )}
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>

      {addOpen && (
        <ModelFormDialog
          mode="create"
          onClose={() => setAddOpen(false)}
          onSaved={invalidate}
        />
      )}
      {editing && (
        <ModelFormDialog
          mode="edit"
          model={editing}
          onClose={() => setEditing(null)}
          onSaved={invalidate}
        />
      )}

      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('models.library.deleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('models.library.deleteDescription', {
                name: pendingDelete?.name ?? '',
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('models.library.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                if (pendingDelete) deleteMutation.mutate(pendingDelete)
              }}
            >
              {t('models.library.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

interface ModelFormDialogProps {
  mode: 'create' | 'edit'
  model?: ModelItem
  onClose: () => void
  onSaved: () => void
}

function ModelFormDialog({ mode, model, onClose, onSaved }: ModelFormDialogProps) {
  const { t } = useTranslation('settings')
  const [name, setName] = useState(model?.name ?? '')
  const [displayName, setDisplayName] = useState(model?.display_name ?? '')

  const saveMutation = useMutation({
    mutationFn: () => {
      const effectiveDisplay = displayName.trim() || name.trim()
      return mode === 'create' || !model
        ? upsertModel(name.trim(), effectiveDisplay, true)
        : updateModel(model.id, { display_name: effectiveDisplay })
    },
    onSuccess: () => {
      onSaved()
      onClose()
      toast.success(
        mode === 'create'
          ? t('models.library.added')
          : t('models.library.updated')
      )
    },
    onError: (err) =>
      toast.error(t('models.library.saveFailed', { detail: errorDetail(err) })),
  })

  const canSave = name.trim() !== '' && !saveMutation.isPending

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {mode === 'create'
              ? t('models.library.addTitle')
              : t('models.library.editTitle')}
          </DialogTitle>
          <DialogDescription>
            {t('models.library.formDescription')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="model-name">{t('models.library.fields.name')}</Label>
            <Input
              id="model-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              disabled={mode === 'edit'}
              placeholder={t('models.library.fields.namePlaceholder')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="model-display-name">
              {t('models.library.fields.displayName')}
            </Label>
            <Input
              id="model-display-name"
              value={displayName}
              onChange={(event) => setDisplayName(event.target.value)}
              placeholder={t('models.library.fields.displayNamePlaceholder')}
            />
          </div>
        </div>
        <DialogFooter>
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('models.library.cancel')}
          </Button>
          <Button
            type="button"
            disabled={!canSave}
            onClick={() => saveMutation.mutate()}
          >
            {t('models.library.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function BudgetPanel() {
  const { t, i18n } = useTranslation('settings')
  const qc = useQueryClient()
  const [projectDialog, setProjectDialog] = useState<{
    open: boolean
    edit?: ModelProjectBudget
  }>({ open: false })
  const [pendingDelete, setPendingDelete] =
    useState<ModelProjectBudget | null>(null)

  const numberFormat = useMemo(
    () => new Intl.NumberFormat(i18n.language),
    [i18n.language]
  )

  const {
    data: budget,
    isLoading,
    isError,
    refetch,
  } = useQuery({ queryKey: BUDGET_QUERY_KEY, queryFn: () => getBudget() })
  const { projects = [] } = useProjects()

  // Soft caps can point at a project that is no longer active; keep those
  // selectable (and readable) via an id fallback.
  const projectOptions = useMemo(() => {
    const names = new Map(projects.map((project) => [project.id, project.name]))
    for (const project of budget?.projects ?? []) {
      if (!names.has(project.project_id)) {
        names.set(project.project_id, `#${project.project_id}`)
      }
    }
    return [...names].map(([id, name]) => ({ id, name }))
  }, [projects, budget])

  const invalidate = () => qc.invalidateQueries({ queryKey: BUDGET_QUERY_KEY })

  const deleteProjectMutation = useMutation({
    mutationFn: (projectId: number) => deleteProjectBudget(projectId),
    onSuccess: () => {
      invalidate()
      setPendingDelete(null)
      toast.success(t('models.budget.projectDeleted'))
    },
    onError: (err) =>
      toast.error(
        t('models.budget.saveFailed', { detail: errorDetail(err) })
      ),
  })

  const projectName = (projectId: number) =>
    projects.find((project) => project.id === projectId)?.name ?? `#${projectId}`

  if (isLoading) {
    return (
      <div className="space-y-2" aria-hidden="true">
        <div className="h-32 animate-pulse rounded-lg bg-warm-muted" />
        <div className="h-40 animate-pulse rounded-lg bg-warm-muted" />
      </div>
    )
  }

  if (isError || !budget) {
    return (
      <div className="flex flex-col items-start gap-3 rounded-md border border-destructive/30 bg-destructive/10 p-4">
        <p className="text-sm text-destructive-text">
          {t('models.budget.loadFailed')}
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => refetch()}
        >
          {t('models.library.retry')}
        </Button>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <Card className="border-warm-border bg-warm-surface">
        <CardHeader className="p-4">
          <CardTitle className="flex items-center gap-2 text-base font-semibold text-warm-text">
            <Coins className="h-4 w-4 text-warm-text-muted" aria-hidden="true" />
            {t('models.budget.ownerTitle')}
          </CardTitle>
          <p className="text-sm text-warm-text-muted">
            {t('models.budget.ownerDescription')}
          </p>
        </CardHeader>
        <CardContent className="p-4 pt-0">
          <OwnerBudgetForm
            key={budget.monthly_tokens}
            budget={budget}
            numberFormat={numberFormat}
            onSaved={invalidate}
          />
        </CardContent>
      </Card>

      <Card className="border-warm-border bg-warm-surface">
        <CardHeader className="flex flex-row items-start justify-between gap-4 space-y-0 p-4">
          <div>
            <CardTitle className="text-base font-semibold text-warm-text">
              {t('models.budget.projectTitle')}
            </CardTitle>
            <p className="mt-1 text-sm text-warm-text-muted">
              {t('models.budget.projectDescription')}
            </p>
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setProjectDialog({ open: true })}
          >
            <Plus className="mr-1 h-4 w-4" aria-hidden="true" />
            {t('models.budget.addProject')}
          </Button>
        </CardHeader>
        <CardContent className="p-4 pt-0">
          {budget.projects.length === 0 ? (
            <EmptyState
              title={t('models.budget.noProjects')}
              description={t('models.budget.noProjectsHint')}
              icon={<Coins className="h-12 w-12 text-warm-text-muted" />}
              size="compact"
            />
          ) : (
            <div className="overflow-hidden rounded-md border border-warm-border">
              <Table>
                <TableHeader className="bg-warm-muted">
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="text-xs font-medium text-warm-text-muted">
                      {t('models.budget.columns.project')}
                    </TableHead>
                    <TableHead className="text-xs font-medium text-warm-text-muted">
                      {t('models.budget.columns.limit')}
                    </TableHead>
                    <TableHead className="text-xs font-medium text-warm-text-muted">
                      {t('models.budget.columns.used')}
                    </TableHead>
                    <TableHead className="text-xs font-medium text-warm-text-muted">
                      {t('models.budget.columns.status')}
                    </TableHead>
                    <TableHead className="text-right text-xs font-medium text-warm-text-muted">
                      {t('models.budget.columns.actions')}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {budget.projects.map((project) => (
                    <TableRow key={project.project_id}>
                      <TableCell className="text-sm text-warm-text">
                        {projectName(project.project_id)}
                      </TableCell>
                      <TableCell className="text-sm text-warm-text">
                        {project.monthly_tokens > 0
                          ? numberFormat.format(project.monthly_tokens)
                          : t('models.budget.unlimited')}
                      </TableCell>
                      <TableCell className="text-sm text-warm-text">
                        {numberFormat.format(project.used_tokens)}
                      </TableCell>
                      <TableCell>
                        <ProjectBudgetStatus project={project} />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            aria-label={t('models.budget.editProject', {
                              name: projectName(project.project_id),
                            })}
                            onClick={() =>
                              setProjectDialog({ open: true, edit: project })
                            }
                          >
                            <Pencil className="h-4 w-4" aria-hidden="true" />
                          </Button>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            aria-label={t('models.budget.deleteProject', {
                              name: projectName(project.project_id),
                            })}
                            onClick={() => setPendingDelete(project)}
                          >
                            <Trash2
                              className="h-4 w-4 text-destructive-text"
                              aria-hidden="true"
                            />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {projectDialog.open && (
        <ProjectBudgetDialog
          edit={projectDialog.edit}
          existing={budget.projects}
          projects={projectOptions}
          onClose={() => setProjectDialog({ open: false })}
          onSaved={invalidate}
        />
      )}

      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('models.budget.deleteProjectTitle')}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t('models.budget.deleteProjectDescription', {
                name: pendingDelete
                  ? projectName(pendingDelete.project_id)
                  : '',
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('models.library.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                if (pendingDelete)
                  deleteProjectMutation.mutate(pendingDelete.project_id)
              }}
            >
              {t('models.library.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

interface OwnerBudgetFormProps {
  budget: ModelBudget
  numberFormat: Intl.NumberFormat
  onSaved: () => void
}

function OwnerBudgetForm({ budget, numberFormat, onSaved }: OwnerBudgetFormProps) {
  const { t } = useTranslation('settings')
  const [value, setValue] = useState(String(budget.monthly_tokens))

  const saveMutation = useMutation({
    mutationFn: () => setBudget(Math.max(0, Math.floor(Number(value) || 0))),
    onSuccess: () => {
      onSaved()
      toast.success(t('models.budget.saved'))
    },
    onError: (err) =>
      toast.error(t('models.budget.saveFailed', { detail: errorDetail(err) })),
  })

  const parsed = Math.max(0, Math.floor(Number(value) || 0))
  const percent =
    budget.monthly_tokens > 0
      ? Math.round((budget.used_tokens / budget.monthly_tokens) * 100)
      : 0
  const overLimit = budget.monthly_tokens > 0 && budget.used_tokens >= budget.monthly_tokens
  const nearLimit = !overLimit && percent >= 80
  const barClass = overLimit
    ? 'bg-destructive'
    : nearLimit
      ? 'bg-warning'
      : 'bg-brand'

  return (
    <div className="space-y-4">
      <p className="text-sm text-warm-text">
        {budget.monthly_tokens > 0
          ? t('models.budget.usedOfLimit', {
              used: numberFormat.format(budget.used_tokens),
              limit: numberFormat.format(budget.monthly_tokens),
              percent,
            })
          : t('models.budget.usedUnlimited', {
              used: numberFormat.format(budget.used_tokens),
            })}
      </p>

      <div
        className="h-2 w-full overflow-hidden rounded-full bg-warm-muted"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.min(100, percent)}
        aria-label={t('models.budget.progressLabel')}
      >
        <div
          className={cn('h-full rounded-full', barClass)}
          style={{
            width: `${budget.monthly_tokens > 0 ? Math.min(100, percent) : 0}%`,
          }}
        />
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full max-w-xs space-y-1.5">
          <Label htmlFor="owner-monthly-tokens">
            {t('models.budget.ownerLimit')}
          </Label>
          <Input
            id="owner-monthly-tokens"
            type="number"
            min={0}
            step={1}
            value={value}
            onChange={(event) => setValue(event.target.value)}
          />
          <p className="text-xs text-warm-text-muted">
            {t('models.budget.ownerLimitHint')}
          </p>
        </div>
        <Button
          type="button"
          disabled={saveMutation.isPending || parsed === budget.monthly_tokens}
          onClick={() => saveMutation.mutate()}
        >
          {t('models.budget.save')}
        </Button>
      </div>
    </div>
  )
}

function ProjectBudgetStatus({ project }: { project: ModelProjectBudget }) {
  const { t } = useTranslation('settings')

  if (project.monthly_tokens <= 0) {
    return (
      <span className="inline-flex items-center rounded border border-warm-border bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
        {t('models.budget.status.unlimited')}
      </span>
    )
  }
  if (project.used_tokens >= project.monthly_tokens) {
    return (
      <span className="inline-flex items-center rounded border border-destructive/30 bg-destructive/10 px-1.5 py-0.5 text-xs text-destructive-text">
        {t('models.budget.status.exceeded')}
      </span>
    )
  }
  const percent = Math.round(
    (project.used_tokens / project.monthly_tokens) * 100
  )
  if (percent >= 80) {
    return (
      <span className="inline-flex items-center rounded border border-warning/30 bg-warning/15 px-1.5 py-0.5 text-xs text-warning">
        {t('models.budget.status.near', { percent })}
      </span>
    )
  }
  return (
    <span className="inline-flex items-center rounded border border-success/30 bg-success/15 px-1.5 py-0.5 text-xs text-success">
      {t('models.budget.status.ok', { percent })}
    </span>
  )
}

interface ProjectBudgetDialogProps {
  edit?: ModelProjectBudget
  existing: ModelProjectBudget[]
  projects: { id: number; name: string }[]
  onClose: () => void
  onSaved: () => void
}

function ProjectBudgetDialog({
  edit,
  existing,
  projects,
  onClose,
  onSaved,
}: ProjectBudgetDialogProps) {
  const { t } = useTranslation('settings')
  const configured = new Set(
    existing.filter((item) => item.project_id !== edit?.project_id).map((item) => item.project_id)
  )
  const selectable = projects.filter((project) => !configured.has(project.id))

  const [projectId, setProjectId] = useState(
    edit ? String(edit.project_id) : ''
  )
  const [limit, setLimit] = useState(edit ? String(edit.monthly_tokens) : '')

  const saveMutation = useMutation({
    mutationFn: () =>
      setProjectBudget(Number(projectId), Math.max(0, Math.floor(Number(limit) || 0))),
    onSuccess: () => {
      onSaved()
      onClose()
      toast.success(t('models.budget.projectSaved'))
    },
    onError: (err) =>
      toast.error(t('models.budget.saveFailed', { detail: errorDetail(err) })),
  })

  const canSave =
    projectId !== '' && limit.trim() !== '' && !saveMutation.isPending

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {edit
              ? t('models.budget.editProjectTitle')
              : t('models.budget.addProjectTitle')}
          </DialogTitle>
          <DialogDescription>
            {t('models.budget.projectFormDescription')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label>{t('models.budget.fields.project')}</Label>
            <Select
              value={projectId}
              onValueChange={setProjectId}
              disabled={Boolean(edit)}
            >
              <SelectTrigger aria-label={t('models.budget.fields.project')}>
                <SelectValue
                  placeholder={t('models.budget.fields.projectPlaceholder')}
                />
              </SelectTrigger>
              <SelectContent>
                {selectable.length === 0 ? (
                  <SelectItem value="__none__" disabled>
                    {t('models.budget.fields.noProjects')}
                  </SelectItem>
                ) : (
                  selectable.map((project) => (
                    <SelectItem key={project.id} value={String(project.id)}>
                      {project.name}
                    </SelectItem>
                  ))
                )}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="project-monthly-tokens">
              {t('models.budget.fields.limit')}
            </Label>
            <Input
              id="project-monthly-tokens"
              type="number"
              min={0}
              step={1}
              value={limit}
              onChange={(event) => setLimit(event.target.value)}
            />
            <p className="text-xs text-warm-text-muted">
              {t('models.budget.fields.limitHint')}
            </p>
          </div>
        </div>
        <DialogFooter>
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('models.library.cancel')}
          </Button>
          <Button
            type="button"
            disabled={!canSave}
            onClick={() => saveMutation.mutate()}
          >
            {t('models.library.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
