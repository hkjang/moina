import { beforeEach, describe, expect, it } from 'vitest';
import {
  clearApiQueryCache,
  MAX_QUERY_CACHE_ENTRIES,
  readApiQueryCache,
  writeApiQueryCache,
} from './apiQueryClient';

describe('apiQuery 캐시 상한', () => {
  beforeEach(() => clearApiQueryCache({ abort: true }));

  it('상한을 넘으면 가장 오래 쓰지 않은 항목부터 버린다', () => {
    // Cursor pages and search terms create a new key every time and are never
    // read again, so without a bound the map grows for the life of the tab.
    for (let index = 0; index < MAX_QUERY_CACHE_ENTRIES + 10; index += 1) {
      writeApiQueryCache(`/feed?cursor=${index}`, { index }, [`/feed?cursor=${index}`]);
    }
    expect(readApiQueryCache('/feed?cursor=0').status).toBe('miss');
    expect(readApiQueryCache('/feed?cursor=9').status).toBe('miss');
    expect(readApiQueryCache(`/feed?cursor=${MAX_QUERY_CACHE_ENTRIES + 9}`).status).toBe('fresh');
  });

  it('읽은 항목은 최근 사용으로 올라가 먼저 버려지지 않는다', () => {
    writeApiQueryCache('/keep-me', { kept: true }, ['/keep-me']);
    for (let index = 0; index < MAX_QUERY_CACHE_ENTRIES - 1; index += 1) {
      writeApiQueryCache(`/filler-${index}`, { index }, [`/filler-${index}`]);
    }
    // Touch the oldest entry; it must survive the writes that follow.
    expect(readApiQueryCache('/keep-me').status).toBe('fresh');
    for (let index = 0; index < 10; index += 1) {
      writeApiQueryCache(`/later-${index}`, { index }, [`/later-${index}`]);
    }
    expect(readApiQueryCache('/keep-me').status).toBe('fresh');
    expect(readApiQueryCache('/filler-0').status).toBe('miss');
  });

  it('덮어쓴 항목이 중복으로 자리를 차지하지 않는다', () => {
    for (let index = 0; index < 20; index += 1) {
      writeApiQueryCache('/same-path', { index }, ['/same-path']);
    }
    for (let index = 0; index < MAX_QUERY_CACHE_ENTRIES - 1; index += 1) {
      writeApiQueryCache(`/other-${index}`, { index }, [`/other-${index}`]);
    }
    const entry = readApiQueryCache<{ index: number }>('/same-path');
    expect(entry.status).toBe('fresh');
    expect(entry.status !== 'miss' && entry.data.index).toBe(19);
  });

  it('만료된 항목은 상한과 무관하게 읽을 때 사라진다', () => {
    const now = Date.now();
    writeApiQueryCache('/stale', { value: 1 }, ['/stale'], now);
    expect(readApiQueryCache('/stale', 1_000, 1_000, now + 5_000).status).toBe('miss');
    expect(readApiQueryCache('/stale').status).toBe('miss');
  });
});
