import { describe, expect, it } from 'vitest';
import { mergePocketMoin } from './PocketPage';

describe('mergePocketMoin', () => {
  const saved = { id: 'm1', viewer: { bookmarked: true } };

  it('optimistic 삭제 실패 rollback에서 사라진 항목을 복구한다', () => {
    const removed = mergePocketMoin([saved], { ...saved, viewer: { bookmarked: false } });
    expect(removed).toEqual([]);
    expect(mergePocketMoin(removed, saved)).toEqual([saved]);
  });
});
