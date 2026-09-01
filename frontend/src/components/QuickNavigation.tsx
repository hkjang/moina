import * as Dialog from "@radix-ui/react-dialog";
import {
  Clock3,
  CornerDownLeft,
  Hash,
  MessageCircleMore,
  Network,
  PenLine,
  Search,
  UserRound,
  X,
  type LucideIcon,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { recentlyVisitedRoutes } from "../config";
import {
  adminNavigation,
  canAccessAdmin,
  hasPermission,
  personalNavigation,
  primaryNavigation,
  type NavItem,
} from "../navigation";
import { cn } from "../lib/cn";
import { IconButton } from "./ui";

type QuickNavigationGroup = "최근 방문" | "빠른 실행" | "주요 화면" | "개인 설정" | "서비스 관리자" | "검색 결과";

interface QuickCommand {
  id: string;
  label: string;
  description: string;
  path: string;
  icon: LucideIcon;
  group: QuickNavigationGroup;
  shortcut?: string;
  keywords?: string[];
}

interface QuickSection {
  label: QuickNavigationGroup;
  commands: QuickCommand[];
}

export interface QuickNavigationProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userId: string;
  username: string;
  permissions?: string[];
  approvalVisible: boolean;
}

function commandFromNavigation(item: NavItem, group: QuickNavigationGroup, path = item.path): QuickCommand {
  return {
    id: `route:${path}`,
    label: item.label,
    description: item.description,
    path,
    icon: item.icon,
    group,
    shortcut: item.shortcut,
    keywords: item.keywords,
  };
}

function decodedSegment(pathname: string) {
  const value = pathname.split("/").filter(Boolean).at(-1) || "";
  try { return decodeURIComponent(value); } catch { return value; }
}

