import { lazy, Suspense, type ReactNode } from 'react';
import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { useAuth } from './auth/AuthContext';
import { rememberedRoute } from './config';
import { allNavigation, canAccessAdmin, hasPermission } from './navigation';

const AppShell = lazy(() => import('./components/AppShell').then((module) => ({ default: module.AppShell })));
const LoginPage = lazy(() => import('./pages/LoginPage'));
const FlowPage = lazy(() => import('./pages/FlowPage'));
const MoinDetailPage = lazy(() => import('./pages/MoinDetailPage'));
const ExplorePage = lazy(() => import('./pages/DiscoveryPages').then((module) => ({ default: module.ExplorePage })));
const PulsePage = lazy(() => import('./pages/DiscoveryPages').then((module) => ({ default: module.PulsePage })));
const SearchPage = lazy(() => import('./pages/DiscoveryPages').then((module) => ({ default: module.SearchPage })));
const TopicPage = lazy(() => import('./pages/DiscoveryPages').then((module) => ({ default: module.TopicPage })));
const NotificationsPage = lazy(() => import('./pages/NotificationsPage'));
const PocketPage = lazy(() => import('./pages/PocketPage'));
const MoimDetailPage = lazy(() => import('./pages/MoimsPage').then((module) => ({ default: module.MoimDetailPage })));
const MoimsPage = lazy(() => import('./pages/MoimsPage').then((module) => ({ default: module.MoimsPage })));
const UserProfilePage = lazy(() => import('./pages/UserProfilePage'));
const AIPage = lazy(() => import('./pages/AIPage'));
const AccessibilitySettingsPage = lazy(() => import('./pages/SettingsPages').then((module) => ({ default: module.AccessibilitySettingsPage })));
const FeedSettingsPage = lazy(() => import('./pages/SettingsPages').then((module) => ({ default: module.FeedSettingsPage })));
const KeySettingsPage = lazy(() => import('./pages/SettingsPages').then((module) => ({ default: module.KeySettingsPage })));
const NotificationSettingsPage = lazy(() => import('./pages/SettingsPages').then((module) => ({ default: module.NotificationSettingsPage })));
const ProfileSettingsPage = lazy(() => import('./pages/SettingsPages').then((module) => ({ default: module.ProfileSettingsPage })));
const SecuritySettingsPage = lazy(() => import('./pages/SettingsPages').then((module) => ({ default: module.SecuritySettingsPage })));
const AdminAIPage = lazy(() => import('./pages/admin/AdminAIPage').then((module) => ({ default: module.AdminAIPage })));
const AdminApprovalsPage = lazy(() => import('./pages/admin/AdminApprovalsPage').then((module) => ({ default: module.AdminApprovalsPage })));
const AdminAuditPage = lazy(() => import('./pages/admin/AdminAuditPage').then((module) => ({ default: module.AdminAuditPage })));
const AdminContentPage = lazy(() => import('./pages/admin/AdminContentPage').then((module) => ({ default: module.AdminContentPage })));
const AdminOIDCPage = lazy(() => import('./pages/admin/AdminOIDCPage').then((module) => ({ default: module.AdminOIDCPage })));
const AdminSMTPPage = lazy(() => import('./pages/admin/AdminSMTPPage').then((module) => ({ default: module.AdminSMTPPage })));
const AdminOverviewPage = lazy(() => import('./pages/admin/AdminOverviewPage').then((module) => ({ default: module.AdminOverviewPage })));
const AdminReportsPage = lazy(() => import('./pages/admin/AdminReportsPage').then((module) => ({ default: module.AdminReportsPage })));
const AdminRolesPage = lazy(() => import('./pages/admin/AdminRolesPage').then((module) => ({ default: module.AdminRolesPage })));
const AdminSettingsPage = lazy(() => import('./pages/admin/AdminSettingsPage').then((module) => ({ default: module.AdminSettingsPage })));
const AdminUsersPage = lazy(() => import('./pages/admin/AdminUsersPage').then((module) => ({ default: module.AdminUsersPage })));
const AccessDeniedPage = lazy(() => import('./pages/StatePages').then((module) => ({ default: module.AccessDeniedPage })));
const NotFoundPage = lazy(() => import('./pages/StatePages').then((module) => ({ default: module.NotFoundPage })));

function LoadingScreen() { return <div className="app-loading" role="status"><img className="brand-symbol" src="/moina-mark.svg" alt=""/><span className="loading-ring"/><p>MOINA를 준비하고 있습니다.</p></div>; }
function Protected({ children }: { children: ReactNode }) { const { user, loading } = useAuth(); const location = useLocation(); if (loading) return <LoadingScreen/>; if (!user) return <Navigate to="/login" state={{ from: `${location.pathname}${location.search}` }} replace/>; return children; }
function Permission({ permission, children }: { permission: string; children: ReactNode }) { const { user } = useAuth(); return hasPermission(user?.permissions, permission) ? children : <AccessDeniedPage/>; }
function Admin({ permission, children }: { permission?: string; children: ReactNode }) { const { user } = useAuth(); return canAccessAdmin(user?.permissions) && hasPermission(user?.permissions, permission) ? children : <AccessDeniedPage/>; }
function RootRedirect() { const { user } = useAuth(); if (!user) return <Navigate to="/login" replace/>; const target = rememberedRoute(user.id); const pathname = target.split(/[?#]/, 1)[0]; const navigation = allNavigation.find((item) => item.path === pathname); const unavailable = target.startsWith('/admin') && !canAccessAdmin(user.permissions) || Boolean(navigation?.permission && !hasPermission(user.permissions, navigation.permission)); return <Navigate to={unavailable ? '/flow' : target} replace/>; }

export default function App() {
  return <Suspense fallback={<LoadingScreen/>}><Routes>
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
      <Route path="settings/notifications" element={<NotificationSettingsPage/>}/>
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
      <Route path="admin/smtp" element={<Admin permission="settings:manage"><AdminSMTPPage/></Admin>}/>
      <Route path="admin/ai" element={<Admin permission="settings:manage"><AdminAIPage/></Admin>}/>
      <Route path="admin/settings" element={<Admin permission="settings:manage"><AdminSettingsPage/></Admin>}/>
      <Route path="admin/audit" element={<Admin permission="audit:read"><AdminAuditPage/></Admin>}/>
      <Route path="access-denied" element={<AccessDeniedPage/>}/>
      <Route path="*" element={<NotFoundPage/>}/>
    </Route>
  </Routes></Suspense>;
}
