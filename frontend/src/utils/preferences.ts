import type { UserPreferences } from '../types';

type RequiredSection<T> = { [Key in keyof T]-?: NonNullable<T[Key]> };
type NotificationPreferences = NonNullable<UserPreferences['notifications']>;

export interface ResolvedUserPreferences {
  appearance: RequiredSection<NonNullable<UserPreferences['appearance']>>;
  feed: RequiredSection<NonNullable<UserPreferences['feed']>>;
  notifications: {
    inApp: RequiredSection<NonNullable<NotificationPreferences['inApp']>>;
    toast: RequiredSection<NonNullable<NotificationPreferences['toast']>>;
    desktop: RequiredSection<NonNullable<NotificationPreferences['desktop']>>;
    email: RequiredSection<NonNullable<NotificationPreferences['email']>>;
    digest: RequiredSection<NonNullable<NotificationPreferences['digest']>>;
    quietHours: RequiredSection<NonNullable<NotificationPreferences['quietHours']>>;
  };
}

export const DEFAULT_PREFERENCES: ResolvedUserPreferences = {
  appearance: { theme: 'system', fontScale: 112, reduceMotion: false, density: 'comfortable' },
  feed: { mode: 'for_me', topicWeight: 40, linkWeight: 30, discoveryWeight: 20, recencyWeight: 10, excludedTopics: [], showReasons: true },
  notifications: {
    inApp: { mentions: true, signals: true, follows: true, echoes: true, approvals: true },
    toast: { enabled: true },
    desktop: { enabled: false },
    email: { enabled: false },
    digest: { mode: 'off', time: '08:00' },
    quietHours: { enabled: false, start: '22:00', end: '07:00' },
  },
};

export function mergePreferences(preferences?: UserPreferences): ResolvedUserPreferences {
  return {
    appearance: { ...DEFAULT_PREFERENCES.appearance, ...preferences?.appearance },
    feed: {
      ...DEFAULT_PREFERENCES.feed,
      ...preferences?.feed,
      excludedTopics: [...(preferences?.feed?.excludedTopics ?? DEFAULT_PREFERENCES.feed.excludedTopics)],
    },
    notifications: {
      inApp: {
        ...DEFAULT_PREFERENCES.notifications.inApp,
        ...preferences?.notifications?.inApp,
        approvals: true,
      },
      toast: { ...DEFAULT_PREFERENCES.notifications.toast, ...preferences?.notifications?.toast },
      desktop: { ...DEFAULT_PREFERENCES.notifications.desktop, ...preferences?.notifications?.desktop },
      email: { ...DEFAULT_PREFERENCES.notifications.email, ...preferences?.notifications?.email },
      digest: { ...DEFAULT_PREFERENCES.notifications.digest, ...preferences?.notifications?.digest },
      quietHours: { ...DEFAULT_PREFERENCES.notifications.quietHours, ...preferences?.notifications?.quietHours },
    },
  };
}

export function applyAppearance(preferences?: UserPreferences) {
  const appearance = mergePreferences(preferences).appearance!;
  const root = document.documentElement;
  root.style.fontSize = `${appearance.fontScale}%`;
  root.dataset.theme = appearance.theme;
  root.dataset.reduceMotion = appearance.reduceMotion ? 'true' : 'false';
  root.dataset.density = appearance.density;
}
