import { ApiError, apiRequest } from '../api/client';

export const DEFAULT_QUERY_TTL_MS = 15_000;
export const DEFAULT_QUERY_STALE_MS = 120_000;
export const DEFAULT_QUERY_RETRIES = 2;

const MAX_RETRY_BACKOFF_MS = 30_000;
const MAX_RETRY_AFTER_MS = 5 * 60_000;
const NETWORK_RETRY_BASE_MS = 250;

export interface ApiQueryRequestOptions {
  resourceKeys?: Iterable<string>;
  retries?: number;
}

interface CacheEntry {
  data: unknown;
  storedAt: number;
  resourceKeys: Set<string>;
}

interface InflightRequest {
  controller: AbortController;
  consumers: Set<symbol>;
  promise: Promise<unknown>;
  resourceKeys: Set<string>;
}

interface InvalidationSubscriber {
  resourceKeys: Set<string>;
  callback: () => void;
}

export type ApiQueryCacheSnapshot<T> =
  | { status: 'fresh' | 'stale'; data: T }
  | { status: 'miss' };

// A cached entry is only dropped when the same path is read again and found
// stale, so paths that are never revisited - every cursor page of a Flow, every
// search term - would accumulate for the life of the tab. The Map preserves
// insertion order, which makes it the LRU list as well as the store.
export const MAX_QUERY_CACHE_ENTRIES = 100;

const cache = new Map<string, CacheEntry>();

function touchCacheEntry(path: string, entry: CacheEntry) {
  cache.delete(path);
  cache.set(path, entry);
}

function evictOverflowingEntries() {
  while (cache.size > MAX_QUERY_CACHE_ENTRIES) {
    const oldest = cache.keys().next();
    if (oldest.done) return;
    cache.delete(oldest.value);
  }
}
const inflight = new Map<string, InflightRequest>();
const subscribers = new Map<symbol, InvalidationSubscriber>();

function abortError() {
  return new DOMException('요청을 취소했습니다.', 'AbortError');
}

function cleanKey(value: string) {
  return value.trim();
}

export function apiQueryResourceKeys(path: string, explicit?: string | readonly string[]) {
  const keys = new Set<string>([path]);
  try {
    const url = new URL(path, 'http://moina.local');
    keys.add(url.pathname);
    const segment = url.pathname.split('/').filter(Boolean)[0];
    if (segment) {
      keys.add(segment);
      keys.add(`/${segment}`);
    }
  } catch {
    // The request layer reports malformed paths; invalidation can still use the raw key.
  }
  const values = typeof explicit === 'string' ? [explicit] : explicit || [];
  for (const value of values) {
    const key = cleanKey(value);
    if (key) keys.add(key);
  }
  return keys;
}

export function readApiQueryCache<T>(
  path: string,
  ttlMs = DEFAULT_QUERY_TTL_MS,
  staleWhileRevalidateMs = DEFAULT_QUERY_STALE_MS,
  now = Date.now(),
): ApiQueryCacheSnapshot<T> {
  const entry = cache.get(path);
  if (!entry) return { status: 'miss' };
  const age = Math.max(0, now - entry.storedAt);
  if (age <= Math.max(0, ttlMs)) {
    touchCacheEntry(path, entry);
    return { status: 'fresh', data: entry.data as T };
  }
  if (age <= Math.max(0, ttlMs) + Math.max(0, staleWhileRevalidateMs)) {
    touchCacheEntry(path, entry);
    return { status: 'stale', data: entry.data as T };
  }
  cache.delete(path);
  return { status: 'miss' };
}

export function writeApiQueryCache<T>(path: string, data: T, resourceKeys: Iterable<string>, now = Date.now()) {
  const previous = cache.get(path);
  cache.delete(path);
  cache.set(path, {
    data,
    storedAt: now,
    resourceKeys: new Set([...(previous?.resourceKeys || []), ...resourceKeys]),
  });
  evictOverflowingEntries();
}

function retryable(error: unknown) {
  if (typeof navigator !== 'undefined' && navigator.onLine === false) return false;
  return error instanceof TypeError || (error instanceof ApiError && [429, 503].includes(error.status));
}

function retryDelay(error: unknown, attempt: number) {
  if (error instanceof ApiError && error.retryAfterMs !== undefined) {
    // Retry-After is the server's authoritative recovery window. Keep a
    // defensive upper bound without shortening MOINA's 60-second limit.
    return Math.min(MAX_RETRY_AFTER_MS, Math.max(0, error.retryAfterMs));
  }
  return Math.min(MAX_RETRY_BACKOFF_MS, NETWORK_RETRY_BASE_MS * 2 ** attempt);
}

