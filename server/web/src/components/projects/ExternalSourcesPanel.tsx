import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2, Github } from 'lucide-react';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { useExternalSources, useDeleteSource } from '@/hooks/useExternalSources';
import type { OwnerRef } from '@/types/org';
import { AddSourceDialog } from './AddSourceDialog';

interface Props {
  projectId: number;
  /** Owner of this project, forwarded to the add-source credential picker so
   *  org-shared credentials surface for org-owned projects. */
  projectOwner?: OwnerRef;
}

// Settings sub-section: list / add / delete external-source bindings on a
// project. The browse drawer (`ExternalIssuesSheet`) consumes the same source
// list to populate its source picker.
export function ExternalSourcesPanel({ projectId, projectOwner }: Props) {
  const { t } = useTranslation('projects');
  const { data: sources = [], isLoading } = useExternalSources(projectId);
  const deleteSource = useDeleteSource(projectId);
  const [addOpen, setAddOpen] = useState(false);

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="flex-1 min-w-0">
          <h3 className="text-sm font-semibold text-warm-text">
            {t('externalSources.title')}
          </h3>
          <p className="text-xs text-warm-text-muted mt-1">
            {t('externalSources.description')}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setAddOpen(true)}
        >
          <Plus className="h-4 w-4 mr-1" aria-hidden="true" />
          {t('externalSources.addSource')}
        </Button>
      </div>

      {isLoading ? (
        <p className="text-xs text-warm-text-muted text-center py-2">
          {t('externalIssues.loading')}
        </p>
      ) : sources.length === 0 ? (
        <p className="text-xs text-warm-text-muted text-center py-4">
          {t('externalSources.noSources')}
        </p>
      ) : (
        <div className="border border-warm-border rounded-md divide-y divide-warm-border">
          {sources.map((s) => (
            <div key={s.id} className="flex items-center gap-3 p-3">
              <Github
                className="h-4 w-4 text-warm-text-muted flex-shrink-0"
                aria-hidden="true"
              />
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium text-warm-text truncate">
                  {s.source_key}
                </p>
                <p className="text-xs text-warm-text-muted">{s.provider}</p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={async () => {
                  if (
                    await confirm(
                      t('externalSources.deleteConfirm', { key: s.source_key }),
                    )
                  ) {
                    deleteSource.mutate(s.id);
                  }
                }}
                disabled={deleteSource.isPending}
                className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                aria-label={t('externalSources.delete')}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      <AddSourceDialog
        projectId={projectId}
        projectOwner={projectOwner}
        open={addOpen}
        onOpenChange={setAddOpen}
      />
    </div>
  );
}
