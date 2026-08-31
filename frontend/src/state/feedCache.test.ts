import { describe, expect, it } from 'vitest';
import { feedCursorChain, mergeFeedPages, nextUnseenCursor, removeMoinFromFeed, replaceFeedPage, updateMoinInFeed, type FeedCursor, type FeedPageMap } from './feedCache';
import type { CursorPage, Moin } from '../types';

const moin = (id: string, content = id): Moin => ({ id, content, author: { id: 'user', username: 'user', displayName: '사용자' }, createdAt: '2026-01-01T00:00:00Z' });

describe('feed page cache', () => {
  it('첫 페이지 교체 시 이전 cursor snapshot을 제거한다', () => {
    const stale: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([[null, { items: [moin('a')], nextCursor: 'v1.next' }], ['v1.next', { items: [moin('b')] }]]);
    const refreshed = replaceFeedPage(stale, null, { items: [moin('fresh')] });
    expect([...refreshed.keys()]).toEqual([null]);
    expect(mergeFeedPages(refreshed).map((item) => item.id)).toEqual(['fresh']);
  });

  it('현재 cursor reload는 해당 page를 append하지 않고 교체한다', () => {
    let pages: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([[null, { items: [moin('a')], nextCursor: 'opaque' }]]);
    pages = replaceFeedPage(pages, 'opaque', { items: [moin('b-old')] });
    pages = replaceFeedPage(pages, 'opaque', { items: [moin('b-new')] });
    expect(mergeFeedPages(pages).map((item) => item.id)).toEqual(['a', 'b-new']);
  });

  it('page 경계의 같은 Moin ID는 최초 위치를 유지하고 최신 값으로 병합한다', () => {
    const pages: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([[null, { items: [moin('same', '이전'), moin('a')], nextCursor: 'next' }], ['next', { items: [moin('same', '최신'), moin('b')] }]]);
    const merged = mergeFeedPages(pages);
    expect(merged.map((item) => item.id)).toEqual(['same', 'a', 'b']);
    expect(merged[0].content).toBe('최신');
  });

  it('버전형 opaque cursor를 해석하거나 변형하지 않는다', () => {
    const cursor = 'v2.eyJ0IjoiMjAyNi0wMS0wMSIsImlkIjoicG9zdC8rPSJ9';
    const pages: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([[null, { items: [], nextCursor: cursor }]]);
    expect(nextUnseenCursor(pages, null)).toBe(cursor);
    const linked = replaceFeedPage(pages, cursor, { items: [] });
    expect(feedCursorChain(linked)).toEqual([null, cursor]);
  });

  it('cycle cursor는 한 번만 순회한다', () => {
    const pages: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([[null, { items: [moin('a')], nextCursor: 'cycle' }], ['cycle', { items: [moin('b')], nextCursor: 'cycle' }]]);
    expect(feedCursorChain(pages)).toEqual([null, 'cycle']);
    expect(mergeFeedPages(pages)).toHaveLength(2);
  });

  it('optimistic 변경은 같은 ID가 있는 page만 갱신한다', () => {
    const untouched = { items: [moin('other')] };
    const pages: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([[null, { items: [moin('target')], nextCursor: 'next' }], ['next', untouched]]);
    const changed = moin('target', '변경됨');
    const updated = updateMoinInFeed(pages, changed);
    expect(updated.get(null)?.items[0]).toBe(changed);
    expect(updated.get('next')).toBe(untouched);
  });

  it('삭제된 Moin은 모든 cursor page에서 제거한다', () => {
    const pages: FeedPageMap = new Map<FeedCursor, CursorPage<Moin>>([
      [null, { items: [moin('target'), moin('a')], nextCursor: 'next' }],
      ['next', { items: [moin('target'), moin('b')] }],
    ]);
    const removed = removeMoinFromFeed(pages, 'target');
    expect(mergeFeedPages(removed).map((item) => item.id)).toEqual(['a', 'b']);
  });
});
