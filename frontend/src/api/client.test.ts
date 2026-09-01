import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiRequest, parseSSE } from './client';

describe('API 클라이언트', () => {
  beforeEach(() => {
    document.cookie = 'moina_csrf=test-token; path=/';
  });
  afterEach(() => vi.restoreAllMocks());

  it('data 성공 래퍼를 풀고 cookie 세션과 CSRF를 전송한다', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: { ok: true } }), {
      status: 200, headers: { 'content-type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(apiRequest<{ ok: boolean }>('/profile/preferences', { method: 'PUT', body: { appearance: {} } })).resolves.toEqual({ ok: true });
    const [, options] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(options.credentials).toBe('include');
    expect(new Headers(options.headers).get('X-CSRF-Token')).toBe('test-token');
    expect(new Headers(options.headers).get('Content-Type')).toBe('application/json');
  });

  it('안전한 GET 요청에는 CSRF 헤더를 붙이지 않는다', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: [] }), {
      status: 200, headers: { 'content-type': 'application/json' },
    }));
    vi.stubGlobal('fetch', fetchMock);
    await apiRequest('/topics');
    const [, options] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(options.headers).has('X-CSRF-Token')).toBe(false);
  });

  it('429/503 재시도를 위해 Retry-After를 밀리초로 보존한다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: '잠시 후 다시 시도하세요.' }), {
      status: 429,
      headers: { 'content-type': 'application/json', 'retry-after': '2' },
    })));
    const error = await apiRequest('/limited').catch((cause) => cause);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 429, retryAfterMs: 2_000 });
  });

  it('구조화된 관리자 오류 details를 보존한다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      code: 'oidc_private_host_required',
      message: '정확한 Host를 추가하세요.',
      details: { targetHost: 'keycloak.internal:8443', action: 'add_private_host' },
    }), { status: 502, headers: { 'content-type': 'application/json' } })));
    const error = await apiRequest('/admin/oidc/test', { method: 'POST' }).catch((cause) => cause);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      code: 'oidc_private_host_required',
      details: { targetHost: 'keycloak.internal:8443', action: 'add_private_host' },
    });
  });

  it('성공한 mutation은 resource invalidation event를 보낸다', async () => {
    const listener = vi.fn();
    window.addEventListener('moina:api-mutated', listener);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(undefined, { status: 204 })));
    await apiRequest('/profile/preferences', { method: 'PUT', body: { notifications: {} } });
    expect(listener).toHaveBeenCalledTimes(1);
    expect((listener.mock.calls[0][0] as CustomEvent).detail).toEqual({ path: '/profile/preferences', method: 'PUT' });
    window.removeEventListener('moina:api-mutated', listener);
  });
});

describe('SSE 스트리밍 파서', () => {
  it('증분 텍스트와 완료 이벤트를 해석한다', () => {
    expect(parseSSE('event: message\ndata: {"delta":"안녕"}')).toMatchObject({ type: 'message', delta: '안녕' });
    expect(parseSSE('data: [DONE]')).toMatchObject({ done: true });
  });

  it('OpenAI 호환 choices delta도 해석한다', () => {
    expect(parseSSE('data: {"choices":[{"delta":{"content":"지식"}}]}')?.delta).toBe('지식');
  });
});
