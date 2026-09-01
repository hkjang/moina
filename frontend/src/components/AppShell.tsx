import * as Dialog from "@radix-ui/react-dialog";
import * as ScrollArea from "@radix-ui/react-scroll-area";
import {
  Bell,
  Menu,
  PenLine,
  Search,
  ShieldCheck,
  TrendingUp,
  UserPlus,
  Waves,
  X,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  Link,
  NavLink,
  Outlet,
  useLocation,
  useNavigate,
} from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { APP_DISPLAY_NAME, rememberRecentRoute, rememberRoute } from "../config";
import { useApiQuery } from "../hooks/useApiQuery";
import {
  adminNavigation,
  canAccessAdmin,
  hasPermission,
  primaryNavigation,
} from "../navigation";
import type {
  CursorPage,
  ServiceCapabilities,
  Topic,
  UserPreferences,
} from "../types";
import { websocketURL } from "../api/client";
import { cn } from "../lib/cn";
import { Avatar, Badge, Button, IconButton } from "./ui";
import { ProfileMenu } from "./ProfileMenu";
import { QuickNavigation } from "./QuickNavigation";
import { useToast } from "./ToastProvider";
import { topicLabel } from "../utils/format";
import {
  dispatchLiveNotification,
  isLiveNotificationPayload,
  showDesktopNotification,
  type LiveNotificationPayload,
} from "../utils/liveNotifications";
import { applyAppearance } from "../utils/preferences";

function Brand() {
  return (
    <Link to="/flow" className="brand-link" aria-label="MOINA 플로우">
      <img className="brand-symbol" src="/moina-mark.svg" alt="" />
      <span>
        <strong>{APP_DISPLAY_NAME}</strong>
        <small>Social Knowledge Network</small>
      </span>
    </Link>
  );
}

function NavList({
  approvalVisible,
  onSelect,
}: {
  approvalVisible: boolean;
  onSelect?: () => void;
}) {
  const { user } = useAuth();
  const visible = (items: typeof primaryNavigation) =>
    items.filter(
      (item) =>
        hasPermission(user?.permissions, item.permission) &&
        (!item.approvalOnly || approvalVisible),
    );
  return (
    <nav className="primary-nav" aria-label="주 메뉴">
      <div className="nav-section">
        {visible(primaryNavigation).map((item) => (
          <NavLink
            to={item.path}
            key={item.path}
            onClick={onSelect}
            className={({ isActive }) => cn("nav-link", isActive && "active")}
            title={item.description}
          >
            <item.icon />
            <span>{item.label}</span>
          </NavLink>
        ))}
      </div>
      {canAccessAdmin(user?.permissions) && (
        <div className="nav-section admin-nav">
          <p>서비스 관리자</p>
          {visible(adminNavigation).map((item) => (
            <NavLink
              end={item.path === "/admin"}
              to={item.path}
              key={item.path}
              onClick={onSelect}
              className={({ isActive }) => cn("nav-link", isActive && "active")}
              title={item.description}
            >
              <item.icon />
              <span>{item.label}</span>
            </NavLink>
          ))}
        </div>
      )}
    </nav>
  );
}

function RightRail() {
  const topicsQuery = useApiQuery<CursorPage<Topic> | Topic[]>(
    "/topics?sort=trending&limit=5",
  );
  const topics = Array.isArray(topicsQuery.data)
    ? topicsQuery.data
    : topicsQuery.data?.items || [];
  return (
    <aside className="context-rail" aria-label="추천 정보">
      <section className="rail-card">
        <header>
          <TrendingUp />
          <h2>지금의 펄스</h2>
        </header>
        {topics.length ? (
          topics.map((topic, index) => (
            <Link
              to={`/topics/${encodeURIComponent(topic.slug)}`}
              className="trend-item"
              key={topic.id}
            >
              <span>
                <small>{index + 1} · 인기 토픽</small>
                <strong>{topicLabel(topic.name)}</strong>
                <small>
                  {topic.moinCount?.toLocaleString("ko-KR") || 0}개 모인
                </small>
              </span>
            </Link>
          ))
        ) : (
          <p className="rail-empty">아직 집계된 인기 토픽이 없습니다.</p>
        )}
        <Link className="rail-more" to="/pulse">
          펄스 전체 보기
        </Link>
      </section>
      <section className="rail-card">
        <header>
          <UserPlus />
          <h2>새로운 연결</h2>
        </header>
        <p className="rail-empty">
          관심사가 맞닿는 사람과 토픽을 탐색해 보세요.
        </p>
        <Link className="rail-more" to="/explore">
          연결 탐색하기
        </Link>
      </section>
      <p className="rail-footnote">
        MOINA의 추천 이유는 각 모인에서 확인할 수 있습니다.
      </p>
    </aside>
  );
}

