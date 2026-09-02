export const APP_NAME = 'moina';
export const APP_DISPLAY_NAME = 'MOINA';
export const APP_VERSION = normalizeVersion(import.meta.env.VITE_MOINA_VERSION || 'v0.1.15');
export const API_BASE = '/api/v1';

export function normalizeVersion(value: string) {
  const clean = value.trim().replace(/^v/i, '');
  return `v${clean || '0.1.15'}`;
}

export function safeAppPath(value: string | null | undefined, fallback = '/flow') {
  // eslint-disable-next-line no-control-regex -- rejecting control characters in a stored route is the purpose of this guard
  if (!value || value !== value.trim() || !value.startsWith('/') || value.startsWith('//') || value.includes('\\') || /[\u0000-\u001f\u007f]/.test(value)) return fallback;
  let decoded: string;
  try { decoded = decodeURIComponent(value); } catch { return fallback; }
  if (!decoded.startsWith('/') || decoded.startsWith('//') || decoded.includes('\\')) return fallback;
  const pathname = decoded.split(/[?#]/, 1)[0];
  if (pathname.includes('//') || pathname.split('/').some((part) => part === '.' || part === '..')) return fallback;
  const withoutHash = value.split('#', 1)[0];
  const normalizedPath = withoutHash.split('?', 1)[0];
  if (normalizedPath === '/' || normalizedPath === '/login' || normalizedPath.startsWith('/auth/')) return fallback;
  return withoutHash;
}

const lastRouteKey = (userId: string) => `moina.last-route.${userId}`;
const recentRoutesKey = (userId: string) => `moina.recent-routes.${userId}`;
const memoryRoutes = new Map<string, string>();
const memoryRecentRoutes = new Map<string, string[]>();
const recentRouteLimit = 8;

function normalizedRecentRoute(path: string) {
  const safe = safeAppPath(path, '');
  if (!safe || safe.startsWith('/access-denied')) return '';
  const url = new URL(safe, 'http://moina.local');
  url.searchParams.delete('compose');
  url.searchParams.delete('quote');
  return `${url.pathname}${url.search}`;
}

function recentRouteIdentity(path: string) {
  return path.split(/[?#]/, 1)[0];
}

export function rememberRoute(userId: string, path: string) {
  const safe = safeAppPath(path, '');
  if (!safe || safe.startsWith('/access-denied')) return;
  const key = lastRouteKey(userId);
  memoryRoutes.set(key, safe);
  try { window.localStorage?.setItem(key, safe); } catch { /* Storage may be unavailable. */ }
}

export function rememberedRoute(userId: string) {
  const key = lastRouteKey(userId);
  try { return safeAppPath(window.localStorage?.getItem(key) || memoryRoutes.get(key)); } catch { return safeAppPath(memoryRoutes.get(key)); }
}

function parseRecentRoutes(value: string | string[] | null | undefined) {
  let candidates: unknown = value;
  if (typeof value === 'string') {
    try { candidates = JSON.parse(value); } catch { return []; }
  }
  if (!Array.isArray(candidates)) return [];
  const unique = new Set<string>();
  const identities = new Set<string>();
  for (const candidate of candidates) {
    if (typeof candidate !== 'string') continue;
    const safe = normalizedRecentRoute(candidate);
    if (!safe) continue;
    const identity = recentRouteIdentity(safe);
    if (identities.has(identity)) continue;
    identities.add(identity);
    unique.add(safe);
    if (unique.size === recentRouteLimit) break;
  }
  return [...unique];
}

export function rememberRecentRoute(userId: string, path: string) {
  const safe = normalizedRecentRoute(path);
  if (!safe) return;
  const key = recentRoutesKey(userId);
  const recent = recentlyVisitedRoutes(userId);
  const identity = recentRouteIdentity(safe);
  const next = [safe, ...recent.filter((candidate) => recentRouteIdentity(candidate) !== identity)].slice(0, recentRouteLimit);
  memoryRecentRoutes.set(key, next);
  try { window.localStorage?.setItem(key, JSON.stringify(next)); } catch { /* Storage may be unavailable. */ }
}

export function recentlyVisitedRoutes(userId: string) {
  const key = recentRoutesKey(userId);
  try {
    const stored = window.localStorage?.getItem(key);
    return parseRecentRoutes(stored === null ? memoryRecentRoutes.get(key) : stored);
  } catch {
    return parseRecentRoutes(memoryRecentRoutes.get(key));
  }
}
