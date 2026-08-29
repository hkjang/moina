import type { ReactNode } from 'react';
import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { useAuth } from './auth/AuthContext';
import { AppShell } from './components/AppShell';
import { rememberedRoute } from './config';
import { allNavigation, canAccessAdmin, hasPermission } from './navigation';
import LoginPage from './pages/LoginPage';
import FlowPage from './pages/FlowPage';
import MoinDetailPage from './pages/MoinDetailPage';
import { ExplorePage, PulsePage, SearchPage, TopicPage } from './pages/DiscoveryPages';
import NotificationsPage from './pages/NotificationsPage';
import PocketPage from './pages/PocketPage';
import { MoimDetailPage, MoimsPage } from './pages/MoimsPage';
import UserProfilePage from './pages/UserProfilePage';
import AIPage from './pages/AIPage';
import { AccessibilitySettingsPage, FeedSettingsPage, KeySettingsPage, ProfileSettingsPage, SecuritySettingsPage } from './pages/SettingsPages';
import { AdminAIPage, AdminApprovalsPage, AdminAuditPage, AdminContentPage, AdminOIDCPage, AdminOverviewPage, AdminReportsPage, AdminRolesPage, AdminSettingsPage, AdminUsersPage } from './pages/AdminPages';
import { AccessDeniedPage, NotFoundPage } from './pages/StatePages';

function LoadingScreen() { return <div className="app-loading" role="status"><img className="brand-symbol" src="/moina-mark.svg" alt=""/><span className="loading-ring"/><p>MOINA를 준비하고 있습니다.</p></div>; }
function Protected({ children }: { children: ReactNode }) { const { user, loading } = useAuth(); const location = useLocation(); if (loading) return <LoadingScreen/>; if (!user) return <Navigate to="/login" state={{ from: `${location.pathname}${location.search}` }} replace/>; return children; }
function Permission({ permission, children }: { permission: string; children: ReactNode }) { const { user } = useAuth(); return hasPermission(user?.permissions, permission) ? children : <AccessDeniedPage/>; }
function Admin({ permission, children }: { permission?: string; children: ReactNode }) { const { user } = useAuth(); return canAccessAdmin(user?.permissions) && hasPermission(user?.permissions, permission) ? children : <AccessDeniedPage/>; }
function RootRedirect() { const { user } = useAuth(); if (!user) return <Navigate to="/login" replace/>; const target = rememberedRoute(user.id); const pathname = target.split(/[?#]/, 1)[0]; const navigation = allNavigation.find((item) => item.path === pathname); const unavailable = target.startsWith('/admin') && !canAccessAdmin(user.permissions) || Boolean(navigation?.permission && !hasPermission(user.permissions, navigation.permission)); return <Navigate to={unavailable ? '/flow' : target} replace/>; }

export default function App() {
  return <Routes>
    <Route path="/login" element={<LoginPage/>}/>
    <Route element={<Protected><AppShell/></Protected>}>
      <Route index element={<RootRedirect/>}/>
      <Route path="flow" element={<FlowPage/>}/>
      <Route path="moin/:id" element={<MoinDetailPage/>}/>
      <Route path="explore" element={<ExplorePage/>}/>
      <Route path="search" element={<SearchPage/>}/>
      <Route path="pulse" element={<PulsePage/>}/>
      <Route path="topics/:slug" element={<TopicPage/>}/>
      <Route path="notifications" element={<NotificationsPage/>}/>
      <Route path="pocket" element={<PocketPage/>}/>
      <Route path="moims" element={<MoimsPage/>}/>
      <Route path="moims/:slug" element={<MoimDetailPage/>}/>
      <Route path="profile/:username" element={<UserProfilePage/>}/>
      <Route path="ai" element={<Permission permission="ai:use"><AIPage/></Permission>}/>
      <Route path="settings" element={<Navigate to="/settings/profile" replace/>}/>
      <Route path="settings/profile" element={<ProfileSettingsPage/>}/>
      <Route path="settings/feed" element={<FeedSettingsPage/>}/>
      <Route path="settings/accessibility" element={<AccessibilitySettingsPage/>}/>
      <Route path="settings/security" element={<SecuritySettingsPage/>}/>
      <Route path="settings/keys" element={<KeySettingsPage/>}/>
      <Route path="admin" element={<Admin><AdminOverviewPage/></Admin>}/>
      <Route path="admin/users" element={<Admin permission="users:manage"><AdminUsersPage/></Admin>}/>
      <Route path="admin/content" element={<Admin permission="posts:manage"><AdminContentPage/></Admin>}/>
      <Route path="admin/reports" element={<Admin permission="moderation:manage"><AdminReportsPage/></Admin>}/>
      <Route path="admin/approvals" element={<Admin permission="approvals:review"><AdminApprovalsPage/></Admin>}/>
      <Route path="admin/roles" element={<Admin permission="roles:manage"><AdminRolesPage/></Admin>}/>
      <Route path="admin/oidc" element={<Admin permission="settings:manage"><AdminOIDCPage/></Admin>}/>
      <Route path="admin/ai" element={<Admin permission="settings:manage"><AdminAIPage/></Admin>}/>
      <Route path="admin/settings" element={<Admin permission="settings:manage"><AdminSettingsPage/></Admin>}/>
      <Route path="admin/audit" element={<Admin permission="audit:read"><AdminAuditPage/></Admin>}/>
      <Route path="access-denied" element={<AccessDeniedPage/>}/>
      <Route path="*" element={<NotFoundPage/>}/>
    </Route>
  </Routes>;
}
