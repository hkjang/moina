import type { UserPreferences } from '../types';

export const DEFAULT_PREFERENCES = {
  appearance: { theme: 'system', fontScale: 112, reduceMotion: false, density: 'comfortable' },
  feed: { topicWeight: 40, linkWeight: 30, discoveryWeight: 20, recencyWeight: 10, excludedTopics: [], showReasons: true },
  notifications: { desktop: true, mentions: true, signals: true, follows: true },
} satisfies Required<Pick<UserPreferences, 'appearance' | 'feed' | 'notifications'>>;

export function mergePreferences(preferences?: UserPreferences): UserPreferences {
  return {
    appearance: { ...DEFAULT_PREFERENCES.appearance, ...preferences?.appearance },
    feed: { ...DEFAULT_PREFERENCES.feed, ...preferences?.feed },
    notifications: { ...DEFAULT_PREFERENCES.notifications, ...preferences?.notifications },
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
