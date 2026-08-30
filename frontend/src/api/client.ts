import { API_BASE } from '../config';

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code = 'request_failed',
    public details?: unknown,
    public retryAfterMs?: number,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

type RequestOptions = Omit<RequestInit, 'body'> & { body?: unknown; suppressUnauthorized?: boolean };

export function apiURL(path: string) {
  return `${API_BASE}${path.startsWith('/') ? path : `/${path}`}`;
}

function cookieValue(name: string) {
  if (typeof document === 'undefined') return '';
  const prefix = `${encodeURIComponent(name)}=`;
  const value = document.cookie.split(';').map((part) => part.trim()).find((part) => part.startsWith(prefix))?.slice(prefix.length) || '';
  try { return decodeURIComponent(value); } catch { return value; }
}

function addCSRFHeader(headers: Headers, method: string | undefined) {
  if (!method || ['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase())) return;
  const token = cookieValue('moina_csrf');
  if (token && !headers.has('Authorization')) headers.set('X-CSRF-Token', token);
}

function errorMessage(payload: unknown, fallback: string) {
  if (payload && typeof payload === 'object') {
    const record = payload as Record<string, unknown>;
    if (typeof record.message === 'string' && record.message.trim()) return record.message;
    if (record.error && typeof record.error === 'object') {
      const nested = record.error as Record<string, unknown>;
      if (typeof nested.message === 'string') return nested.message;
    }
  }
  return fallback;
}

function retryAfterMilliseconds(value: string | null, now = Date.now()) {
  if (!value) return undefined;
  const seconds = Number(value);
  if (Number.isFinite(seconds) && seconds >= 0) return Math.round(seconds * 1000);
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? Math.max(0, timestamp - now) : undefined;
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set('Accept', 'application/json');
  addCSRFHeader(headers, options.method);
  let body: BodyInit | undefined;
  if (options.body !== undefined) {
    if (options.body instanceof FormData || typeof options.body === 'string') body = options.body;
    else { headers.set('Content-Type', 'application/json'); body = JSON.stringify(options.body); }
  }
  const response = await fetch(apiURL(path), { ...options, headers, body, credentials: 'include' });
  const contentType = response.headers.get('content-type') || '';
  const payload: unknown = response.status === 204 ? undefined : contentType.includes('json') ? await response.json().catch(() => undefined) : await response.text().catch(() => undefined);
  if (!response.ok) {
    if (response.status === 401 && !options.suppressUnauthorized) window.dispatchEvent(new CustomEvent('moina:unauthorized'));
    const code = payload && typeof payload === 'object' && typeof (payload as Record<string, unknown>).code === 'string' ? String((payload as Record<string, unknown>).code) : 'request_failed';
    throw new ApiError(
      errorMessage(payload, `요청을 처리하지 못했습니다. (${response.status})`),
      response.status,
      code,
      payload,
      retryAfterMilliseconds(response.headers.get('retry-after')),
    );
  }
  const method = (options.method || 'GET').toUpperCase();
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent('moina:api-mutated', { detail: { path, method } }));
  }
  if (payload && typeof payload === 'object' && 'data' in payload) return (payload as { data: T }).data;
  return payload as T;
}

export function readableError(error: unknown) {
  if (error instanceof DOMException && error.name === 'AbortError') return '요청을 취소했습니다.';
  if (error instanceof ApiError) return error.message;
  if (error instanceof TypeError) return '서비스에 연결할 수 없습니다. 서버 상태를 확인해 주세요.';
  return error instanceof Error ? error.message : '알 수 없는 오류가 발생했습니다.';
}

export interface StreamEvent { type: string; delta?: string; done?: boolean; error?: string; data?: unknown }

export function parseSSE(block: string): StreamEvent | null {
  let type = 'message';
  const data: string[] = [];
  for (const raw of block.split('\n')) {
    if (raw.startsWith('event:')) type = raw.slice(6).trim();
    if (raw.startsWith('data:')) data.push(raw.slice(5).trimStart());
  }
  const text = data.join('\n').trim();
  if (!text) return null;
  if (text === '[DONE]') return { type, done: true };
  try {
    const parsed = JSON.parse(text) as Record<string, unknown>;
    const choices = Array.isArray(parsed.choices) ? parsed.choices as Array<Record<string, unknown>> : [];
    const choiceDelta = choices[0]?.delta && typeof choices[0].delta === 'object' ? (choices[0].delta as Record<string, unknown>).content : undefined;
    const delta = parsed.delta ?? parsed.content ?? parsed.text ?? choiceDelta;
    const resolvedType = typeof parsed.type === 'string' ? parsed.type : type;
    const failed = resolvedType.includes('error') || resolvedType.endsWith('.failed') || parsed.error != null;
    return { type: resolvedType, delta: typeof delta === 'string' ? delta : undefined, done: parsed.done === true || choices[0]?.finish_reason != null, error: failed ? errorMessage(parsed, 'AI 응답 중 오류가 발생했습니다.') : undefined, data: parsed };
  } catch { return type.includes('error') ? { type, error: text } : { type, delta: text }; }
}

export async function streamRequest(path: string, payload: unknown, onEvent: (event: StreamEvent) => void, signal?: AbortSignal) {
  const headers = new Headers({ Accept: 'text/event-stream', 'Content-Type': 'application/json' });
  addCSRFHeader(headers, 'POST');
  const response = await fetch(apiURL(path), { method: 'POST', credentials: 'include', headers, body: JSON.stringify(payload), signal });
  if (!response.ok) {
    const error = await response.json().catch(() => undefined);
    if (response.status === 401) window.dispatchEvent(new CustomEvent('moina:unauthorized'));
    throw new ApiError(errorMessage(error, `AI 응답을 시작하지 못했습니다. (${response.status})`), response.status);
  }
  if (!response.body) throw new ApiError('이 브라우저는 스트리밍 응답을 지원하지 않습니다.', 0);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  const deliver = (block: string) => { const event = parseSSE(block); if (!event) return; if (event.error) throw new ApiError(event.error, 502); onEvent(event); };
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n');
    const blocks = buffer.split('\n\n');
    buffer = blocks.pop() || '';
    blocks.forEach(deliver);
  }
  buffer += decoder.decode();
  if (buffer.trim()) deliver(buffer);
}

export function websocketURL(path = '/ws/notifications') {
  const url = new URL(apiURL(path), window.location.origin);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  return url.toString();
}