export function AppShell() {
  const { user } = useAuth();
  const { notify } = useToast();
  const location = useLocation();
  const navigate = useNavigate();
  const mainRef = useRef<HTMLElement>(null);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [quickNavigationOpen, setQuickNavigationOpen] = useState(false);
  const [unread, setUnread] = useState(0);
  const capabilities = useApiQuery<ServiceCapabilities>("/workflow/status");
  const preferences = useApiQuery<UserPreferences>("/profile/preferences");
  const notificationSummary = useApiQuery<{ unreadCount?: number }>(
    "/notifications?limit=1",
  );
  const reloadNotificationSummary = notificationSummary.reload;
  const approvalVisible = Boolean(
    capabilities.data?.approvalEnabled || capabilities.data?.approvalPending,
  );
  const adminView =
    location.pathname === "/admin" || location.pathname.startsWith("/admin/");

  useEffect(() => {
    if (preferences.data) applyAppearance(preferences.data);
  }, [preferences.data]);
  useEffect(() => {
    if (notificationSummary.data?.unreadCount !== undefined)
      setUnread(notificationSummary.data.unreadCount);
  }, [notificationSummary.data?.unreadCount]);

  useEffect(() => {
    if (!user) return;
    const currentRoute = `${location.pathname}${location.search}`;
    rememberRoute(user.id, currentRoute);
    rememberRecentRoute(user.id, currentRoute);
    const title = [...primaryNavigation, ...adminNavigation].find(
      (item) => item.path === location.pathname,
    )?.label;
    document.title = title ? `${title} · MOINA` : "MOINA";
  }, [location.pathname, location.search, user]);

  useEffect(() => {
    setMobileOpen(false);
    const frame = requestAnimationFrame(() =>
      mainRef.current?.focus({ preventScroll: true }),
    );
    return () => cancelAnimationFrame(frame);
  }, [location.pathname]);

  useEffect(() => {
    if (!user) return;
    let socket: WebSocket | undefined;
    let retry: number | undefined;
    let closed = false;
    let attempts = 0;
    const reconcile = window.setInterval(
      () => void reloadNotificationSummary(),
      60_000,
    );
    const connect = () => {
      if (closed) return;
      try {
        socket = new WebSocket(websocketURL("/ws/notifications"));
        socket.onopen = () => {
          attempts = 0;
          void reloadNotificationSummary();
        };
        socket.onmessage = (event) => {
          try {
            const decoded: unknown = JSON.parse(event.data);
            if (!isLiveNotificationPayload(decoded)) {
              void reloadNotificationSummary();
              return;
            }
            const payload: LiveNotificationPayload = decoded;
            if (!dispatchLiveNotification(payload, {
              toast: (message) => notify(message),
              desktop: (notification) => showDesktopNotification(notification, navigate),
            })) return;
            setUnread((current) => payload.unreadCount ?? current + 1);
          } catch {
            // The durable REST summary is authoritative. A malformed frame or
            // a best-effort browser channel failure must not invent unread rows.
            void reloadNotificationSummary();
          }
        };
        socket.onclose = () => {
          if (!closed)
            retry = window.setTimeout(
              connect,
              Math.min(30_000, 1000 * 2 ** attempts++),
            );
        };
      } catch {
        retry = window.setTimeout(connect, 5000);
      }
    };
    connect();
    return () => {
      closed = true;
      window.clearInterval(reconcile);
      if (retry) window.clearTimeout(retry);
      socket?.close();
    };
  }, [navigate, notify, reloadNotificationSummary, user]);

  const mobileTitle = useMemo(
    () =>
      [...primaryNavigation, ...adminNavigation].find(
        (item) => location.pathname === item.path,
      )?.label || APP_DISPLAY_NAME,
    [location.pathname],
  );
  return (
    <div className={cn("app-shell", adminView && "admin-context")}>
      <a href="#main-content" className="skip-link">
        본문으로 건너뛰기
      </a>
      <aside className="desktop-sidebar">
        <Brand />
        <ScrollArea.Root
          type="always"
          className="sidebar-scroll custom-scrollbar"
        >
          <ScrollArea.Viewport className="sidebar-viewport">
            <NavList approvalVisible={approvalVisible} />
          </ScrollArea.Viewport>
          <ScrollArea.Scrollbar
            orientation="vertical"
            className="radix-scrollbar sidebar-scrollbar"
          >
            <ScrollArea.Thumb className="radix-scroll-thumb" />
          </ScrollArea.Scrollbar>
        </ScrollArea.Root>
        <Button
          className="quick-navigation-trigger"
          variant="secondary"
          aria-keyshortcuts="Control+K Meta+K"
          onClick={() => setQuickNavigationOpen(true)}
        >
          <Search />
          <span>빠른 이동</span>
          <kbd>Ctrl K</kbd>
        </Button>
        <Button
          className="compose-button"
          variant="primary"
          onClick={() => navigate("/flow?compose=1")}
        >
          <PenLine />
          모인 작성
        </Button>
        <div className="sidebar-profile">
          <ProfileMenu />
        </div>
      </aside>
      <Dialog.Root open={mobileOpen} onOpenChange={setMobileOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="mobile-nav-overlay" />
          <Dialog.Content
            className="mobile-nav-panel"
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              const trigger = document.getElementById("mobile-menu-trigger");
              if (trigger instanceof HTMLButtonElement) {
                event.preventDefault();
                trigger.focus();
              }
            }}
          >
            <Dialog.Title className="sr-only">주 메뉴</Dialog.Title>
            <div className="mobile-nav-head">
              <Brand />
              <Dialog.Close asChild>
                <IconButton label="메뉴 닫기">
                  <X />
                </IconButton>
              </Dialog.Close>
            </div>
            <ScrollArea.Root
              type="always"
              className="mobile-nav-scroll custom-scrollbar"
            >
              <ScrollArea.Viewport className="mobile-nav-viewport">
                <NavList
                  approvalVisible={approvalVisible}
                  onSelect={() => setMobileOpen(false)}
                />
              </ScrollArea.Viewport>
              <ScrollArea.Scrollbar
                orientation="vertical"
                className="radix-scrollbar"
              >
                <ScrollArea.Thumb className="radix-scroll-thumb" />
              </ScrollArea.Scrollbar>
            </ScrollArea.Root>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <div className="app-content-column">
        <header className="mobile-topbar">
          <IconButton
            id="mobile-menu-trigger"
            label="메뉴 열기"
            aria-haspopup="dialog"
            aria-expanded={mobileOpen}
            onClick={() => setMobileOpen(true)}
          >
            <Menu />
          </IconButton>
          <strong>{mobileTitle}</strong>
          <div>
            <IconButton
              label="빠른 이동"
              aria-keyshortcuts="Control+K Meta+K"
              onClick={() => setQuickNavigationOpen(true)}
            >
              <Search />
            </IconButton>
            <ProfileMenu />
          </div>
        </header>
        <main
          id="main-content"
          ref={mainRef}
          tabIndex={-1}
          className="main-content"
        >
          <Outlet />
        </main>
        <nav className="mobile-bottom-nav" aria-label="모바일 빠른 메뉴">
          <NavLink to="/flow" aria-label="플로우">
            <Waves />
          </NavLink>
          <NavLink to="/search" aria-label="검색">
            <Search />
          </NavLink>
          <Link
            to="/flow?compose=1"
            className="mobile-compose"
            aria-label="모인 작성"
          >
            <PenLine />
          </Link>
          <NavLink
            to="/notifications"
            aria-label={`알림${unread ? ` ${unread}개` : ""}`}
            className="notification-link"
          >
            <Bell />
            {unread > 0 && <span>{Math.min(unread, 99)}</span>}
          </NavLink>
          <NavLink
            to={`/profile/${encodeURIComponent(user?.username || "")}`}
            aria-label="내 프로필"
          >
            <Avatar
              name={user?.displayName || "나"}
              src={user?.avatarUrl}
              size="small"
            />
          </NavLink>
        </nav>
      </div>
      {!adminView && <RightRail />}
      {adminView && (
        <aside className="admin-context-badge">
          <ShieldCheck />
          <span>
            <strong>서비스 관리자</strong>
            <small>개인 설정과 분리된 운영 영역</small>
          </span>
        </aside>
      )}
      {user && (
        <QuickNavigation
          open={quickNavigationOpen}
          onOpenChange={setQuickNavigationOpen}
          userId={user.id}
          username={user.username}
          permissions={user.permissions}
          approvalVisible={approvalVisible}
        />
      )}
    </div>
  );
}
