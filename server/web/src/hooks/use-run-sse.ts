import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAgentSSEStore } from '@/stores/agent-sse-store'
import type { AgentMessage } from '@/types/api'

/**
 * useRunSSE — subscribes to Phase 2 harness SSE event topics and
 * invalidates / patches TanStack Query cache accordingly.
 *
 * Topics handled:
 *   run_phase_started / run_phase_skipped / run_phase_aborted
 *   gate_started / gate_progress / gate_done
 *   agent_lifecycle
 *
 * The workspace SSE channel is already open (WorkspacePage calls
 * agent-sse-store.addWorkspace). This hook registers a global handler
 * on top of that and does NOT open a second connection.
 *
 * @param workspaceId  Numeric workspace ID (as number).
 * @param projectId    Numeric project ID; pass 0 when unknown — issues query
 *                     invalidation will be skipped (no-op key never matches).
 */
interface Args {
  workspaceId: number
  projectId: number
}

interface GateProgressCache {
  jobId: number
  columnId: number
  specCount: number
  passed: number
}

export function useRunSSE({ workspaceId, projectId }: Args) {
  const qc = useQueryClient()

  useEffect(() => {
    const store = useAgentSSEStore.getState()

    const unsub = store.addGlobalHandler((msg: AgentMessage) => {
      switch (msg.type) {
        case 'run_phase_started':
        case 'run_phase_skipped':
        case 'run_phase_aborted':
          if (projectId > 0) {
            qc.invalidateQueries({ queryKey: ['issues', { projectId }] })
          }
          qc.invalidateQueries({ queryKey: ['workspace-active-run', workspaceId] })
          break

        case 'gate_started': {
          const p = msg.gateJob
          if (!p) break
          qc.setQueryData<GateProgressCache>(['gate-progress', p.runId], {
            jobId: p.jobId,
            columnId: p.columnId,
            specCount: p.specCount,
            passed: 0,
          })
          break
        }

        case 'gate_progress': {
          const p = msg.gateProgress
          if (!p) break
          qc.setQueryData<GateProgressCache>(
            ['gate-progress', p.runId],
            (prev) => (prev ? { ...prev, passed: Math.max(prev.passed, p.index) } : prev),
          )
          break
        }

        case 'gate_done': {
          const p = msg.gateDone
          if (!p) break
          qc.removeQueries({ queryKey: ['gate-progress', p.runId] })
          qc.invalidateQueries({ queryKey: ['workspace-active-run', workspaceId] })
          if (projectId > 0) {
            qc.invalidateQueries({ queryKey: ['issues', { projectId }] })
          }
          break
        }

        case 'agent_lifecycle':
          qc.invalidateQueries({ queryKey: ['workspace-agents', workspaceId] })
          break

        default:
          break
      }
    })

    return () => {
      unsub()
    }
  }, [workspaceId, projectId, qc])
}
