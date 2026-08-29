import { describe, expect, it } from 'vitest';
import { endpointAuthority, ensureEndpointHost, formatAllowedHosts, parseAllowedHosts } from './allowedHosts';

describe('allowed host editor', () => {
  it('줄바꿈과 쉼표를 중복 없이 정규화한다', () => expect(parseAllowedHosts('AI.INTERNAL:8443, keycloak.internal\nai.internal:8443')).toEqual(['ai.internal:8443', 'keycloak.internal']));
  it('비기본 port와 IPv6 bracket을 유지한다', () => {
    expect(endpointAuthority('https://ai.internal:8443/v1')).toBe('ai.internal:8443');
    expect(endpointAuthority('https://[2001:db8::1]:9443/v1')).toBe('[2001:db8::1]:9443');
    expect(endpointAuthority('https://ai.internal:443/v1')).toBe('ai.internal');
  });
  it('endpoint authority가 없을 때 정확한 port 범위로 자동 추가한다', () => expect(ensureEndpointHost(['ai.internal'], 'https://ai.internal:8443/v1')).toEqual({ hosts: ['ai.internal', 'ai.internal:8443'], invalid: false, added: 'ai.internal:8443' }));
  it('잘못된 URL을 저장 전에 검출한다', () => expect(ensureEndpointHost([], 'not a url').invalid).toBe(true));
  it('서버 배열을 줄 단위 편집 문자열로 바꾼다', () => expect(formatAllowedHosts(['a.internal', 'b.internal:8080'])).toBe('a.internal\nb.internal:8080'));
});
