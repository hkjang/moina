import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiRequest } from '../api/client';
import {
  clearApiQueryCache,
  invalidateApiQueries,
  requestApiQuery,
} from './apiQueryClient';
import { useApiQuery } from './useApiQuery';

vi.mock('../api/client', async (load) => {
  const actual = await load<typeof import('../api/client')>();
  return { ...actual, apiRequest: vi.fn() };
});

const mockedRequest = vi.mocked(apiRequest);

describe('useApiQuery', () => {
  beforeEach(() => {
    mockedRequest.mockReset();
    clearApiQueryCache({ abort: true });
  });

  afterEach(() => {
    cleanup();
    clearApiQueryCache({ abort: true });
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('path 변경과 unmount에서 해당 소비자의 요청을 취소한다', async () => {
    const signals: AbortSignal[] = [];
    mockedRequest.mockImplementation((_path, options) => new Promise((_resolve, reject) => {
      const signal = options?.signal;
      if (!signal) throw new Error('AbortSignal이 필요합니다.');
      signals.push(signal);
      signal.addEventListener('abort', () => reject(new DOMException('취소됨', 'AbortError')), { once: true });
    }));

    const { rerender, unmount } = renderHook(({ path }) => useApiQuery(path), { initialProps: { path: '/first' } });
    await waitFor(() => expect(signals).toHaveLength(1));
    rerender({ path: '/second' });
    await waitFor(() => expect(signals).toHaveLength(2));
    expect(signals[0].aborted).toBe(true);
    unmount();
    expect(signals[1].aborted).toBe(true);
  });

  it('동일 GET의 네트워크 요청을 병합하되 각 소비자에 결과를 전달한다', async () => {
    let resolveRequest!: (value: unknown) => void;
    mockedRequest.mockReturnValue(new Promise((resolve) => { resolveRequest = resolve; }));
    const first = renderHook(() => useApiQuery<{ value: string }>('/shared'));
    const second = renderHook(() => useApiQuery<{ value: string }>('/shared'));

    await waitFor(() => expect(mockedRequest).toHaveBeenCalledTimes(1));
    act(() => resolveRequest({ value: 'merged' }));
    await waitFor(() => expect(first.result.current.data?.value).toBe('merged'));
    expect(second.result.current.data?.value).toBe('merged');
    first.unmount();
    second.unmount();
  });

  it('TTL 안의 cache는 즉시 재사용하고 추가 GET을 보내지 않는다', async () => {
    mockedRequest.mockResolvedValue({ value: 'cached' });
    const first = renderHook(() => useApiQuery<{ value: string }>('/cache'));
    await waitFor(() => expect(first.result.current.data?.value).toBe('cached'));
    first.unmount();

    const second = renderHook(() => useApiQuery<{ value: string }>('/cache'));
    expect(second.result.current.data?.value).toBe('cached');
    expect(second.result.current.loading).toBe(false);
    expect(mockedRequest).toHaveBeenCalledTimes(1);
    second.unmount();
  });

  it('stale cache를 유지하며 background 상태로 갱신한다', async () => {
    let now = 1_000;
    vi.spyOn(Date, 'now').mockImplementation(() => now);
    mockedRequest.mockResolvedValueOnce({ value: 'stale' });
    const first = renderHook(() => useApiQuery<{ value: string }>('/stale', { ttlMs: 10, staleWhileRevalidateMs: 1_000 }));
    await waitFor(() => expect(first.result.current.data?.value).toBe('stale'));
    first.unmount();

    now += 20;
    let refresh!: (value: unknown) => void;
    mockedRequest.mockReturnValueOnce(new Promise((resolve) => { refresh = resolve; }));
    const second = renderHook(() => useApiQuery<{ value: string }>('/stale', { ttlMs: 10, staleWhileRevalidateMs: 1_000 }));
    expect(second.result.current.data?.value).toBe('stale');
    expect(second.result.current).toMatchObject({ loading: false, initialLoading: false, backgroundLoading: true, stale: true });

    act(() => refresh({ value: 'fresh' }));
    await waitFor(() => expect(second.result.current.data?.value).toBe('fresh'));
    expect(second.result.current.backgroundLoading).toBe(false);
    second.unmount();
  });

  it('filter path 변경 중 previous data를 유지한다', async () => {
    mockedRequest.mockResolvedValueOnce({ items: ['all'] });
    const query = renderHook(({ filter }) => useApiQuery<{ items: string[] }>(`/search?filter=${filter}`), {
      initialProps: { filter: 'all' },
    });
    await waitFor(() => expect(query.result.current.data?.items).toEqual(['all']));
    let resolveNext!: (value: unknown) => void;
    mockedRequest.mockReturnValueOnce(new Promise((resolve) => { resolveNext = resolve; }));
    query.rerender({ filter: 'unread' });
    expect(query.result.current.data?.items).toEqual(['all']);
    expect(query.result.current.initialLoading).toBe(false);
    expect(query.result.current.backgroundLoading).toBe(true);
    await waitFor(() => expect(query.result.current.backgroundLoading).toBe(true));
    expect(query.result.current.data?.items).toEqual(['all']);
    act(() => resolveNext({ items: ['unread'] }));
    await waitFor(() => expect(query.result.current.data?.items).toEqual(['unread']));
    query.unmount();
  });

  it('background 실패는 stale data와 분리된 오류 상태를 유지한다', async () => {
    let now = 1_000;
    vi.spyOn(Date, 'now').mockImplementation(() => now);
    mockedRequest.mockResolvedValueOnce({ value: 'usable' });
    const first = renderHook(() => useApiQuery<{ value: string }>('/background-error', { ttlMs: 1, staleWhileRevalidateMs: 1_000, retries: 0 }));
    await waitFor(() => expect(first.result.current.data?.value).toBe('usable'));
    first.unmount();

    now += 10;
    mockedRequest.mockRejectedValueOnce(new ApiError('갱신 실패', 500));
    const second = renderHook(() => useApiQuery<{ value: string }>('/background-error', { ttlMs: 1, staleWhileRevalidateMs: 1_000, retries: 0 }));
    await waitFor(() => expect(second.result.current.backgroundError).toBe('갱신 실패'));
    expect(second.result.current.data?.value).toBe('usable');
    expect(second.result.current.error).toBeNull();
    expect(second.result.current.loading).toBe(false);
    second.unmount();
  });

  it('resource key invalidation은 관련 cache를 지우고 구독 중인 query만 갱신한다', async () => {
    mockedRequest.mockResolvedValueOnce({ value: 'before' });
    const query = renderHook(() => useApiQuery<{ value: string }>('/posts?limit=10', { resourceKey: 'timeline' }));
    await waitFor(() => expect(query.result.current.data?.value).toBe('before'));
    mockedRequest.mockResolvedValueOnce({ value: 'after' });

    act(() => { expect(invalidateApiQueries('timeline')).toBe(1); });
    await waitFor(() => expect(query.result.current.data?.value).toBe('after'));
    expect(mockedRequest).toHaveBeenCalledTimes(2);
    query.unmount();
  });

  it('mutation invalidation은 공유 중인 이전 GET을 폐기하고 새 응답만 반영한다', async () => {
    let resolveOld!: (value: unknown) => void;
    let resolveFresh!: (value: unknown) => void;
    const signals: AbortSignal[] = [];
    mockedRequest
      .mockImplementationOnce((_path, options) => {
        if (options?.signal) signals.push(options.signal);
        // Deliberately ignore abort to cover browsers/adapters that still
        // resolve an already-issued request after cancellation.
        return new Promise((resolve) => { resolveOld = resolve; });
      })
      .mockImplementationOnce((_path, options) => {
        if (options?.signal) signals.push(options.signal);
        return new Promise((resolve) => { resolveFresh = resolve; });
      });
    const first = renderHook(() => useApiQuery<{ value: string }>('/posts?limit=10', { resourceKey: 'timeline' }));
    const second = renderHook(() => useApiQuery<{ value: string }>('/posts?limit=10', { resourceKey: 'timeline' }));
    await waitFor(() => expect(mockedRequest).toHaveBeenCalledTimes(1));

    act(() => { invalidateApiQueries('timeline'); });
    await waitFor(() => expect(mockedRequest).toHaveBeenCalledTimes(2));
    expect(signals[0].aborted).toBe(true);
    act(() => resolveOld({ value: 'mutation 이전' }));
    expect(first.result.current.data?.value).not.toBe('mutation 이전');
    expect(second.result.current.data?.value).not.toBe('mutation 이전');

    act(() => resolveFresh({ value: 'mutation 이후' }));
    await waitFor(() => expect(first.result.current.data?.value).toBe('mutation 이후'));
    expect(second.result.current.data?.value).toBe('mutation 이후');
    first.unmount();
    second.unmount();
  });
});

describe('apiQueryClient retry', () => {
  beforeEach(() => {
    mockedRequest.mockReset();
    clearApiQueryCache({ abort: true });
    vi.useFakeTimers();
  });

  afterEach(() => {
    clearApiQueryCache({ abort: true });
    vi.useRealTimers();
  });

  it.each([
    ['network', new TypeError('offline'), 250],
    ['429 Retry-After', new ApiError('limited', 429, 'limited', undefined, 1_200), 1_200],
    ['503', new ApiError('unavailable', 503), 250],
  ])('%s 오류만 제한적으로 재시도한다', async (_label, failure, delay) => {
    mockedRequest.mockRejectedValueOnce(failure).mockResolvedValueOnce({ ok: true });
    const result = requestApiQuery<{ ok: boolean }>('/retry', new AbortController().signal, { retries: 1 });
    await vi.advanceTimersByTimeAsync(delay - 1);
    expect(mockedRequest).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    await expect(result).resolves.toEqual({ ok: true });
    expect(mockedRequest).toHaveBeenCalledTimes(2);
  });

  it('서버의 60초 Retry-After를 조기에 줄이지 않는다', async () => {
    mockedRequest.mockRejectedValueOnce(new ApiError('limited', 429, 'limited', undefined, 60_000)).mockResolvedValueOnce({ ok: true });
    const result = requestApiQuery<{ ok: boolean }>('/retry-after-60', new AbortController().signal, { retries: 1 });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(mockedRequest).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(29_999);
    expect(mockedRequest).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    await expect(result).resolves.toEqual({ ok: true });
    expect(mockedRequest).toHaveBeenCalledTimes(2);
  });

  it('그 밖의 HTTP 오류는 재시도하지 않는다', async () => {
    mockedRequest.mockRejectedValueOnce(new ApiError('bad request', 400));
    await expect(requestApiQuery('/no-retry', new AbortController().signal, { retries: 2 })).rejects.toMatchObject({ status: 400 });
    expect(mockedRequest).toHaveBeenCalledTimes(1);
  });

  it('브라우저가 offline이면 network 오류를 재시도하지 않는다', async () => {
    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
    mockedRequest.mockRejectedValueOnce(new TypeError('offline'));
    await expect(requestApiQuery('/offline', new AbortController().signal, { retries: 2 })).rejects.toBeInstanceOf(TypeError);
    expect(mockedRequest).toHaveBeenCalledTimes(1);
  });
});
