import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Settings2, ShieldCheck, Trash2 } from 'lucide-react'
import { api } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ColumnExtensionDialog } from '@/components/dialogs/column-extension-dialog'
import { ColumnGateSpecsDialog } from '@/components/dialogs/column-gate-specs-dialog'
import { CreateColumnDialog } from '@/components/dialogs/create-column-dialog'
import { DeleteColumnDialog } from '@/components/dialogs/delete-column-dialog'
import type { ColumnExtension } from '@/lib/project-template-api'

interface Props {
  projectId: number
}

export function ColumnsEditorSection({ projectId }: Props) {
  const { t } = useTranslation('projects')
  const [editing, setEditing] = useState<ColumnExtension | null>(null)
  const [editingGates, setEditingGates] = useState<ColumnExtension | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<ColumnExtension | null>(null)

  const { data: columns = [], isLoading } = useQuery({
    queryKey: ['project-columns', projectId],
    queryFn: () => api.get<ColumnExtension[]>(`/projects/${projectId}/columns`),
  })

  if (isLoading) {
    return (
      <div className="text-sm text-warm-text-muted">
        {t('common:actions.loading')}
      </div>
    )
  }

  return (
    <section className="space-y-4">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h3 className="text-base font-semibold text-warm-text">
            {t('tabs.settings.columns.title')}
          </h3>
          <p className="mt-1 text-xs text-warm-text-muted">
            {t('tabs.settings.columns.description')}
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setCreating(true)}
          className="flex-shrink-0"
        >
          <Plus className="w-3.5 h-3.5 mr-1" aria-hidden="true" />
          {t('tabs.settings.columns.addColumn')}
        </Button>
      </header>
      <div className="overflow-hidden rounded-md border border-warm-border bg-warm-surface">
        <table className="w-full text-sm">
          <thead className="bg-warm-muted text-warm-text-muted">
            <tr>
              <th className="px-3 py-2 text-left">
                {t('tabs.settings.columns.header.name')}
              </th>
              <th className="px-3 py-2 text-left">
                {t('tabs.settings.columns.header.primitive')}
              </th>
              <th className="px-3 py-2 text-left">
                {t('tabs.settings.columns.header.gate')}
              </th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {columns.map((c) => (
              <tr key={c.id} className="border-t border-warm-border">
                <td className="px-3 py-2 font-medium text-warm-text">{c.name}</td>
                <td className="px-3 py-2">
                  <Badge variant="outline" className="font-normal">
                    {t(`tabs.settings.columns.opPrimitiveOption.${c.op_primitive || 'none'}`)}
                  </Badge>
                </td>
                <td className="px-3 py-2">
                  <div className="flex items-center gap-2 flex-wrap">
                    {c.gate_specs && c.gate_specs.length > 0 ? (
                      <div className="flex items-center gap-1 flex-wrap">
                        {c.gate_specs.map((g) => (
                          <Badge
                            key={g.id}
                            variant="outline"
                            className="font-normal"
                            title={g.severity}
                          >
                            {g.name}
                            <span className="ml-1 text-[10px] text-warm-text-muted">
                              {g.severity}
                            </span>
                          </Badge>
                        ))}
                      </div>
                    ) : (
                      <span className="text-warm-text-muted">—</span>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setEditingGates(c)}
                      aria-label={`${c.name} gate specs`}
                    >
                      <ShieldCheck className="h-4 w-4 mr-1" />
                      {t('tabs.settings.columns.configure')}
                    </Button>
                  </div>
                </td>
                <td className="px-3 py-2 text-right">
                  <div className="flex items-center justify-end gap-0.5">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setEditing(c)}
                      aria-label={t('tabs.settings.columns.edit')}
                      title={t('tabs.settings.columns.edit')}
                    >
                      <Settings2 className="h-4 w-4" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setDeleting(c)}
                      aria-label={`${t('tabs.settings.columns.delete')} ${c.name}`}
                      title={t('tabs.settings.columns.delete')}
                      className="hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && (
        <ColumnExtensionDialog
          open={!!editing}
          column={editing}
          projectId={projectId}
          onOpenChange={(o) => {
            if (!o) setEditing(null)
          }}
        />
      )}
      {editingGates && (
        <ColumnGateSpecsDialog
          open={!!editingGates}
          columnId={editingGates.id}
          columnName={editingGates.name}
          projectId={projectId}
          onOpenChange={(o) => {
            if (!o) setEditingGates(null)
          }}
        />
      )}
      <CreateColumnDialog
        open={creating}
        projectId={projectId}
        onOpenChange={setCreating}
      />
      <DeleteColumnDialog
        open={!!deleting}
        column={deleting}
        projectId={projectId}
        onOpenChange={(o) => {
          if (!o) setDeleting(null)
        }}
      />
    </section>
  )
}
