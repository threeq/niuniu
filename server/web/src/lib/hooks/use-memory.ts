import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { memoryApi, type CreateMemoryInput, type UpdateMemoryInput } from '@/lib/memory-api'
import { ApiError } from '@/lib/api'
import i18n from '@/i18n'
import { toast } from 'sonner'

const extractStatusKey = (workspaceId?: number) => ['memory-extract-status', workspaceId] as const

type NavigateFn = ReturnType<typeof useNavigate>

// Toast options that tag which workspace the extraction belongs to (so messages
// are unambiguous across workspaces) and offer a one-click jump to it. Applied to
// success, "no learnings", and failure toasts alike.
function workspaceToastOpts(navigate: NavigateFn, workspaceId: number) {
  return {
    description: i18n.t('projects:hooks.extractWorkspace', { id: workspaceId }),
    action: {
      label: i18n.t('projects:hooks.gotoWorkspace'),
      onClick: () => {
        void navigate({ to: '/workspaces/$id', params: { id: String(workspaceId) } })
      },
    },
  }
}

// Workspaces with a user-initiated extraction awaiting its result. Tying the
// completion toast to an explicit kickoff (rather than a render transition) makes
// it fire exactly once per run and immune to component remounts / observing a
// stale completed state on mount. Module-level so the kickoff (useExtractMemory)
// and the poller (useExtractStatus) share it.
const awaitingExtract = new Set<number>()

const listKey = (projectId: number) => ['memories', 'project', projectId] as const

export function useProjectMemories(projectId: number | null) {
  return useQuery({
    queryKey: ['memories', 'project', projectId],
    queryFn: () => memoryApi.listForProject(projectId!),
    enabled: !!projectId,
  })
}

export function useMemoryVersions(id: number | null) {
  return useQuery({
    queryKey: ['memories', 'versions', id],
    queryFn: () => memoryApi.versions(id!),
    enabled: !!id,
  })
}

export function useCreateMemory(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (data: CreateMemoryInput) => memoryApi.create(data),
    onSuccess: () => qc.invalidateQueries({ queryKey: listKey(projectId) }),
  })
}

export function useUpdateMemory(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...data }: { id: number } & UpdateMemoryInput) => memoryApi.update(id, data),
    onSuccess: (_d, vars) => {
      qc.invalidateQueries({ queryKey: listKey(projectId) })
      qc.invalidateQueries({ queryKey: ['memories', 'versions', vars.id] })
    },
  })
}

export function useDeleteMemory(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => memoryApi.delete(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: listKey(projectId) }),
  })
}

export function useRollbackMemory(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, version }: { id: number; version: number }) => memoryApi.rollback(id, version),
    onSuccess: (_d, vars) => {
      qc.invalidateQueries({ queryKey: listKey(projectId) })
      qc.invalidateQueries({ queryKey: ['memories', 'versions', vars.id] })
    },
  })
}

// --- Automatic memory-staleness management (per project) ---

const reviewQueueKey = (projectId: number) => ['memories', 'review-queue', projectId] as const

// useRunMaintenanceOnce triggers a single maintenance run regardless of schedule
// (the header "run once" button). The run is async on the server.
export function useRunMaintenanceOnce(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => memoryApi.runMaintenanceOnce(projectId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['memories', 'sweep-runs', projectId] })
      toast.success(i18n.t('projects:tabs.settings.memory.runStarted'))
    },
    onError: (e: unknown) =>
      toast.error(
        i18n.t('projects:tabs.settings.memory.runFailed', {
          message: e instanceof Error ? e.message : String(e),
        }),
      ),
  })
}

// useMemorySchedule reads a project's maintenance schedule (empty cron = OFF),
// used to show the enabled/disabled status in the Memory tab header.
export function useMemorySchedule(projectId: number | null) {
  return useQuery({
    queryKey: ['memory-schedule', projectId],
    queryFn: () => memoryApi.getSchedule(projectId!),
    enabled: !!projectId,
  })
}

// useSweepRuns lists the automatic staleness-sweep execution log for a project.
export function useSweepRuns(projectId: number | null) {
  return useQuery({
    queryKey: ['memories', 'sweep-runs', projectId],
    queryFn: () => memoryApi.listSweepRuns(projectId!),
    enabled: !!projectId,
  })
}

