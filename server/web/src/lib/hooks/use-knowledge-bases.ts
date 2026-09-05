import { useQuery, useQueryClient } from '@tanstack/react-query'
import { listKnowledgeBases, isKBBusy, type KnowledgeBase } from '@/lib/kb-api'

/** Single source of truth for the KB list query key, so every mutation in the
 *  feature invalidates the same cache entry. */
export const KB_LIST_KEY = ['knowledge-bases'] as const

/**
 * The owner's knowledge bases.
 *
 * Polls only while at least one KB is mid-ingest, so the progress bar advances
 * live and then the query falls idle — a url-kind download can run for minutes,
 * and polling the list for that whole window (or forever after) is wasteful.
 */
export function useKnowledgeBases() {
  const query = useQuery({
    queryKey: KB_LIST_KEY,
    queryFn: listKnowledgeBases,
    refetchInterval: (q) => {
      const items = q.state.data as KnowledgeBase[] | undefined
      return items?.some(isKBBusy) ? 2000 : false
    },
  })
  return {
    knowledgeBases: query.data,
    isLoading: query.isLoading,
    isError: query.isError,
    refetch: query.refetch,
  }
}

/** One KB from the list cache, by id. Avoids a second request for the detail
 *  view: the list already carries every field the detail page renders. */
export function useKnowledgeBase(id: number | undefined) {
  const { knowledgeBases, isLoading, isError } = useKnowledgeBases()
  return {
    kb: id == null ? undefined : knowledgeBases?.find((k) => k.id === id),
    isLoading,
    isError,
  }
}

/** Invalidator for the KB list, for use in mutation `onSuccess`. */
export function useInvalidateKnowledgeBases() {
  const qc = useQueryClient()
  return () => qc.invalidateQueries({ queryKey: KB_LIST_KEY })
}
