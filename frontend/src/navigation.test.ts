import { describe, expect, it } from 'vitest';
import { adminNavigation, allNavigation, hasPermission, personalNavigation, primaryNavigation } from './navigation';

describe('한국어 내비게이션', () => {
  it('모든 메뉴는 중복 없는 절대 URL과 설명을 가진다', () => {
    const paths = allNavigation.map((item) => item.path);
    expect(new Set(paths).size).toBe(paths.length);
    expect(paths.every((path) => path.startsWith('/'))).toBe(true);
    expect(allNavigation.every((item) => item.label.trim() && item.description.trim())).toBe(true);
  });

  it('서비스 관리와 개인 설정을 별도 메뉴 집합으로 유지한다', () => {
    expect(primaryNavigation.some((item) => item.path === '/flow')).toBe(true);
    expect(personalNavigation.every((item) => item.path.startsWith('/settings/'))).toBe(true);
    expect(personalNavigation.some((item) => item.path === '/settings/notifications')).toBe(true);
    expect(adminNavigation.every((item) => item.admin && item.path.startsWith('/admin'))).toBe(true);
  });

  it('주요 화면의 빠른 이동 단축키가 중복되지 않는다', () => {
    const shortcuts = [...primaryNavigation, ...adminNavigation]
      .flatMap((item) => item.shortcut ? [item.shortcut] : []);
    expect(shortcuts.length).toBeGreaterThan(0);
    expect(new Set(shortcuts).size).toBe(shortcuts.length);
    expect(shortcuts.every((shortcut) => /^G [A-Z]$/.test(shortcut))).toBe(true);
  });

  it('정확한 권한, 전역 와일드카드와 도메인 와일드카드를 해석한다', () => {
    expect(hasPermission(['posts:manage'], 'posts:manage')).toBe(true);
    expect(hasPermission(['posts:*'], 'posts:manage')).toBe(true);
    expect(hasPermission(['*'], 'settings:manage')).toBe(true);
    expect(hasPermission(['posts:read'], 'posts:manage')).toBe(false);
  });
});
