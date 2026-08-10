import { Archive } from 'lucide-react';
import { useTranslation } from 'react-i18next';

export function ArchivedPlaceholder() {
  const { t } = useTranslation('workspaces');
  return (
    <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-2">
      <Archive className="w-8 h-8" />
      <span className="text-sm">{t('panels.archivedNotice')}</span>
    </div>
  );
}
