import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { apiRequest, parseSSE } from './client';

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
