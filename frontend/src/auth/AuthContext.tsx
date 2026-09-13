import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { apiRequest, readableError } from '../api/client';
import { clearApiQueryCache } from '../hooks/apiQueryClient';
import type { PublicConfig, SessionUser } from '../types';
import { beginSilentSso, clearSilentSsoState, markSignedOut, shouldAttemptSilentSso } from './silentSso';

interface AuthContextValue {
  user: SessionUser | null;
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

function normalizeUser(payload: unknown): SessionUser | null {
  if (!payload || typeof payload !== 'object') return null;
  const wrapper = payload as Record<string, unknown>;
  const raw = wrapper.user && typeof wrapper.user === 'object' ? wrapper.user as Record<string, unknown> : wrapper;
  const id = raw.id ?? raw.userId ?? raw.sub ?? raw.username;
  if (!id) return null;
  const username = String(raw.username ?? id);
  const roles = Array.isArray(raw.roles) ? raw.roles.map(String) : typeof raw.role === 'string' ? [raw.role] : [];
  const outerPermissions = wrapper.permissions;
  const permissions = Array.isArray(raw.permissions) ? raw.permissions.map(String) : Array.isArray(outerPermissions) ? outerPermissions.map(String) : [];
  return {
    id: String(id), username,
    displayName: String(raw.displayName ?? raw.name ?? username),
    email: typeof raw.email === 'string' ? raw.email : undefined,
    bio: typeof raw.bio === 'string' ? raw.bio : undefined,
    avatarUrl: typeof raw.avatarUrl === 'string' ? raw.avatarUrl : undefined,
    provider: typeof raw.provider === 'string' ? raw.provider : 'local',
    roles, permissions,
  };
}

async function loadPublicConfig(): Promise<PublicConfig | undefined> {
  try { return { oidc: await apiRequest<PublicConfig['oidc']>('/auth/oidc/status', { suppressUnauthorized: true }) }; }
  catch { return undefined; }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<SessionUser | null>(null);
  const [loading, setLoading] = useState(true);
  const loadUser = useCallback(async () => {
    try { return normalizeUser(await apiRequest<unknown>('/auth/me', { suppressUnauthorized: true })); }
    catch { return null; }
  }, []);
  const refresh = useCallback(async () => {
    setUser(await loadUser());
    setLoading(false);
  }, [loadUser]);
  useEffect(() => {
    // The first load is the one place a silent SSO attempt may start: a visitor
    // without a session whose Keycloak session is still alive is sent to the
    // provider before anything renders, so they never see the login screen.
    // The attempt is a top-level navigation and `loading` stays true until the
    // page unloads, so nothing flashes in between.
    let cancelled = false;
    void (async () => {
      const current = await loadUser();
      if (cancelled) return;
      if (!current && await shouldAttemptSilentSso(loadPublicConfig)) {
        if (cancelled) return;
        beginSilentSso(`${window.location.pathname}${window.location.search}`);
        return;
      }
      setUser(current);
      setLoading(false);
    })();
    return () => { cancelled = true; };
  }, [loadUser]);
  // Once a session exists again, an earlier sign-out or refused attempt no
  // longer has to suppress auto-login.
  useEffect(() => { if (user) clearSilentSsoState(); }, [user]);
  useEffect(() => {
    const unauthorized = () => {
      clearApiQueryCache({ abort: true });
      setUser(null);
      if (window.location.pathname !== '/login') {
        const returnTo = `${window.location.pathname}${window.location.search}`;
        window.location.assign(`/login?expired=1&returnTo=${encodeURIComponent(returnTo)}`);
      }
    };
    window.addEventListener('moina:unauthorized', unauthorized);
    return () => window.removeEventListener('moina:unauthorized', unauthorized);
  }, []);
  const login = useCallback(async (username: string, password: string) => {
    try {
      clearApiQueryCache({ abort: true });
      const response = await apiRequest<unknown>('/auth/login', { method: 'POST', body: { username, password }, suppressUnauthorized: true });
      const resolved = normalizeUser(response);
      if (resolved) setUser(resolved); else await refresh();
    } catch (error) { throw new Error(readableError(error), { cause: error }); }
  }, [refresh]);
  const logout = useCallback(async () => {
    // An explicit sign-out must stick even while the Keycloak session is still
    // alive, otherwise auto-login would immediately undo it.
    markSignedOut();
    try { await apiRequest('/auth/logout', { method: 'POST', suppressUnauthorized: true }); } catch { /* Local state is still cleared. */ }
    finally { clearApiQueryCache({ abort: true }); setUser(null); }
  }, []);
  const value = useMemo(() => ({ user, loading, login, logout, refresh }), [user, loading, login, logout, refresh]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error('useAuth는 AuthProvider 안에서 사용해야 합니다.');
  return value;
}
