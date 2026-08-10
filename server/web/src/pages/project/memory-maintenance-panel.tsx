import { ProjectMemoryScheduleCard } from './memory-schedule-card'
import { MemoryStalenessSection } from './memory-staleness-section'

interface Props {
  projectId: number
}

// MemoryMaintenancePanel is the inline (non-modal) automatic-memory-maintenance
// area shown above the memory list when toggled open from the tab header: the
// schedule settings (enable + cron + run-once) and the execution history /
// review queue.
export function MemoryMaintenancePanel({ projectId }: Props) {
  return (
    <div className="space-y-4 rounded-lg border bg-muted/30 p-4">
      <ProjectMemoryScheduleCard projectId={projectId} />
      <MemoryStalenessSection projectId={projectId} />
    </div>
  )
}
