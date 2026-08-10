import { Outlet } from '@tanstack/react-router';
import { Group, Panel, Separator } from 'react-resizable-panels';
import { RepositorySidebar } from './repository-sidebar';

export function RepositoryLayout() {
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
