import { describe, expect, it } from 'vitest';
import {
  normalizeVersion,
  recentlyVisitedRoutes,
  rememberedRoute,
  rememberRecentRoute,
  rememberRoute,
  safeAppPath,
} from './config';

describe('앱 경로와 버전 설정', () => {
  it('버전에는 항상 v 접두사를 한 번만 붙인다', () => {
    expect(normalizeVersion('0.1.0')).toBe('v0.1.0');
    expect(normalizeVersion('v2.3.4')).toBe('v2.3.4');
  });

  it('내부 URL만 새로고침 복원 경로로 허용한다', () => {
    expect(safeAppPath('/topics/go?sort=latest')).toBe('/topics/go?sort=latest');
    expect(safeAppPath('//evil.example')).toBe('/flow');
    expect(safeAppPath('/%2e%2e/admin')).toBe('/flow');
    expect(safeAppPath('/login')).toBe('/flow');
  });

  it('사용자별 마지막 메뉴를 분리해 기억한다', () => {
    rememberRoute('route-test-user-a', '/settings/accessibility');
    rememberRoute('route-test-user-b', '/admin/users?page=2');
    expect(rememberedRoute('route-test-user-a')).toBe('/settings/accessibility');
    expect(rememberedRoute('route-test-user-b')).toBe('/admin/users?page=2');
  });

  it('빠른 이동용 최근 경로를 사용자별·최신순으로 중복 없이 기억한다', () => {
    const userId = 'recent-route-test-user';
    rememberRecentRoute(userId, '/flow');
    rememberRecentRoute(userId, '/moims/developers');
    rememberRecentRoute(userId, '/flow?compose=1&mode=following');
    rememberRecentRoute(userId, '/search?q=first&type=posts');
    rememberRecentRoute(userId, '/search?q=latest&type=users');
    rememberRecentRoute(userId, '//outside.example');

    expect(recentlyVisitedRoutes(userId)).toEqual([
      '/search?q=latest&type=users',
      '/flow?mode=following',
      '/moims/developers',
    ]);
  });

  it('손상되거나 위험한 최근 경로 저장값을 복원하지 않는다', () => {
    const userId = 'unsafe-recent-route-test-user';
    window.localStorage.setItem(`moina.recent-routes.${userId}`, JSON.stringify([
      '/topics/react',
      '/%2e%2e/admin',
      '/login',
      '/topics/react',
    ]));
    expect(recentlyVisitedRoutes(userId)).toEqual(['/topics/react']);

    window.localStorage.setItem(`moina.recent-routes.${userId}`, '{broken');
    expect(recentlyVisitedRoutes(userId)).toEqual([]);
  });
});
