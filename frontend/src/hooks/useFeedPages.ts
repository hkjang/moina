import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { normalizeMoin, normalizePage } from '../api/adapters';
import { apiRequest, readableError } from '../api/client';
import { mergeFeedPages, nextUnseenCursor, replaceFeedPage, updateMoinInFeed, type FeedCursor, type FeedPageMap } from '../state/feedCache';
import type { Moin } from '../types';

export type FeedMode = 'for_me' | 'following';

export function useFeedPages(mode: FeedMode) {
  const [pages, setPages] = useState<FeedPageMap>(() => new Map());
  const [cursor, setCursor] = useState<FeedCursor>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const requestID = useRef(0);

  const load = useCallback(async (requestedCursor: FeedCursor, signal?: AbortSignal) => {
    const current = ++requestID.current;
    setLoading(true);
    setError(null);
    const cursorQuery = requestedCursor === null ? '' : `&cursor=${encodeURIComponent(requestedCursor)}`;
    try {
      const raw = await apiRequest<unknown>(`/feed?mode=${mode}&limit=20${cursorQuery}`, { signal });
      if (current !== requestID.current || signal?.aborted) return;
      const page = normalizePage(raw, normalizeMoin);
      setPages((value) => replaceFeedPage(value, requestedCursor, page));
    } catch (cause) {
      if (current === requestID.current && !signal?.aborted) setError(readableError(cause));
    } finally {
      if (current === requestID.current && !signal?.aborted) setLoading(false);
    }
  }, [mode]);

  useEffect(() => {
    const controller = new AbortController();
    void load(cursor, controller.signal);
    return () => controller.abort();
  }, [cursor, load]);

  const items = useMemo(() => mergeFeedPages(pages), [pages]);
  const nextCursor = nextUnseenCursor(pages, cursor);
  const updateMoin = useCallback((moin: Moin) => setPages((value) => updateMoinInFeed(value, moin)), []);
  const loadMore = useCallback(() => { if (nextCursor) setCursor(nextCursor); }, [nextCursor]);
  const reload = useCallback(() => load(cursor), [cursor, load]);
  const reloadFirstPage = useCallback(() => {
    if (cursor === null) return load(null);
    setCursor(null);
    return Promise.resolve();
  }, [cursor, load]);

  return { items, pages, cursor, loading, error, nextCursor, loadMore, reload, reloadFirstPage, updateMoin };
}
