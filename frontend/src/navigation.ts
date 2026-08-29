import type { LucideIcon } from 'lucide-react';
import {
  Activity, Bell, Bookmark, Bot, CircleGauge, Compass, FileCheck2, Flag, KeyRound,
  LockKeyhole, MessageCircleMore, Network, Settings2, ShieldCheck, Sparkles, Users,
  UserRoundCog, Waves, Search, ScrollText,
} from 'lucide-react';

export interface NavItem {
  label: string;
  path: string;
  icon: LucideIcon;
  description: string;
  permission?: string;
  admin?: boolean;
  approvalOnly?: boolean;
}

export const primaryNavigation: NavItem[] = [
  { label: '플로우', path: '/flow', icon: Waves, description: '나를 위한 피드와 팔로잉 피드' },
  { label: '탐색', path: '/explore', icon: Compass, description: '새로운 사람과 관심사 발견' },
  { label: '검색', path: '/search', icon: Search, description: '모인, 사람, 토픽 통합 검색' },
  { label: '펄스', path: '/pulse', icon: Activity, description: '지금 주목받는 주제' },
  { label: '알림', path: '/notifications', icon: Bell, description: '내 활동과 실시간 알림' },
  { label: '포켓', path: '/pocket', icon: Bookmark, description: '나중에 볼 모인' },
  { label: '모임', path: '/moims', icon: Network, description: '관심사 기반 커뮤니티' },
  { label: 'AI', path: '/ai', icon: Sparkles, description: '스트리밍 AI 어시스턴트', permission: 'ai:use' },
];

export const personalNavigation: NavItem[] = [
  { label: '프로필 설정', path: '/settings/profile', icon: UserRoundCog, description: '내 프로필과 계정' },
  { label: '피드 개인화', path: '/settings/feed', icon: CircleGauge, description: 'For Me 추천 비율' },
  { label: '화면 및 접근성', path: '/settings/accessibility', icon: Settings2, description: '글자 크기와 화면 설정' },
  { label: '로그인 보안', path: '/settings/security', icon: LockKeyhole, description: '비밀번호와 세션' },
  { label: '내 API·MCP 키', path: '/settings/keys', icon: KeyRound, description: '개인 키 권한과 회전' },
];

export const adminNavigation: NavItem[] = [
  { label: '관리 대시보드', path: '/admin', icon: CircleGauge, description: '서비스 운영 현황', admin: true, permission: 'admin:access' },
  { label: '사용자 관리', path: '/admin/users', icon: Users, description: '사용자 상태와 역할', admin: true, permission: 'users:manage' },
  { label: '콘텐츠 관리', path: '/admin/content', icon: MessageCircleMore, description: '모인과 모임 운영', admin: true, permission: 'posts:manage' },
  { label: '신고·제재', path: '/admin/reports', icon: Flag, description: '신고 검토와 제재', admin: true, permission: 'moderation:manage' },
  { label: '검토·승인', path: '/admin/approvals', icon: FileCheck2, description: '승인 또는 반려', admin: true, permission: 'approvals:review', approvalOnly: true },
  { label: '역할·권한', path: '/admin/roles', icon: ShieldCheck, description: '변경 가능한 권한 정책', admin: true, permission: 'roles:manage' },
  { label: 'Keycloak OIDC', path: '/admin/oidc', icon: LockKeyhole, description: 'SSO 자동 연결', admin: true, permission: 'settings:manage' },
  { label: 'AI 설정', path: '/admin/ai', icon: Bot, description: '모델과 스트리밍 정책', admin: true, permission: 'settings:manage' },
  { label: '일반 설정', path: '/admin/settings', icon: Settings2, description: '서비스와 API·MCP 정책', admin: true, permission: 'settings:manage' },
  { label: '감사 로그', path: '/admin/audit', icon: ScrollText, description: '관리 활동 추적', admin: true, permission: 'audit:read' },
];

export const allNavigation = [...primaryNavigation, ...personalNavigation, ...adminNavigation];

export function hasPermission(permissions: string[] | undefined, required: string | undefined) {
  if (!required) return true;
  if (!permissions) return false;
  if (permissions.includes('*') || permissions.includes(required)) return true;
  const domain = required.split(':', 1)[0];
  return permissions.includes(`${domain}:*`);
}

export function canAccessAdmin(permissions: string[] | undefined) {
  return hasPermission(permissions, 'admin:access');
}
