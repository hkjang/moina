// 빈 DB에서도 열 수 있는 정적 route-level 화면의 단일 E2E catalog입니다.
// 데이터가 필요한 상세 route는 capture-pages.mjs가 정상 API seed 후 추가합니다.
export const appRoutes = [
  { slug: 'flow', path: '/flow', title: '플로우' },
  { slug: 'explore', path: '/explore', title: '탐색' },
  { slug: 'search', path: '/search', title: '검색' },
  { slug: 'pulse', path: '/pulse', title: '펄스' },
  { slug: 'notifications', path: '/notifications', title: '알림' },
  { slug: 'pocket', path: '/pocket', title: '포켓' },
  { slug: 'moims', path: '/moims', title: '모임' },
  { slug: 'ai', path: '/ai', title: 'AI' },
  { slug: 'settings-profile', path: '/settings/profile', title: '프로필 설정' },
  { slug: 'settings-feed', path: '/settings/feed', title: '피드 개인화' },
  { slug: 'settings-accessibility', path: '/settings/accessibility', title: '화면 및 접근성' },
  { slug: 'settings-security', path: '/settings/security', title: '로그인 보안' },
  { slug: 'settings-keys', path: '/settings/keys', title: '내 API·MCP 키' },
  { slug: 'admin-dashboard', path: '/admin', title: '관리 대시보드', admin: true },
  { slug: 'admin-users', path: '/admin/users', title: '사용자 관리', admin: true },
  { slug: 'admin-content', path: '/admin/content', title: '콘텐츠 관리', admin: true },
  { slug: 'admin-reports', path: '/admin/reports', title: '신고·제재', admin: true },
  { slug: 'admin-approvals', path: '/admin/approvals', title: '검토·승인', admin: true, approvalOnly: true },
  { slug: 'admin-roles', path: '/admin/roles', title: '역할·권한', admin: true },
  { slug: 'admin-oidc', path: '/admin/oidc', title: 'Keycloak OIDC', admin: true },
  { slug: 'admin-ai', path: '/admin/ai', title: 'AI 설정', admin: true },
  { slug: 'admin-settings', path: '/admin/settings', title: '일반 설정', admin: true },
  { slug: 'admin-audit', path: '/admin/audit', title: '감사 로그', admin: true },
];

export const stateRoutes = [
  { slug: 'access-denied', path: '/access-denied', title: '접근 권한이 없습니다', state: true },
  { slug: 'not-found', path: '/e2e-page-does-not-exist', title: '페이지를 찾을 수 없습니다', state: true },
];

export const captureRoutes = [...appRoutes, ...stateRoutes];

export function routeCatalogFromEnvironment() {
  const value = process.env.MOINA_E2E_ROUTES_JSON?.trim();
  if (!value) return captureRoutes;
  const parsed = JSON.parse(value);
  if (!Array.isArray(parsed) || parsed.length === 0) throw new Error('MOINA_E2E_ROUTES_JSON은 비어 있지 않은 배열이어야 합니다.');
  return parsed.map((route) => {
    if (!route || typeof route.slug !== 'string' || typeof route.path !== 'string' || !route.path.startsWith('/') || typeof route.title !== 'string') {
      throw new Error('각 E2E route에는 slug/path/title이 필요합니다.');
    }
    return route;
  });
}
