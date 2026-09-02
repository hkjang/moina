import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { formatDate, formatRelativeTime, listFrom, topicLabel } from './format';

const NOW = '2026-06-15T12:00:00.000Z';

describe('formatRelativeTime', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW));
  });
  afterEach(() => vi.useRealTimers());

  it('값이 없으면 방금 전으로 표시한다', () => expect(formatRelativeTime(undefined)).toBe('방금 전'));
  it('해석할 수 없는 값은 원본을 그대로 보여준다', () => expect(formatRelativeTime('언제인지 모름')).toBe('언제인지 모름'));
  it('서버와 시계가 어긋난 미래 시각도 방금 전으로 묶는다', () => expect(formatRelativeTime('2026-06-15T12:05:00.000Z')).toBe('방금 전'));
  it('분·시간·일 단위를 경계에서 바꾼다', () => {
    expect(formatRelativeTime('2026-06-15T11:59:30.000Z')).toBe('방금 전');
    expect(formatRelativeTime('2026-06-15T11:58:00.000Z')).toBe('2분 전');
    expect(formatRelativeTime('2026-06-15T09:00:00.000Z')).toBe('3시간 전');
    expect(formatRelativeTime('2026-06-11T12:00:00.000Z')).toBe('4일 전');
  });
  it('같은 해 7일 이전 Moin은 월·일만 표시한다', () => {
    const label = formatRelativeTime('2026-03-05T12:00:00.000Z');
    expect(label).toContain('3월');
    expect(label).not.toContain('년');
  });
  it('해가 다른 Moin은 연도를 함께 표시한다', () => {
    expect(formatRelativeTime('2025-06-10T12:00:00.000Z')).toContain('2025년');
  });
});

describe('formatDate', () => {
  it('값이 없으면 자리 표시자를 보여준다', () => expect(formatDate(undefined)).toBe('—'));
  it('해석할 수 없는 값은 원본을 그대로 보여준다', () => expect(formatDate('알 수 없음')).toBe('알 수 없음'));
  it('연도를 항상 포함하고 시각 표시는 선택할 수 있다', () => {
    expect(formatDate('2026-06-15T12:00:00.000Z')).toContain('2026년');
    expect(formatDate('2026-06-15T12:00:00.000Z')).toMatch(/\d{2}:\d{2}/);
    expect(formatDate('2026-06-15T12:00:00.000Z', false)).not.toMatch(/\d{2}:\d{2}/);
  });
});

describe('listFrom', () => {
  it('배열과 items 응답을 모두 목록으로 다룬다', () => {
    expect(listFrom([1, 2])).toEqual([1, 2]);
    expect(listFrom({ items: [3] })).toEqual([3]);
    expect(listFrom(undefined)).toEqual([]);
    expect(listFrom<number>({})).toEqual([]);
  });
});

describe('topicLabel', () => {
  it('앞의 # 중복 없이 하나만 붙인다', () => {
    expect(topicLabel('go')).toBe('#go');
    expect(topicLabel('#go')).toBe('#go');
    expect(topicLabel('##go')).toBe('#go');
  });
});
