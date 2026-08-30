export interface LiveNotificationPayload {
  id?: string;
  type?: string;
  title?: string;
  body?: string;
  targetPath?: string;
  unreadCount?: number;
  inApp?: boolean;
  toast?: boolean;
  desktop?: boolean;
}

interface LiveNotificationHandlers {
  toast: (message: string) => void;
  desktop: (payload: LiveNotificationPayload) => void;
}

export function isLiveNotificationPayload(value: unknown): value is LiveNotificationPayload {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  if (record.type === 'connected') return true;
  if (typeof record.id !== 'string' || !record.id.trim() || typeof record.type !== 'string' || !record.type.trim()) return false;
  for (const key of ['title', 'body', 'targetPath'] as const) {
    if (record[key] !== undefined && typeof record[key] !== 'string') return false;
  }
  for (const key of ['inApp', 'toast', 'desktop'] as const) {
    if (record[key] !== undefined && typeof record[key] !== 'boolean') return false;
  }
  if (record.unreadCount !== undefined && (typeof record.unreadCount !== 'number' || !Number.isSafeInteger(record.unreadCount) || record.unreadCount < 0)) return false;
  return true;
}

export function dispatchLiveNotification(payload: LiveNotificationPayload, handlers: LiveNotificationHandlers) {
  if (payload.type === 'connected') return false;
  // OS notification and toast adapters are best-effort channels. Their
  // failure must not change the durable In App/unread policy of this event.
  if (payload.toast) {
    try { handlers.toast(payload.title || '새 알림이 도착했습니다.'); } catch { /* channel unavailable */ }
  }
  if (payload.desktop) {
    try { handlers.desktop(payload); } catch { /* channel unavailable */ }
  }
  return payload.inApp !== false;
}

export function showDesktopNotification(payload: LiveNotificationPayload, navigate: (path: string) => void) {
  if (typeof window === 'undefined' || !('Notification' in window) || window.Notification.permission !== 'granted') return false;
  try {
    const notification = new window.Notification(payload.title || 'MOINA 새 알림', {
      body: payload.body,
      icon: '/icon-192.png',
      tag: payload.id ? `moina-${payload.id}` : undefined,
    });
    notification.onclick = () => {
      window.focus();
      if (payload.targetPath?.startsWith('/')) navigate(payload.targetPath);
      notification.close();
    };
    return true;
  } catch {
    return false;
  }
}
