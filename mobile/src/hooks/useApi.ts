import { useState, useCallback, useEffect, useRef } from 'react';

interface UseApiState<T> {
  data: T | undefined;
  loading: boolean;
  error: string | undefined;
}

interface UseApiReturn<T> extends UseApiState<T> {
  refetch: () => Promise<void>;
}

export function useApi<T>(fetcher: () => Promise<T>, deps: unknown[] = []): UseApiReturn<T> {
  const [state, setState] = useState<UseApiState<T>>({
    data: undefined,
    loading: true,
    error: undefined,
  });
  const mountedRef = useRef(true);
  const abortRef = useRef<AbortController | null>(null);

  const refetch = useCallback(async () => {
    // Abort any in-flight request
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setState((s) => ({ ...s, loading: true, error: undefined }));
    try {
      const data = await fetcher();
      if (mountedRef.current && !controller.signal.aborted) {
        setState({ data, loading: false, error: undefined });
      }
    } catch (err: any) {
      if (err?.name === 'AbortError') return; // Ignore abort errors
      if (mountedRef.current && !controller.signal.aborted) {
        const message = err?.message || '未知错误';
        setState({ data: undefined, loading: false, error: message });
      }
    }
  }, deps);

  useEffect(() => {
    mountedRef.current = true;
    refetch();
    return () => {
      mountedRef.current = false;
      abortRef.current?.abort();
    };
  }, [refetch]);

  return { ...state, refetch };
}
