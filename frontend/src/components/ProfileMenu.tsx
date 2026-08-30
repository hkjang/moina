import * as DropdownMenu from '@radix-ui/react-dropdown-menu';
import * as ScrollArea from '@radix-ui/react-scroll-area';
import { Bell, ChevronDown, KeyRound, LogOut, Settings2, ShieldCheck, UserRound } from 'lucide-react';
import { useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';
import { APP_NAME, APP_VERSION, normalizeVersion } from '../config';
import { useApiQuery } from '../hooks/useApiQuery';
import { canAccessAdmin } from '../navigation';
import { Avatar } from './ui';

export function ProfileMenu() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const version = useApiQuery<{ version?: string } | string>('/version');
  const resolvedVersion = normalizeVersion(typeof version.data === 'string' ? version.data : version.data?.version || APP_VERSION);
  const go = (path: string) => navigate(path);
  return <DropdownMenu.Root>
    <DropdownMenu.Trigger className="profile-trigger" aria-label="프로필 메뉴">
      <Avatar name={user?.displayName || '사용자'} src={user?.avatarUrl}/>
      <span className="profile-trigger-copy"><strong>{user?.displayName}</strong><small>@{user?.username}</small></span>
      <ChevronDown size={18} aria-hidden="true"/>
    </DropdownMenu.Trigger>
    <DropdownMenu.Portal>
      <DropdownMenu.Content className="profile-popover" sideOffset={8} align="end">
        <div className="profile-summary"><Avatar name={user?.displayName || '사용자'} src={user?.avatarUrl} size="large"/><span><strong>{user?.displayName}</strong><small>{user?.email || `@${user?.username}`}</small></span></div>
        <ScrollArea.Root className="profile-menu-scroll custom-scrollbar">
          <ScrollArea.Viewport className="profile-menu-viewport">
            <DropdownMenu.Group>
              <DropdownMenu.Item className="profile-menu-item" onSelect={() => go(`/profile/${encodeURIComponent(user?.username || '')}`)}><UserRound/>내 프로필</DropdownMenu.Item>
              <DropdownMenu.Item className="profile-menu-item" onSelect={() => go('/settings/profile')}><Settings2/>개인화 설정</DropdownMenu.Item>
              <DropdownMenu.Item className="profile-menu-item" onSelect={() => go('/settings/notifications')}><Bell/>알림 개인화</DropdownMenu.Item>
              <DropdownMenu.Item className="profile-menu-item" onSelect={() => go('/settings/keys')}><KeyRound/>내 API·MCP 키</DropdownMenu.Item>
              {canAccessAdmin(user?.permissions) && <DropdownMenu.Item className="profile-menu-item" onSelect={() => go('/admin')}><ShieldCheck/>서비스 관리자</DropdownMenu.Item>}
            </DropdownMenu.Group>
          </ScrollArea.Viewport>
          <ScrollArea.Scrollbar orientation="vertical" className="radix-scrollbar"><ScrollArea.Thumb className="radix-scroll-thumb"/></ScrollArea.Scrollbar>
        </ScrollArea.Root>
        <div className="profile-version"><span>서비스 버전</span><strong>{APP_NAME} {resolvedVersion}</strong></div>
        <DropdownMenu.Separator className="menu-separator"/>
        <DropdownMenu.Item className="profile-menu-item danger" onSelect={() => void logout()}><LogOut/>로그아웃</DropdownMenu.Item>
        <DropdownMenu.Arrow className="profile-arrow"/>
      </DropdownMenu.Content>
    </DropdownMenu.Portal>
  </DropdownMenu.Root>;
}
