import { Library } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { WorkspaceKBsManager } from './workspace-kbs-manager'

// First-class workspace knowledge-base panel (KB as a first-class citizen,
// mirroring the files/changes panels). Mount/unmount/sync KBs for this
// workspace; the mounted content is materialized read-only into
// datasets/<name>/ in the workspace file tree. The panel header carries the
// label; the manager renders the list + mount picker without repeating it.

interface WorkspaceKBsPanelProps {
  workspaceId: string
}

export function WorkspaceKBsPanel({ workspaceId }: WorkspaceKBsPanelProps) {
  const { t } = useTranslation('knowledge')
  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 px-3 py-2 shrink-0 border-b border-border">
        <Library className="size-4 text-muted-foreground" aria-hidden />
        <h2 className="text-sm font-medium">{t('workspaceMount.title')}</h2>
      </div>
      <div className="flex-1 min-h-0 overflow-y-auto px-3 py-3">
        <WorkspaceKBsManager workspaceId={workspaceId} showHeader={false} />
      </div>
    </div>
  )
}