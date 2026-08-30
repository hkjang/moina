import { useCallback, useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react';
import { readableError } from '../api/client';
import {
  DEFAULT_QUERY_RETRIES,
  DEFAULT_QUERY_STALE_MS,
  DEFAULT_QUERY_TTL_MS,
  apiQueryResourceKeys,
  invalidateApiQueries,
  readApiQueryCache,
  requestApiQuery,
  subscribeApiQueryInvalidation,
  writeApiQueryCache,
} from './apiQueryClient';

export interface UseApiQueryOptions {
  ttlMs?: number;
  staleWhileRevalidateMs?: number;
  retries?: number;
  resourceKey?: string | readonly string[];
  keepPreviousData?: boolean;
}

interface QueryState<T> {
  path: string | null;
  data: T | undefined;
  initialLoading: boolean;
  backgroundLoading: boolean;
  error: string | null;
  backgroundError: string | null;
  stale: boolean;
}

function initialState<T>(path: string | null, ttlMs: number, staleMs: number): QueryState<T> {
  if (!path) return { path, data: undefined, initialLoading: false, backgroundLoading: false, error: null, backgroundError: null, stale: false };
  const cached = readApiQueryCache<T>(path, ttlMs, staleMs);
  if (cached.status === 'fresh') return { path, data: cached.data, initialLoading: false, backgroundLoading: false, error: null, backgroundError: null, stale: false };
  if (cached.status === 'stale') return { path, data: cached.data, initialLoading: false, backgroundLoading: true, error: null, backgroundError: null, stale: true };
  return { path, data: undefined, initialLoading: true, backgroundLoading: false, error: null, backgroundError: null, stale: false };
}

export function useApiQuery<T>(path: string | null, options: UseApiQueryOptions = {}) {
  const ttlMs = Math.max(0, options.ttlMs ?? DEFAULT_QUERY_TTL_MS);
  const staleMs = Math.max(0, options.staleWhileRevalidateMs ?? DEFAULT_QUERY_STALE_MS);
  const retries = Math.max(0, Math.floor(options.retries ?? DEFAULT_QUERY_RETRIES));
  const keepPreviousData = options.keepPreviousData !== false;
  const explicitKeys = typeof options.resourceKey === 'string' ? [options.resourceKey] : options.resourceKey || [];
  const resourceSignature = explicitKeys.join('\u0000');
  const resourceKeys = useMemo(() => path ? apiQueryResourceKeys(path, explicitKeys) : new Set<string>(), [path, resourceSignature]);
  const [state, setState] = useState<QueryState<T>>(() => initialState<T>(path, ttlMs, staleMs));
  const stateRef = useRef(state);
  stateRef.current = state;
  const requestID = useRef(0);
  const activeController = useRef<AbortController | null>(null);

  const load = useCallback(async (seed?: T, seedIsStale = false) => {
    if (!path) {
      activeController.current?.abort();
      activeController.current = null;
      requestID.current += 1;
      setState({ path: null, data: undefined, initialLoading: false, backgroundLoading: false, error: null, backgroundError: null, stale: false });
      return;
    }
    activeController.current?.abort();
    const controller = new AbortController();
    activeController.current = controller;
    const current = ++requestID.current;
    const hasData = seed !== undefined;
    setState({
      path,
      data: seed,
      initialLoading: !hasData,
      backgroundLoading: hasData,
      error: null,
      backgroundError: null,
      stale: hasData && seedIsStale,
    });
    try {
      const data = await requestApiQuery<T>(path, controller.signal, { resourceKeys, retries });
      if (current === requestID.current && !controller.signal.aborted) {
        setState({ path, data, initialLoading: false, backgroundLoading: false, error: null, backgroundError: null, stale: false });
      }
    } catch (cause) {
      if (current !== requestID.current || controller.signal.aborted || (cause instanceof DOMException && cause.name === 'AbortError')) return;
      const message = readableError(cause);
      setState({
        path,
        data: seed,
        initialLoading: false,
        backgroundLoading: false,
        error: hasData ? null : message,
        backgroundError: hasData ? message : null,
        stale: hasData,
      });
    } finally {
      if (activeController.current === controller) activeController.current = null;
    }
  }, [path, resourceSignature, retries]);

  useEffect(() => {
    if (!path) {
      void load();
      return;
    }
    const cached = readApiQueryCache<T>(path, ttlMs, staleMs);
    if (cached.status === 'fresh') {
      activeController.current?.abort();
      requestID.current += 1;
      setState({ path, data: cached.data, initialLoading: false, backgroundLoading: false, error: null, backgroundError: null, stale: false });
    } else if (cached.status === 'stale') {
      void load(cached.data, true);
    } else {
      const previous = stateRef.current;
      void load(keepPreviousData && previous.path !== path ? previous.data : undefined, keepPreviousData && previous.path !== path && previous.data !== undefined);
    }
    const unsubscribe = subscribeApiQueryInvalidation(resourceKeys, () => {
      const current = stateRef.current;
      void load(current.path === path ? current.data : undefined, current.path === path && current.data !== undefined);
    });
    return () => {
      unsubscribe();
      requestID.current += 1;
      activeController.current?.abort();
      activeController.current = null;
    };
  }, [path, ttlMs, staleMs, resourceSignature, load, keepPreviousData]);

  const reload = useCallback(() => {
    const current = stateRef.current;
    return load(current.path === path ? current.data : undefined, current.path === path && current.data !== undefined);
  }, [load, path]);

  const setData = useCallback<Dispatch<SetStateAction<T | undefined>>>((next) => {
    setState((current) => {
      const previous = current.path === path ? current.data : undefined;
      const data = typeof next === 'function' ? (next as (value: T | undefined) => T | undefined)(previous) : next;
      if (path && data !== undefined) writeApiQueryCache(path, data, resourceKeys);
      return { path, data, initialLoading: false, backgroundLoading: false, error: null, backgroundError: null, stale: false };
    });
  }, [path, resourceSignature]);

  const invalidate = useCallback(() => path ? invalidateApiQueries(path) : 0, [path]);
  const visible = state.path === path
    ? state
    : keepPreviousData && path && state.data !== undefined
      ? {
          ...state,
          path,
          initialLoading: false,
          backgroundLoading: true,
          error: null,
          backgroundError: null,
          stale: true,
        }
      : initialState<T>(path, ttlMs, staleMs);
  return {
    data: visible.data,
    loading: visible.initialLoading,
    initialLoading: visible.initialLoading,
    backgroundLoading: visible.backgroundLoading,
    refreshing: visible.backgroundLoading,
    error: visible.error,
    backgroundError: visible.backgroundError,
    stale: visible.stale,
    reload,
    setData,
    invalidate,
  };
}

export { clearApiQueryCache, invalidateApiQueries, invalidateApiQuery } from './apiQueryClient';
