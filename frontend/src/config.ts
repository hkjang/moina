export const APP_NAME = 'moina';
export const APP_DISPLAY_NAME = 'MOINA';
export const APP_VERSION = normalizeVersion(import.meta.env.VITE_MOINA_VERSION || 'v0.1.2');
export const API_BASE = '/api/v1';

export function normalizeVersion(value: string) {
  const clean = value.trim().replace(/^v/i, '');
  return `v${clean || '0.1.2'}`;
}

export function safeAppPath(value: string | null | undefined, fallback = '/flow') {
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
const memoryRoutes = new Map<string, string>();

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
