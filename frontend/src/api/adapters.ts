import { API_BASE } from '../config';
import type { CursorPage, Moin, Profile, SignalType, Topic } from '../types';

const record = (value: unknown): Record<string, unknown> => value && typeof value === 'object' ? value as Record<string, unknown> : {};
const text = (...values: unknown[]) => String(values.find((value) => typeof value === 'string' && value.trim()) ?? '');
const number = (...values: unknown[]) => {
  const value = values.find((candidate) => Number.isFinite(Number(candidate)));
  return value === undefined ? 0 : Number(value);
};
const boolean = (...values: unknown[]) => values.find((value) => typeof value === 'boolean') === true;
const strings = (value: unknown) => Array.isArray(value) ? value.map(String) : [];

function mediaURL(id: unknown) {
  return typeof id === 'string' && id ? `${API_BASE}/media/${encodeURIComponent(id)}` : undefined;
}

export function normalizeProfile(value: unknown): Profile {
  const raw = record(value);
  const username = text(raw.username, raw.handle, raw.id, 'user');
  return {
    id: text(raw.id, raw.userId, username),
    username,
    displayName: text(raw.displayName, raw.name, username),
    bio: text(raw.bio) || undefined,
    avatarUrl: text(raw.avatarUrl) || mediaURL(raw.avatarId),
    expertise: strings(raw.expertise ?? raw.topics),
    signal: number(raw.signal, raw.signalScore),
    followerCount: number(raw.followerCount, raw.followers),
    followingCount: number(raw.followingCount, raw.followingTotal),
    moinCount: number(raw.moinCount, raw.postCount),
    followed: boolean(raw.followed, raw.following),
    blocked: boolean(raw.blocked),
    accountType: raw.accountType === 'agent' || raw.type === 'agent' ? 'agent' : 'human',
  };
}

export function normalizeTopic(value: unknown): Topic {
  const raw = typeof value === 'string' ? { name: value, slug: value } : record(value);
  const name = text(raw.name, raw.label, raw.slug, '토픽').replace(/^#+/, '');
  return {
    id: text(raw.id, raw.slug, name), slug: text(raw.slug, name), name,
    description: text(raw.description) || undefined,
    followerCount: number(raw.followerCount), moinCount: number(raw.moinCount, raw.postCount),
    trendScore: number(raw.trendScore, raw.score), following: boolean(raw.following),
  };
}

function normalizeSignals(rawValue: unknown): Partial<Record<SignalType, number>> {
  const raw = record(rawValue);
  return {
    like: number(raw.like, raw.likes), useful: number(raw.useful), insight: number(raw.insight),
    question: number(raw.question), verify: number(raw.verify),
  };
}

export function normalizeMoin(value: unknown): Moin {
  const raw = record(value);
  const authorRaw = raw.author && typeof raw.author === 'object' ? raw.author : {
    id: raw.authorId, username: raw.authorUsername ?? raw.username,
    displayName: raw.authorDisplayName ?? raw.displayName, avatarId: raw.authorAvatarId,
    accountType: raw.authorType,
  };
  const counts = record(raw.counts);
  const viewer = record(raw.viewer);
  const media = Array.isArray(raw.media) ? raw.media.map((item) => {
    const entry = record(item);
    return { id: text(entry.id), type: entry.type === 'video' ? 'video' as const : 'image' as const, url: text(entry.url) || mediaURL(entry.id) || '', alt: text(entry.altText, entry.alt) || undefined };
  }) : [];
  const signalValues = raw.signals ?? counts.signals;
  const remoinSource = raw.remoinMoin;
  const recommendations: unknown[] = Array.isArray(raw.recommendation) ? raw.recommendation : Array.isArray(raw.reasons) ? raw.reasons : [];
  return {
    id: text(raw.id, raw.postId), content: text(raw.content, raw.text), author: normalizeProfile(authorRaw),
    kind: raw.kind === 'echo' || raw.kind === 'quote' || raw.kind === 'remoin' ? raw.kind : remoinSource ? 'remoin' : 'moin',
    createdAt: text(raw.createdAt, raw.created_at, new Date().toISOString()), updatedAt: text(raw.updatedAt) || undefined,
    visibility: raw.visibility === 'followers' || raw.visibility === 'moim' ? raw.visibility : 'public',
    topics: Array.isArray(raw.topics) ? raw.topics.map(normalizeTopic) : [], media,
    replyToId: text(raw.replyToId, raw.parentId) || undefined,
    quoteMoin: raw.quoteMoin || raw.quotePost || remoinSource ? normalizeMoin(raw.quoteMoin ?? raw.quotePost ?? remoinSource) : undefined,
    counts: {
      echoes: number(counts.echoes, raw.replyCount), remoins: number(counts.remoins, raw.remoinCount),
      bookmarks: number(counts.bookmarks, raw.bookmarkCount), signals: normalizeSignals(signalValues),
    },
    viewer: {
      bookmarked: boolean(viewer.bookmarked, raw.bookmarked), remoined: boolean(viewer.remoined, raw.remoined),
      signals: strings(viewer.signals ?? raw.viewerSignals).filter((item): item is SignalType => ['like', 'useful', 'insight', 'question', 'verify'].includes(item)),
    },
    recommendation: recommendations.map((reason) => { const item = record(reason); return { label: text(item.label, item.reason), score: number(item.score, item.weight) }; }),
  };
}

export function normalizePage<T>(value: unknown, normalize: (item: unknown) => T): CursorPage<T> {
  const raw = record(value);
  const items = Array.isArray(value) ? value : Array.isArray(raw.items) ? raw.items : Array.isArray(raw.posts) ? raw.posts : Array.isArray(raw.results) ? raw.results : [];
  return { items: items.map(normalize), nextCursor: text(raw.nextCursor, raw.next_cursor) || undefined, total: Number.isFinite(Number(raw.total)) ? Number(raw.total) : undefined };
}