function recentCommand(path: string, available: NavItem[]): QuickCommand | undefined {
  const pathname = path.split(/[?#]/, 1)[0];
  const navigation = available.find((item) => item.path === pathname);
  if (navigation) return commandFromNavigation(navigation, "최근 방문", path);
  const segment = decodedSegment(pathname);
  if (!segment) return undefined;
  if (pathname.startsWith("/moin/")) {
    return { id: `recent:${path}`, label: "Moin 대화", description: "최근에 본 Moin Chain", path, icon: MessageCircleMore, group: "최근 방문" };
  }
  if (pathname.startsWith("/profile/")) {
    return { id: `recent:${path}`, label: `@${segment} 프로필`, description: "최근에 본 사용자 프로필", path, icon: UserRound, group: "최근 방문" };
  }
  if (pathname.startsWith("/topics/")) {
    return { id: `recent:${path}`, label: `#${segment} 토픽`, description: "최근에 본 관심사", path, icon: Hash, group: "최근 방문" };
  }
  if (pathname.startsWith("/moims/")) {
    return { id: `recent:${path}`, label: `${segment} 모임`, description: "최근에 방문한 커뮤니티", path, icon: Network, group: "최근 방문" };
  }
  return undefined;
}

function searchable(command: QuickCommand) {
  return [command.label, command.description, command.path, ...(command.keywords || [])]
    .join(" ")
    .normalize("NFKC")
    .toLocaleLowerCase("ko-KR");
}

function matchesQuery(command: QuickCommand, query: string) {
  const target = searchable(command);
  return query
    .normalize("NFKC")
    .toLocaleLowerCase("ko-KR")
    .trim()
    .split(/\s+/)
    .every((token) => target.includes(token));
}

function isEditableTarget(target: EventTarget | null) {
  return target instanceof HTMLElement && Boolean(
    target.closest("input, textarea, select, [contenteditable='true'], [role='textbox']"),
  );
}

export function QuickNavigation({
  open,
  onOpenChange,
  userId,
  username,
  permissions,
  approvalVisible,
}: QuickNavigationProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const inputRef = useRef<HTMLInputElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const chordRef = useRef("");
  const chordTimerRef = useRef<number | undefined>(undefined);
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [recentPaths, setRecentPaths] = useState<string[]>([]);
  const [chordHint, setChordHint] = useState(false);
  const currentPath = `${location.pathname}${location.search}`;

  const visibleNavigation = useMemo(() => {
    const allowed = (item: NavItem) =>
      hasPermission(permissions, item.permission) &&
      (!item.admin || canAccessAdmin(permissions)) &&
      (!item.approvalOnly || approvalVisible);
    return [...primaryNavigation, ...personalNavigation, ...adminNavigation].filter(allowed);
  }, [approvalVisible, permissions]);

  const baseSections = useMemo<QuickSection[]>(() => {
    const actions: QuickCommand[] = [
      {
        id: "action:compose",
        label: "새 Moin 작성",
        description: "Flow에서 새 생각이나 이미지를 공유합니다",
        path: "/flow?compose=1",
        icon: PenLine,
        group: "빠른 실행",
        shortcut: "C",
        keywords: ["글쓰기", "게시", "작성"],
      },
      {
        id: "action:profile",
        label: "내 프로필",
        description: `@${username} 공개 프로필로 이동합니다`,
        path: `/profile/${encodeURIComponent(username)}`,
        icon: UserRound,
        group: "빠른 실행",
        keywords: ["계정", "마이페이지"],
      },
    ];
    const recent = recentPaths
      .filter((path) => path !== currentPath)
      .map((path) => recentCommand(path, visibleNavigation))
      .filter((command): command is QuickCommand => Boolean(command))
      .slice(0, 8);
    const primaryPaths = new Set(primaryNavigation.map((item) => item.path));
    const personalPaths = new Set(personalNavigation.map((item) => item.path));
    const adminPaths = new Set(adminNavigation.map((item) => item.path));
    const sections: QuickSection[] = [
      { label: "최근 방문", commands: recent },
      { label: "빠른 실행", commands: actions },
      {
        label: "주요 화면",
        commands: visibleNavigation
          .filter((item) => primaryPaths.has(item.path))
          .map((item) => commandFromNavigation(item, "주요 화면")),
      },
      {
        label: "개인 설정",
        commands: visibleNavigation
          .filter((item) => personalPaths.has(item.path))
          .map((item) => commandFromNavigation(item, "개인 설정")),
      },
      {
        label: "서비스 관리자",
        commands: visibleNavigation
          .filter((item) => adminPaths.has(item.path))
          .map((item) => commandFromNavigation(item, "서비스 관리자")),
      },
    ];
    return sections.filter((section) => section.commands.length);
  }, [currentPath, recentPaths, username, visibleNavigation]);

  const sections = useMemo<QuickSection[]>(() => {
    const trimmed = query.trim();
    if (!trimmed) return baseSections;
    const seen = new Set<string>();
    const matched = baseSections
      .flatMap((section) => section.commands)
      .filter((command) => {
        if (seen.has(command.path) || !matchesQuery(command, trimmed)) return false;
        seen.add(command.path);
        return true;
      });
    return [{
      label: "검색 결과",
      commands: [
        {
          id: "action:search-query",
          label: `“${trimmed}” 통합 검색`,
          description: "Moin, 사람, 토픽과 모임 전체에서 찾습니다",
          path: `/search?q=${encodeURIComponent(trimmed)}&type=posts`,
          icon: Search,
          group: "검색 결과",
          shortcut: "Enter",
        },
        ...matched,
      ],
    }];
  }, [baseSections, query]);

  const commands = useMemo(() => sections.flatMap((section) => section.commands), [sections]);
  const selected = commands[selectedIndex];

  const clearChord = useCallback(() => {
    chordRef.current = "";
    setChordHint(false);
    if (chordTimerRef.current !== undefined) window.clearTimeout(chordTimerRef.current);
    chordTimerRef.current = undefined;
  }, []);

  const runCommand = useCallback((command: QuickCommand) => {
    clearChord();
    setQuery("");
    onOpenChange(false);
    navigate(command.path);
  }, [clearChord, navigate, onOpenChange]);

  useEffect(() => {
    if (!open) return;
    setRecentPaths(recentlyVisitedRoutes(userId));
    setQuery("");
    setSelectedIndex(0);
  }, [open, userId]);

  useEffect(() => {
    setSelectedIndex(0);
  }, [query]);

  useEffect(() => {
    if (!open || !selected) return;
    document.getElementById(`quick-navigation-option-${selectedIndex}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [open, selected, selectedIndex]);

  useEffect(() => {
    const shortcutCommands = visibleNavigation
      .filter((item) => item.shortcut)
      .map((item) => commandFromNavigation(item, item.admin ? "서비스 관리자" : "주요 화면"));
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) return;
      const key = event.key.toLocaleLowerCase("en-US");
      const otherDialogOpen = !open && Boolean(document.querySelector("[role='dialog']"));
      if ((event.ctrlKey || event.metaKey) && key === "k") {
        if (otherDialogOpen) return;
        event.preventDefault();
        clearChord();
        onOpenChange(!open);
        return;
      }
      if (open || otherDialogOpen || isEditableTarget(event.target) || event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.key === "?") {
        event.preventDefault();
        onOpenChange(true);
        return;
      }
      if (key === "c") {
        event.preventDefault();
        runCommand({ id: "shortcut:compose", label: "새 Moin 작성", description: "", path: "/flow?compose=1", icon: PenLine, group: "빠른 실행" });
        return;
      }
      if (chordRef.current === "g") {
        const command = shortcutCommands.find((candidate) => candidate.shortcut?.endsWith(` ${key.toUpperCase()}`));
        clearChord();
        if (command) {
          event.preventDefault();
          runCommand(command);
        }
        return;
      }
      if (key === "g") {
        chordRef.current = "g";
        setChordHint(true);
        chordTimerRef.current = window.setTimeout(clearChord, 1200);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      clearChord();
    };
  }, [clearChord, onOpenChange, open, runCommand, visibleNavigation]);

  const handleInputKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing || !commands.length) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setSelectedIndex((current) => (current + 1) % commands.length);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setSelectedIndex((current) => (current - 1 + commands.length) % commands.length);
    } else if (event.key === "Home") {
      event.preventDefault();
      setSelectedIndex(0);
    } else if (event.key === "End") {
      event.preventDefault();
      setSelectedIndex(commands.length - 1);
    } else if (event.key === "Enter" && selected) {
      event.preventDefault();
      runCommand(selected);
    }
  };

  let optionIndex = 0;
  return (
    <>
      {chordHint && !open && (
        <div className="quick-navigation-chord" role="status">
          <kbd>G</kbd>
          <span>다음 이동 키를 누르세요</span>
        </div>
      )}
      <Dialog.Root open={open} onOpenChange={onOpenChange}>
        <Dialog.Portal>
          <Dialog.Overlay className="quick-navigation-overlay" />
          <Dialog.Content
            className="quick-navigation-dialog"
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              if (document.activeElement instanceof HTMLElement) restoreFocusRef.current = document.activeElement;
              requestAnimationFrame(() => inputRef.current?.focus());
            }}
            onCloseAutoFocus={(event) => {
              const target = restoreFocusRef.current;
              if (target?.isConnected) {
                event.preventDefault();
                target.focus({ preventScroll: true });
              }
              restoreFocusRef.current = null;
            }}
          >
            <div className="quick-navigation-heading">
              <div>
                <Dialog.Title>빠른 이동</Dialog.Title>
                <Dialog.Description>화면, 설정과 작업을 한 번에 찾아 이동합니다.</Dialog.Description>
              </div>
              <Dialog.Close asChild>
                <IconButton label="빠른 이동 닫기"><X /></IconButton>
              </Dialog.Close>
            </div>
            <div className="quick-navigation-search">
              <Search aria-hidden="true" />
              <label className="sr-only" htmlFor="quick-navigation-input">빠른 이동 검색</label>
              <input
                id="quick-navigation-input"
                ref={inputRef}
                role="combobox"
                aria-expanded="true"
                aria-autocomplete="list"
                aria-controls="quick-navigation-results"
                aria-activedescendant={selected ? `quick-navigation-option-${selectedIndex}` : undefined}
                autoComplete="off"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={handleInputKeyDown}
                placeholder="화면, 설정, 사용자 또는 작업 검색"
              />
              <kbd aria-hidden="true">ESC</kbd>
            </div>
            <div
              id="quick-navigation-results"
              className="quick-navigation-results custom-scrollbar"
              role="listbox"
              aria-label="빠른 이동 결과"
            >
              {sections.map((section) => (
                <div className="quick-navigation-group" role="group" aria-label={section.label} key={section.label}>
                  <p>{section.label === "최근 방문" && <Clock3 aria-hidden="true" />}{section.label}</p>
                  {section.commands.map((command) => {
                    const index = optionIndex++;
                    const active = index === selectedIndex;
                    return (
                      <button
                        id={`quick-navigation-option-${index}`}
                        type="button"
                        role="option"
                        aria-selected={active}
                        tabIndex={-1}
                        className={cn("quick-navigation-option", active && "active")}
                        key={`${section.label}:${command.id}`}
                        onMouseEnter={() => setSelectedIndex(index)}
                        onClick={() => runCommand(command)}
                      >
                        <span className="quick-navigation-icon"><command.icon aria-hidden="true" /></span>
                        <span className="quick-navigation-copy">
                          <strong>{command.label}</strong>
                          <small>{command.description}</small>
                        </span>
                        {command.shortcut && <kbd>{command.shortcut}</kbd>}
                      </button>
                    );
                  })}
                </div>
              ))}
            </div>
            <footer className="quick-navigation-footer">
              <span><kbd>↑</kbd><kbd>↓</kbd> 이동</span>
              <span><kbd>Enter</kbd> 선택</span>
              <span><kbd>G</kbd> 연속 이동</span>
              <span><CornerDownLeft aria-hidden="true" /> 검색어는 통합 검색</span>
            </footer>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </>
  );
}
