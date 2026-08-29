import { describe, expect, it } from 'vitest';
import { normalizeVersion, rememberedRoute, rememberRoute, safeAppPath } from './config';

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
});
