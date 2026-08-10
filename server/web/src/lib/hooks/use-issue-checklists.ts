import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'

export function useIssueChecklists(issueId: number | null) {
  const queryClient = useQueryClient()
  const queryKey = ['issue-checklists', issueId]
  const issueQueryKey = ['issue', String(issueId)]

  const { data: checklists = [], isLoading } = useQuery({
    queryKey,
    queryFn: () => api.listChecklists(issueId!),
    enabled: !!issueId,
  })

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey })
    queryClient.invalidateQueries({ queryKey: issueQueryKey })
    queryClient.invalidateQueries({ queryKey: ['issues'] })
    queryClient.invalidateQueries({ queryKey: ['all-issues'] })
  }

  const createMutation = useMutation({
    mutationFn: (title: string) => api.createChecklist(issueId!, { title }),
    onSuccess: invalidateAll,
  })

  const updateMutation = useMutation({
    mutationFn: (data: { id: number; title: string; is_completed: number }) =>
      api.updateChecklist(data.id, { title: data.title, is_completed: data.is_completed }),
    onSuccess: invalidateAll,
  })

  const updatePositionMutation = useMutation({
    mutationFn: (data: { id: number; position: number }) =>
      api.updateChecklistPosition(data.id, { position: data.position }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.deleteChecklist(id),
    onSuccess: invalidateAll,
  })

  return {
    checklists,
    isLoading,
    createChecklist: createMutation.mutate,
    updateChecklist: updateMutation.mutate,
    updateChecklistPosition: updatePositionMutation.mutate,
    deleteChecklist: deleteMutation.mutate,
  }
}
