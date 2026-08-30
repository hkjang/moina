export type Permission = string;

export interface SessionUser {
  id: string;
  username: string;
  displayName: string;
  email?: string;
  bio?: string;
  avatarUrl?: string;
  provider?: "local" | "oidc" | string;
  roles: string[];
  permissions: Permission[];
}

export interface PublicConfig {
  serviceName?: string;
  version?: string;
  oidc?: {
    enabled?: boolean;
    label?: string;
    providerName?: string;
    allowRegistration?: boolean;
    registrationEnabled?: boolean;
  };
  features?: Record<string, boolean>;
}

export interface Topic {
  id: string;
  slug: string;
  name: string;
  description?: string;
  followerCount?: number;
  moinCount?: number;
  trendScore?: number;
  following?: boolean;
}

export interface Profile {
  id: string;
  username: string;
  displayName: string;
  bio?: string;
  avatarUrl?: string;
  expertise?: string[];
  signal?: number;
  followerCount?: number;
  followingCount?: number;
  moinCount?: number;
  followed?: boolean;
  blocked?: boolean;
  accountType?: "human" | "agent";
}

export type SignalType = "like" | "useful" | "insight" | "question" | "verify";

export interface Moin {
  id: string;
  content: string;
  kind?: "moin" | "echo" | "quote" | "remoin";
  author: Profile;
  createdAt: string;
  updatedAt?: string;
  visibility?: "public" | "followers" | "moim";
  status?: "published" | "pending_approval" | "rejected" | "deleted";
  topics?: Topic[];
  media?: Array<{
    id: string;
    type: "image" | "video";
    url: string;
    alt?: string;
  }>;
  replyToId?: string;
  quoteMoin?: Moin;
  counts?: {
    echoes?: number;
    remoins?: number;
    bookmarks?: number;
    signals?: Partial<Record<SignalType, number>>;
  };
  viewer?: { bookmarked?: boolean; remoined?: boolean; signals?: SignalType[] };
  recommendation?: Array<{ label: string; score: number }>;
}

export interface CursorPage<T> {
  items: T[];
  nextCursor?: string;
  total?: number;
}

export interface NotificationItem {
  id: string;
  type: "follow" | "signal" | "echo" | "remoin" | "mention" | "system" | string;
  title: string;
  body?: string;
  actor?: Profile;
  targetPath?: string;
  inApp?: boolean;
  toast?: boolean;
  desktop?: boolean;
  readAt?: string;
  createdAt: string;
}

export interface Moim {
  id: string;
  slug: string;
  name: string;
  description?: string;
  memberCount?: number;
  moinCount?: number;
  joined?: boolean;
  ownerId?: string;
  avatarUrl?: string;
  topics?: Topic[];
}

export interface UserPreferences {
  appearance?: {
    theme?: "light" | "dark" | "system";
    fontScale?: 100 | 112 | 125;
    reduceMotion?: boolean;
    density?: "comfortable" | "compact";
  };
  feed?: {
    mode?: "for_me" | "following";
    topicWeight?: number;
    linkWeight?: number;
    discoveryWeight?: number;
    recencyWeight?: number;
    excludedTopics?: string[];
    showReasons?: boolean;
  };
  notifications?: {
    inApp?: {
      mentions?: boolean;
      signals?: boolean;
      follows?: boolean;
      echoes?: boolean;
      approvals?: boolean;
    };
    toast?: {
      enabled?: boolean;
    };
    desktop?: {
      enabled?: boolean;
    };
    digest?: {
      mode?: "off" | "hourly" | "daily";
      time?: string;
    };
    quietHours?: {
      enabled?: boolean;
      start?: string;
      end?: string;
    };
  };
}

export interface PersonalKey {
  id: string;
  name: string;
  prefix?: string;
  permissions: string[];
  createdAt?: string;
  rotatedAt?: string;
  expiresAt?: string;
  revokedAt?: string;
  lastUsedAt?: string;
}

export interface ServiceCapabilities {
  approvalEnabled?: boolean;
  approvalPending?: boolean;
  approverRoles?: string[];
  aiEnabled?: boolean;
  mcpEnabled?: boolean;
}
