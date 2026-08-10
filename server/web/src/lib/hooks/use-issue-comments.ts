import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'

export function useIssueComments(issueId: number | null) {
  const queryClient = useQueryClient()
  const queryKey = ['issue-comments', issueId]

  const { data: comments = [], isLoading } = useQuery({
    queryKey,
    queryFn: () => api.listIssueComments(issueId!),
    enabled: !!issueId,
  })

  const createMutation = useMutation({
    mutationFn: (data: { author: string; content: string }) =>
      api.createIssueComment(issueId!, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey })
      queryClient.invalidateQueries({ queryKey: ['issue-timeline', issueId] })
    },
  })

  const updateMutation = useMutation({
    mutationFn: (data: { id: number; content: string }) =>
      api.updateIssueComment(data.id, { content: data.content }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey })
      queryClient.invalidateQueries({ queryKey: ['issue-timeline', issueId] })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.deleteIssueComment(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey })
      queryClient.invalidateQueries({ queryKey: ['issue-timeline', issueId] })
    },
  })

  return {
    comments,
    isLoading,
    createComment: createMutation.mutate,
    updateComment: updateMutation.mutate,
    deleteComment: deleteMutation.mutate,
    isCreating: createMutation.isPending,
  }
}
