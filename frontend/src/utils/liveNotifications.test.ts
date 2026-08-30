import { afterEach, describe, expect, it, vi } from 'vitest';
import { dispatchLiveNotification, isLiveNotificationPayload, showDesktopNotification } from './liveNotifications';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('실시간 알림 전달', () => {
  it('연결 frame 또는 id/type이 있는 실제 알림만 protocol payload로 허용한다', () => {
    expect(isLiveNotificationPayload({ type: 'connected' })).toBe(true);
    expect(isLiveNotificationPayload({ id: 'n1', type: 'follow', inApp: true, unreadCount: 2 })).toBe(true);
    for (const invalid of [{}, [], 'notification', 1, { type: 'follow' }, { id: 'n1', type: 'follow', inApp: 'yes' }, { id: 'n1', type: 'follow', unreadCount: -1 }]) {
      expect(isLiveNotificationPayload(invalid)).toBe(false);
    }
  });

  it('각 WebSocket payload의 toast와 desktop 값을 독립적으로 즉시 적용한다', () => {
    const toast = vi.fn();
    const desktop = vi.fn();

    expect(dispatchLiveNotification({ id: 'one', title: '데스크톱만', toast: false, desktop: true }, { toast, desktop })).toBe(true);
    expect(toast).not.toHaveBeenCalled();
    expect(desktop).toHaveBeenCalledTimes(1);

    dispatchLiveNotification({ id: 'two', title: '토스트만', toast: true, desktop: false }, { toast, desktop });
    expect(toast).toHaveBeenCalledWith('토스트만');
    expect(desktop).toHaveBeenCalledTimes(1);
    expect(dispatchLiveNotification({ type: 'connected', toast: true, desktop: true }, { toast, desktop })).toBe(false);
  });

  it('알림 센터에서 숨긴 이벤트도 선택 채널로 전달하되 unread 증가는 요청하지 않는다', () => {
    const toast = vi.fn();
    const desktop = vi.fn();
    expect(dispatchLiveNotification({ id: 'hidden', title: '데스크톱 전달', inApp: false, toast: false, desktop: true }, { toast, desktop })).toBe(false);
    expect(toast).not.toHaveBeenCalled();
    expect(desktop).toHaveBeenCalledTimes(1);
  });

  it('권한이 허용되면 실제 데스크톱 알림을 만들고 클릭 시 내부 경로로 이동한다', () => {
    const instances: MockNotification[] = [];
    class MockNotification {
      static permission: NotificationPermission = 'granted';
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(public title: string, public options?: NotificationOptions) { instances.push(this); }
    }
    vi.stubGlobal('Notification', MockNotification as unknown as typeof Notification);
    vi.spyOn(window, 'focus').mockImplementation(() => undefined);
    const navigate = vi.fn();

    expect(showDesktopNotification({ id: 'n1', title: '새 Echo', body: '답글이 도착했습니다.', targetPath: '/moin/one' }, navigate)).toBe(true);
    expect(instances).toHaveLength(1);
    expect(instances[0].title).toBe('새 Echo');
    expect(instances[0].options).toMatchObject({ body: '답글이 도착했습니다.', icon: '/icon-192.png', tag: 'moina-n1' });
    instances[0].onclick?.();
    expect(window.focus).toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith('/moin/one');
    expect(instances[0].close).toHaveBeenCalled();
  });

  it('브라우저 권한이 없으면 데스크톱 알림을 만들지 않는다', () => {
    const constructor = vi.fn();
    class BlockedNotification {
      static permission: NotificationPermission = 'denied';
      constructor() { constructor(); }
    }
    vi.stubGlobal('Notification', BlockedNotification as unknown as typeof Notification);
    expect(showDesktopNotification({ desktop: true, title: '차단됨' }, vi.fn())).toBe(false);
    expect(constructor).not.toHaveBeenCalled();
  });

  it('OS 알림 생성 실패가 숨김 이벤트의 In App 정책을 바꾸지 않는다', () => {
    class ThrowingNotification {
      static permission: NotificationPermission = 'granted';
      constructor() { throw new Error('OS notification unavailable'); }
    }
    vi.stubGlobal('Notification', ThrowingNotification as unknown as typeof Notification);
    expect(showDesktopNotification({ id: 'hidden', desktop: true, inApp: false }, vi.fn())).toBe(false);
    expect(dispatchLiveNotification(
      { id: 'hidden', desktop: true, inApp: false },
      { toast: vi.fn(), desktop: () => { throw new Error('adapter failed'); } },
    )).toBe(false);
  });
});
