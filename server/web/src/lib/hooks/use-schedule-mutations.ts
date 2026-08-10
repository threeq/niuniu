import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'

interface CreateScheduleData {
  name?: string
  default_message?: string
  schedule_type: 'cron' | 'once'
  action_kind?: 'agent_message' | 'autonomous_discovery'
  cron_expr?: string
  run_at?: string
}

interface UpdateScheduleData {
  name: string
  default_message?: string
  schedule_type: 'cron' | 'once'
  action_kind?: 'agent_message' | 'autonomous_discovery'
  cron_expr?: string
  run_at?: string
}

export function useScheduleMutations(workspaceId: string | number) {
  const queryClient = useQueryClient()

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ['schedules'] })
    queryClient.invalidateQueries({ queryKey: ['schedule-runs'] })
    queryClient.invalidateQueries({ queryKey: ['workspace-schedules', String(workspaceId)] })
    queryClient.invalidateQueries({ queryKey: ['workspaces'] })
  }

  const create = useMutation({
    mutationFn: (data: CreateScheduleData) =>
      api.post(`/workspaces/${workspaceId}/schedules`, data),
    onSuccess: invalidateAll,
  })

  const update = useMutation({
    mutationFn: ({ scheduleId, data }: { scheduleId: number; data: UpdateScheduleData }) =>
      api.put(`/workspaces/${workspaceId}/schedules/${scheduleId}`, data),
    onSuccess: invalidateAll,
  })

  const remove = useMutation({
    mutationFn: (scheduleId: number) =>
      api.delete(`/workspaces/${workspaceId}/schedules/${scheduleId}`),
    onSuccess: invalidateAll,
  })

  const toggle = useMutation({
    mutationFn: ({ scheduleId, enabled }: { scheduleId: number; enabled: boolean }) =>
      api.post(`/workspaces/${workspaceId}/schedules/${scheduleId}/toggle`, { enabled }),
    onSuccess: invalidateAll,
  })

  const trigger = useMutation({
    mutationFn: (scheduleId: number) =>
      api.post(`/workspaces/${workspaceId}/schedules/${scheduleId}/trigger`),
    onSuccess: invalidateAll,
  })

  return { create, update, remove, toggle, trigger }
}
