import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Badge } from '@/components/ui/badge';
import { projectBlueprintApi } from '@/lib/project-blueprint-api';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  blueprintId: number | null;
}

/** Read-only view of a template's columns + scenes. */
export function BlueprintDetailDialog({ open, onOpenChange, blueprintId }: Props) {
  const { t } = useTranslation('projects');
  const { data, isLoading } = useQuery({
    queryKey: ['project-blueprint-detail', blueprintId],
    queryFn: () => projectBlueprintApi.getDetail(blueprintId as number),
    enabled: open && !!blueprintId,
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px] max-h-[85vh]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {data?.name ?? t('tabs.settings.templates.detail.title')}
            {data?.is_builtin && (
              <Badge variant="outline" className="font-normal">{t('tabs.settings.templates.builtin')}</Badge>
            )}
          </DialogTitle>
          <DialogDescription>{data?.description || t('tabs.settings.templates.detail.noDescription')}</DialogDescription>
        </DialogHeader>

        {isLoading || !data ? (
          <p className="text-sm text-muted-foreground py-4 text-center">{t('tabs.settings.templates.loading')}</p>
        ) : (
          <div className="space-y-4 py-2">
            <div>
              <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">
                {t('tabs.settings.templates.detail.columns', { count: data.columns.length })}
              </h4>
              <div className="overflow-hidden rounded-md border border-warm-border">
                <table className="w-full text-sm">
                  <thead className="bg-warm-muted text-warm-text-muted">
                    <tr>
                      <th className="text-left font-medium px-3 py-1.5">{t('tabs.settings.templates.detail.colName')}</th>
                      <th className="text-left font-medium px-3 py-1.5">{t('tabs.settings.columns.opPrimitive')}</th>
                      <th className="text-left font-medium px-3 py-1.5">{t('tabs.settings.columns.whenToUse')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.columns.map((c, i) => (
                      <tr key={i} className="border-t border-warm-border align-top">
                        <td className="px-3 py-1.5 font-medium">{c.name}</td>
                        <td className="px-3 py-1.5">
                          <Badge variant="outline" className="font-normal">
                            {t(`tabs.settings.columns.opPrimitiveOption.${c.op_primitive}`)}
                          </Badge>
                        </td>
                        <td className="px-3 py-1.5 text-muted-foreground">{c.when_to_use || '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            <div>
              <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-2">
                {t('tabs.settings.templates.detail.scenes', { count: data.scenes.length })}
              </h4>
              {data.scenes.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  {data.scenes.map((s) => (
                    <Badge key={s.slug} variant="secondary" className="font-normal">{s.display_name || s.slug}</Badge>
                  ))}
                </div>
              ) : (
                <p className="text-xs text-muted-foreground">{t('tabs.settings.templates.detail.noScenes')}</p>
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
