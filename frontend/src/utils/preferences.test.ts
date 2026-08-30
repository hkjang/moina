import { describe, expect, it } from 'vitest';
import { DEFAULT_PREFERENCES, mergePreferences } from './preferences';

describe('mergePreferences', () => {
  it('중첩된 알림 부분값을 기본값과 깊게 병합한다', () => {
    const merged = mergePreferences({
      notifications: {
        inApp: { mentions: false },
        desktop: { enabled: true },
        quietHours: { enabled: true, start: '23:30' },
      },
    });

    expect(merged.notifications).toEqual({
      inApp: { mentions: false, signals: true, follows: true, echoes: true, approvals: true },
      toast: { enabled: true },
      desktop: { enabled: true },
      digest: { mode: 'off', time: '08:00' },
      quietHours: { enabled: true, start: '23:30', end: '07:00' },
    });
  });

  it('필수 승인 알림을 유지하고 배열 기본값을 공유하지 않는다', () => {
    const first = mergePreferences({ notifications: { inApp: { approvals: false } } });
    const second = mergePreferences();
    first.feed.excludedTopics.push('go');

    expect(first.notifications.inApp.approvals).toBe(true);
    expect(second.feed.excludedTopics).toEqual([]);
    expect(DEFAULT_PREFERENCES.feed.excludedTopics).toEqual([]);
  });
});