function waitForRetry(delayMs: number, signal: AbortSignal) {
  if (signal.aborted) return Promise.reject(abortError());
  return new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal.removeEventListener('abort', cancel);
      resolve();
    }, delayMs);
    const cancel = () => {
      window.clearTimeout(timer);
      reject(abortError());
    };
    signal.addEventListener('abort', cancel, { once: true });
  });
}

async function requestWithRetry(path: string, signal: AbortSignal, retries: number) {
  let attempt = 0;
  while (true) {
    try {
      return await apiRequest<unknown>(path, { signal });
    } catch (error) {
      if (signal.aborted) throw abortError();
      if (!retryable(error) || attempt >= retries) throw error;
      await waitForRetry(retryDelay(error, attempt), signal);
      attempt += 1;
    }
  }
}

function releaseConsumer(request: InflightRequest, consumer: symbol) {
  request.consumers.delete(consumer);
  if (request.consumers.size === 0 && !request.controller.signal.aborted) request.controller.abort();
}

export function requestApiQuery<T>(path: string, signal: AbortSignal, options: ApiQueryRequestOptions = {}) {
  const resourceKeys = new Set(options.resourceKeys || apiQueryResourceKeys(path));
  let request = inflight.get(path);
  if (request?.controller.signal.aborted) {
    inflight.delete(path);
    request = undefined;
  }
  if (!request) {
    const controller = new AbortController();
    const created: InflightRequest = {
      controller,
      consumers: new Set(),
      resourceKeys,
      promise: Promise.resolve(),
    };
    created.promise = requestWithRetry(path, controller.signal, Math.max(0, options.retries ?? DEFAULT_QUERY_RETRIES))
      .then((data) => {
        // A mutation may invalidate and detach this shared request even when
        // the underlying fetch implementation ignores AbortSignal. Such a
        // response must never repopulate the cache after the mutation.
        if (controller.signal.aborted || inflight.get(path) !== created) throw abortError();
        writeApiQueryCache(path, data, created.resourceKeys);
        return data;
      })
      .finally(() => {
        if (inflight.get(path) === created) inflight.delete(path);
      });
    request = created;
    inflight.set(path, request);
  } else {
    for (const key of resourceKeys) request.resourceKeys.add(key);
  }

  const shared = request;
  const consumer = Symbol(path);
  shared.consumers.add(consumer);
  return new Promise<T>((resolve, reject) => {
    let settled = false;
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener('abort', cancel);
      releaseConsumer(shared, consumer);
      callback();
    };
    const cancel = () => finish(() => reject(abortError()));
    if (signal.aborted) {
      cancel();
      return;
    }
    signal.addEventListener('abort', cancel, { once: true });
    shared.promise.then(
      (data) => finish(() => resolve(data as T)),
      (error) => finish(() => reject(error)),
    );
  });
}

function invalidationKeys(value: string | readonly string[]) {
  const values = typeof value === 'string' ? [value] : value;
  return new Set(values.map(cleanKey).filter(Boolean));
}

function intersects(left: Set<string>, right: Set<string>) {
  for (const value of left) if (right.has(value)) return true;
  return false;
}

export function subscribeApiQueryInvalidation(resourceKeys: Iterable<string>, callback: () => void) {
  const id = Symbol('api-query-subscriber');
  subscribers.set(id, { resourceKeys: new Set(resourceKeys), callback });
  return () => subscribers.delete(id);
}

export function invalidateApiQueries(resourceKey: string | readonly string[]) {
  const keys = invalidationKeys(resourceKey);
  if (!keys.size) return 0;
  let invalidated = 0;
  for (const [path, entry] of cache) {
    if (!intersects(entry.resourceKeys, keys)) continue;
    cache.delete(path);
    invalidated += 1;
  }
  // Abort and detach matching pre-mutation GETs before subscriber callbacks
  // start their replacement request. Every subscriber then joins one fresh
  // shared request instead of reattaching to a stale inflight promise.
  for (const [path, request] of inflight) {
    if (!intersects(request.resourceKeys, keys)) continue;
    inflight.delete(path);
    request.controller.abort();
  }
  const callbacks = new Set<() => void>();
  for (const subscriber of subscribers.values()) {
    if (intersects(subscriber.resourceKeys, keys)) callbacks.add(subscriber.callback);
  }
  callbacks.forEach((callback) => callback());
  return invalidated;
}

export const invalidateApiQuery = invalidateApiQueries;

export function clearApiQueryCache({ abort = false }: { abort?: boolean } = {}) {
  cache.clear();
  if (abort) {
    for (const request of inflight.values()) request.controller.abort();
    inflight.clear();
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('moina:api-mutated', (event) => {
    const detail = (event as CustomEvent<{ path?: string }>).detail;
    if (detail?.path) invalidateApiQueries([...apiQueryResourceKeys(detail.path)]);
  });
}
