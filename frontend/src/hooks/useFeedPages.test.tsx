import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { apiRequest } from '../api/client';
import { useFeedPages } from './useFeedPages';

vi.mock('../api/client', async (load) => {
  const actual = await load<typeof import('../api/client')>();
  return { ...actual, apiRequest: vi.fn() };
});

const mockedRequest = vi.mocked(apiRequest);
const rawMoin = (id: string, content = id) => ({ id, content, author: { id: 'u', username: 'user', displayName: '사용자' }, createdAt: '2026-01-01T00:00:00Z' });

describe('useFeedPages', () => {
  beforeEach(() => mockedRequest.mockReset());

  it('opaque cursor page를 병합하고 현재 page reload는 교체한다', async () => {
    const cursor = 'v2.opaque/+==';
    mockedRequest.mockResolvedValueOnce({ items: [rawMoin('a')], nextCursor: cursor });
    const { result } = renderHook(() => useFeedPages('for_me'));
    await waitFor(() => expect(result.current.items.map((item) => item.id)).toEqual(['a']));

    mockedRequest.mockResolvedValueOnce({ items: [rawMoin('a', '최신 중복'), rawMoin('b-old')] });
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.items.map((item) => item.id)).toEqual(['a', 'b-old']));
    expect(result.current.items[0].content).toBe('최신 중복');
    expect(mockedRequest).toHaveBeenLastCalledWith(`/feed?mode=for_me&limit=20&cursor=${encodeURIComponent(cursor)}`, expect.any(Object));

    mockedRequest.mockResolvedValueOnce({ items: [rawMoin('b-new')] });
    await act(async () => { await result.current.reload(); });
    expect(result.current.items.map((item) => item.id)).toEqual(['a', 'b-new']);
  });

  it('첫 페이지 새로고침은 downstream snapshot을 제거한다', async () => {
    mockedRequest.mockResolvedValueOnce({ items: [rawMoin('a')], nextCursor: 'next' });
    const { result } = renderHook(() => useFeedPages('following'));
    await waitFor(() => expect(result.current.nextCursor).toBe('next'));
    mockedRequest.mockResolvedValueOnce({ items: [rawMoin('b')] });
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.items).toHaveLength(2));

    mockedRequest.mockResolvedValueOnce({ items: [rawMoin('fresh')] });
    await act(async () => { await result.current.reloadFirstPage(); });
    await waitFor(() => expect(result.current.items.map((item) => item.id)).toEqual(['fresh']));
  });
});
