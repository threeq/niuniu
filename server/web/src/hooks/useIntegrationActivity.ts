import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { integrationApi } from '@/lib/integration-api';
import type { IntegrationActivityFilters } from '@/types/integration';

/** Rows per page for the integration activity feed. */
export const ACTIVITY_PAGE_SIZE = 20;

export const integrationActivityKey = (
  filters: IntegrationActivityFilters,
  page: number,
) => ['integration-activity', filters, page] as const;

/**
 * One page of the caller's merged integration activity (external API calls +
 * data queries). `filters` is part of the query key, so changing a filter
 * refetches; the previous page stays visible while the next one loads.
 */
export function useIntegrationActivity(
  filters: IntegrationActivityFilters,
  page: number,
) {
  return useQuery({
    queryKey: integrationActivityKey(filters, page),
    queryFn: () =>
      integrationApi.listActivity({
        filters,
        limit: ACTIVITY_PAGE_SIZE,
        offset: page * ACTIVITY_PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  });
}
