import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PublicConfig } from '../types';
import { beginSilentSso, clearSilentSsoState, markSignedOut, shouldAttemptSilentSso, silentSsoAttempted, silentSsoEligiblePath } from './silentSso';

const enabled: PublicConfig = { oidc: { enabled: true, autoLogin: true } };
const at = (pathname: string, search = '') => ({ pathname, search });
const config = (value: PublicConfig | undefined) => vi.fn(() => Promise.resolve(value));
// jsdom's location.assign cannot be spied on, so the whole location is stubbed.
function stubAssign() {
  const assign = vi.fn();
  vi.stubGlobal('location', { ...window.location, assign });
  return assign;
}

describe('silent SSO 시도 규칙', () => {
  beforeEach(() => window.sessionStorage.clear());
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

  it('auto-login이 켜진 SSO에서 처음 한 번은 시도한다', async () => {
    await expect(shouldAttemptSilentSso(config(enabled), at('/flow'))).resolves.toBe(true);
  });

  it('auto-login이 꺼져 있거나 SSO가 꺼져 있으면 시도하지 않는다', async () => {
    await expect(shouldAttemptSilentSso(config({ oidc: { enabled: true, autoLogin: false } }), at('/flow'))).resolves.toBe(false);
    await expect(shouldAttemptSilentSso(config({ oidc: { enabled: false, autoLogin: true } }), at('/flow'))).resolves.toBe(false);
    await expect(shouldAttemptSilentSso(config(undefined), at('/flow'))).resolves.toBe(false);
  });

  it('한 탭 세션에 한 번만 시도한다', async () => {
    stubAssign();
    beginSilentSso('/flow');
    expect(silentSsoAttempted()).toBe(true);
    await expect(shouldAttemptSilentSso(config(enabled), at('/flow'))).resolves.toBe(false);
  });

  it('거절 표시가 붙은 주소에서는 저장소가 비어 있어도 시도하지 않는다', async () => {
    const load = config(enabled);
    await expect(shouldAttemptSilentSso(load, at('/flow', '?sso=none'))).resolves.toBe(false);
    await expect(shouldAttemptSilentSso(load, at('/flow', '?sso=error'))).resolves.toBe(false);
    expect(load).not.toHaveBeenCalled();
  });

  it('로그인·콜백·API·헬스 경로에서는 시도하지 않고 서버에 묻지도 않는다', async () => {
    for (const pathname of ['/login', '/login/', '/api/v1/auth/oidc/callback', '/auth/callback', '/mcp', '/healthz', '/readyz', '/metrics']) {
      expect(silentSsoEligiblePath(pathname), pathname).toBe(false);
      const load = config(enabled);
      await expect(shouldAttemptSilentSso(load, at(pathname)), pathname).resolves.toBe(false);
      expect(load, pathname).not.toHaveBeenCalled();
    }
    for (const pathname of ['/', '/flow', '/moin/abc', '/loginhistory', '/apis']) expect(silentSsoEligiblePath(pathname), pathname).toBe(true);
  });

  it('스스로 로그아웃한 뒤에는 시도하지 않고, 다시 세션이 생기면 억제가 풀린다', async () => {
    markSignedOut();
    await expect(shouldAttemptSilentSso(config(enabled), at('/flow'))).resolves.toBe(false);
    expect(silentSsoAttempted()).toBe(true);
    clearSilentSsoState();
    await expect(shouldAttemptSilentSso(config(enabled), at('/flow'))).resolves.toBe(true);
  });

  it('저장소를 읽지 못하면 이미 시도한 것으로 친다', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new DOMException('blocked', 'SecurityError'); });
    const load = config(enabled);
    await expect(shouldAttemptSilentSso(load, at('/flow'))).resolves.toBe(false);
    expect(load).not.toHaveBeenCalled();
    expect(silentSsoAttempted()).toBe(true);
  });

  it('최상위 이동으로 prompt=none 로그인을 시작하고 깊은 링크를 지킨다', () => {
    const assign = stubAssign();
    beginSilentSso('/moin/abc?tab=replies');
    expect(assign).toHaveBeenCalledWith('/api/v1/auth/oidc/login?prompt=none&returnTo=%2Fmoin%2Fabc%3Ftab%3Dreplies');
    beginSilentSso('//evil.example');
    expect(assign).toHaveBeenLastCalledWith('/api/v1/auth/oidc/login?prompt=none&returnTo=%2F');
    beginSilentSso('https://evil.example');
    expect(assign).toHaveBeenLastCalledWith('/api/v1/auth/oidc/login?prompt=none&returnTo=%2F');
  });
});
