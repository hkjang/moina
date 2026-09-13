import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthProvider, useAuth } from './AuthContext';
import { markSignedOut, silentSsoAttempted } from './silentSso';

const mocks = vi.hoisted(() => ({ apiRequest: vi.fn() }));

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  return { ...actual, apiRequest: mocks.apiRequest };
});

const session = { user: { id: 'usr_1', username: 'mina', displayName: '미나', roles: ['member'] }, permissions: [] };

function answer(options: { me?: unknown; status?: unknown }) {
  mocks.apiRequest.mockImplementation((path: string) => {
    if (path === '/auth/me') return options.me ? Promise.resolve(options.me) : Promise.reject(new Error('401'));
    if (path === '/auth/oidc/status') return Promise.resolve(options.status ?? { enabled: false });
    if (path === '/auth/logout') return Promise.resolve(undefined);
    return Promise.reject(new Error(`unexpected ${path}`));
  });
}

function stubLocation(pathname: string, search = '') {
  const assign = vi.fn();
  vi.stubGlobal('location', { ...window.location, pathname, search, assign });
  return assign;
}

function Probe() {
  const { user, loading, logout } = useAuth();
  return <div><span>{loading ? 'loading' : user ? `user:${user.username}` : 'anonymous'}</span><button type="button" onClick={() => void logout()}>로그아웃</button></div>;
}

const statusCalls = () => mocks.apiRequest.mock.calls.filter(([path]) => path === '/auth/oidc/status').length;

describe('AuthProvider의 silent SSO 시작', () => {
  beforeEach(() => { mocks.apiRequest.mockReset(); window.sessionStorage.clear(); });
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

  it('세션이 없고 auto-login이 켜져 있으면 화면을 그리기 전에 깊은 링크를 들고 제공자로 이동한다', async () => {
    const assign = stubLocation('/moin/abc', '?tab=replies');
    answer({ status: { enabled: true, autoLogin: true } });
    render(<AuthProvider><Probe/></AuthProvider>);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/api/v1/auth/oidc/login?prompt=none&returnTo=%2Fmoin%2Fabc%3Ftab%3Dreplies'));
    // The page is unloading; nothing must flash in the meantime.
    expect(screen.getByText('loading')).toBeInTheDocument();
    expect(silentSsoAttempted()).toBe(true);
  });

  it('auto-login이 꺼진 기본 설정에서는 아무것도 달라지지 않는다', async () => {
    const assign = stubLocation('/flow');
    answer({ status: { enabled: true, autoLogin: false } });
    render(<AuthProvider><Probe/></AuthProvider>);
    await screen.findByText('anonymous');
    expect(assign).not.toHaveBeenCalled();
    expect(silentSsoAttempted()).toBe(false);
  });

  it('로그인 화면 자체에서는 시도하지 않고 설정을 묻지도 않는다', async () => {
    const assign = stubLocation('/login', '?sso=none');
    answer({ status: { enabled: true, autoLogin: true } });
    render(<AuthProvider><Probe/></AuthProvider>);
    await screen.findByText('anonymous');
    expect(assign).not.toHaveBeenCalled();
    expect(statusCalls()).toBe(0);
  });

  it('스스로 로그아웃한 뒤에는 시도하지 않고, 세션이 다시 생기면 억제가 풀린다', async () => {
    const assign = stubLocation('/flow');
    answer({ me: session, status: { enabled: true, autoLogin: true } });
    render(<AuthProvider><Probe/></AuthProvider>);
    await screen.findByText('user:mina');
    await act(async () => { screen.getByRole('button', { name: '로그아웃' }).click(); });
    await screen.findByText('anonymous');
    expect(assign).not.toHaveBeenCalled();
    expect(silentSsoAttempted()).toBe(true);
    cleanup();

    // A new load in the same tab without a session stays on the login screen.
    answer({ status: { enabled: true, autoLogin: true } });
    render(<AuthProvider><Probe/></AuthProvider>);
    await screen.findByText('anonymous');
    expect(assign).not.toHaveBeenCalled();
    cleanup();

    // Signing in again (here: the session is back) lifts the suppression.
    markSignedOut();
    answer({ me: session });
    render(<AuthProvider><Probe/></AuthProvider>);
    await screen.findByText('user:mina');
    expect(silentSsoAttempted()).toBe(false);
  });
});
