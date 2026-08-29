import { describe, expect, it } from 'vitest';
import { toggleMoinBookmark, toggleMoinRemoin, toggleMoinSignal } from './moinMutations';
import type { Moin } from '../types';

const original = (): Moin => ({ id: 'm1', content: '테스트', author: { id: 'u1', username: 'user', displayName: '사용자' }, createdAt: '2026-01-01T00:00:00Z', counts: { bookmarks: 2, remoins: 3, signals: { like: 4 } }, viewer: { bookmarked: false, remoined: false, signals: [] } });

describe('optimistic Moin mutation', () => {
  it('Signal을 즉시 추가하고 count를 올린다', () => {
    const before = original();
    const after = toggleMoinSignal(before, 'like');
    expect(after.viewer?.signals).toEqual(['like']);
    expect(after.counts?.signals?.like).toBe(5);
    expect(before.viewer?.signals).toEqual([]);
  });

  it('활성 Signal을 제거하되 count는 음수가 되지 않는다', () => {
    const before = { ...original(), counts: { signals: { like: 0 } }, viewer: { signals: ['like' as const] } };
    const after = toggleMoinSignal(before, 'like');
    expect(after.viewer?.signals).toEqual([]);
    expect(after.counts?.signals?.like).toBe(0);
  });

  it('Pocket 상태와 count를 함께 토글한다', () => {
    const added = toggleMoinBookmark(original());
    expect(added.viewer?.bookmarked).toBe(true);
    expect(added.counts?.bookmarks).toBe(3);
    const removed = toggleMoinBookmark(added);
    expect(removed.viewer?.bookmarked).toBe(false);
    expect(removed.counts?.bookmarks).toBe(2);
  });

  it('Remoin 상태와 count를 함께 토글한다', () => {
    const added = toggleMoinRemoin(original());
    expect(added.viewer?.remoined).toBe(true);
    expect(added.counts?.remoins).toBe(4);
    const removed = toggleMoinRemoin(added);
    expect(removed.viewer?.remoined).toBe(false);
    expect(removed.counts?.remoins).toBe(3);
  });
});
