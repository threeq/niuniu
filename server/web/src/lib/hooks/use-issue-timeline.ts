import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

export function useIssueTimeline(issueId: number | null) {
  const { data: timeline = [], isLoading } = useQuery({
    queryKey: ['issue-timeline', issueId],
    queryFn: () => api.getIssueTimeline(issueId!),
    enabled: !!issueId,
  })

  return { timeline, isLoading }
}
