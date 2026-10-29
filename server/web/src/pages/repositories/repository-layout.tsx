import { useState } from 'react';
import { Outlet } from '@tanstack/react-router';
import { Group, Panel, Separator } from 'react-resizable-panels';
import { useTranslation } from 'react-i18next';
import { PanelLeftOpen } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet';
import { useIsMobile } from '@/hooks/use-media-query';
import { RepositorySidebar } from './repository-sidebar';

export function RepositoryLayout() {
  const { t } = useTranslation('repositories');
  const isMobile = useIsMobile();
  const [listOpen, setListOpen] = useState(false);

  // Desktop keeps the resizable master/detail split.
  if (!isMobile) {
    return (
      <div className="h-full">
        <Group orientation="horizontal" className="h-full">
          <Panel defaultSize={280}>
            <RepositorySidebar />
          </Panel>
          <Separator className="w-1 bg-border hover:bg-info/40 transition-colors cursor-col-resize" />
          <Panel>
            <Outlet />
          </Panel>
        </Group>
      </div>
    );
  }

  // Phones cannot host a 280px rail beside the detail pane: the list becomes a
  // drawer opened from the header.
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b bg-card px-2">
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          onClick={() => setListOpen(true)}
          aria-label={t('list.title')}
          title={t('list.title')}
        >
          <PanelLeftOpen className="h-4 w-4" aria-hidden="true" />
        </Button>
        <span className="truncate text-sm font-medium text-foreground">{t('list.title')}</span>
      </div>
      <div className="min-h-0 flex-1">
        <Outlet />
      </div>

      <Sheet open={listOpen} onOpenChange={setListOpen}>
        <SheetContent side="left" className="w-4/5 max-w-xs gap-0 p-0">
          <SheetTitle className="sr-only">{t('list.title')}</SheetTitle>
          <div
            className="flex h-full flex-col overflow-hidden"
            onClickCapture={(e) => {
              if ((e.target as HTMLElement).closest('a')) setListOpen(false);
            }}
          >
            <RepositorySidebar />
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}