// useClearSweepRuns clears a project's automatic-maintenance run log.
export function useClearSweepRuns(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => memoryApi.clearSweepRuns(projectId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['memories', 'sweep-runs', projectId] })
      toast.success(i18n.t('projects:tabs.memory.staleness.historyCleared'))
    },
    onError: () => toast.error(i18n.t('projects:tabs.memory.staleness.actionFailed')),
  })
}

// useReviewQueue lists memories flagged stale and awaiting a keep/delete decision.
export function useReviewQueue(projectId: number | null) {
  return useQuery({
    queryKey: ['memories', 'review-queue', projectId],
    queryFn: () => memoryApi.listReviewQueue(projectId!),
    enabled: !!projectId,
  })
}

// useKeepMemory keeps a queued memory (restore + clear the review flag).
export function useKeepMemory(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => memoryApi.keepMemory(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: reviewQueueKey(projectId) })
      qc.invalidateQueries({ queryKey: listKey(projectId) })
      toast.success(i18n.t('projects:tabs.memory.staleness.keptToast'))
    },
    onError: () => toast.error(i18n.t('projects:tabs.memory.staleness.actionFailed')),
  })
}

// useConfirmDeleteMemory confirms a queued memory's deletion (clears the flag,
// leaves it soft-deleted / still restorable).
export function useConfirmDeleteMemory(projectId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => memoryApi.confirmDeleteMemory(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: reviewQueueKey(projectId) })
      toast.success(i18n.t('projects:tabs.memory.staleness.deletedToast'))
    },
    onError: () => toast.error(i18n.t('projects:tabs.memory.staleness.actionFailed')),
  })
}

// useExtractMemory kicks off async server-side AI extraction over a workspace
// session. The request returns immediately ({running:true}); useExtractStatus
// polls for completion and reports the result. We optimistically mark the status
// running so the button spinner appears without waiting for the first poll.
export function useExtractMemory() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  return useMutation({
    mutationFn: (workspaceId: number) => memoryApi.extractSession(workspaceId),
    onSuccess: (_data, workspaceId) => {
      awaitingExtract.add(workspaceId)
      queryClient.setQueryData(extractStatusKey(workspaceId), {
        data: { running: true, extracted: 0 },
      })
    },
    onError: (error: unknown, workspaceId) => {
      const opts = workspaceToastOpts(navigate, workspaceId)
      if (error instanceof ApiError) {
        if (error.status === 422) {
          toast.error(i18n.t('projects:hooks.noLinkedProject'), opts)
          return
        }
        if (error.status === 409) {
          toast.warning(i18n.t('projects:hooks.extractInProgress'), opts)
          return
        }
      }
      toast.error(i18n.t('projects:hooks.extractFailed'), opts)
    },
  })
}

// useExtractStatus polls async extraction progress for a workspace while the
// server reports it running, drives the toolbar spinner, and toasts the result
// once on completion. Polling self-stops when not running OR on error — so a
// missing/failing status route can never leave the spinner stuck or spam toasts.
export function useExtractStatus(workspaceId?: number) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const query = useQuery({
    queryKey: extractStatusKey(workspaceId),
    queryFn: () => memoryApi.extractStatus(workspaceId as number),
    enabled: !!workspaceId,
    refetchInterval: (q) =>
      q.state.status === 'success' && q.state.data?.data?.running ? 1500 : false,
    refetchOnWindowFocus: false,
  })

  // Only trust the running flag from a successful fetch; an errored status query
  // (404 on an old binary, transient 5xx) must not keep the spinner alive.
  const status = query.isSuccess ? query.data?.data : undefined
  useEffect(() => {
    if (workspaceId == null || !status) return
    if (!status.running && awaitingExtract.has(workspaceId)) {
      awaitingExtract.delete(workspaceId)
      const opts = workspaceToastOpts(navigate, workspaceId)
      if (status.error) {
        toast.error(i18n.t('projects:hooks.extractFailed'), opts)
      } else if (status.extracted > 0) {
        toast.success(i18n.t('projects:hooks.extractedLearnings', { count: status.extracted }), opts)
        queryClient.invalidateQueries({ queryKey: ['memories'] })
      } else {
        toast.info(i18n.t('projects:hooks.noLearningsExtracted'), opts)
      }
    }
  }, [status, workspaceId, queryClient, navigate])

  return query
}
