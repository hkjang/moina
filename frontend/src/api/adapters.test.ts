import { describe, expect, it } from 'vitest';
import { normalizeMoin, normalizePage, normalizeProfile, normalizeTopic } from './adapters';

describe('백엔드 응답 어댑터', () => {
  it('평탄한 사용자 응답을 프로필 UI 타입으로 변환한다', () => {
    expect(normalizeProfile({ id: 'u1', username: 'jang', displayName: '장', avatarId: 'media-1', following: true })).toMatchObject({
      id: 'u1', username: 'jang', displayName: '장', avatarUrl: '/api/v1/media/media-1', followed: true,
    });
  });

  it('평탄한 게시물 카운트와 Signal을 보존한다', () => {
    const moin = normalizeMoin({
      id: 'p1', content: 'Go와 AI', authorId: 'u1', authorUsername: 'jang',
      replyCount: 7, remoinCount: 2, bookmarkCount: 1, signals: { likes: 3, useful: 5 },
    });
    expect(moin.author.username).toBe('jang');
    expect(moin.counts).toMatchObject({ echoes: 7, remoins: 2, bookmarks: 1, signals: { like: 3, useful: 5 } });
  });

  it('토픽 표시에서 중복 #을 제거하고 페이지 래퍼를 읽는다', () => {
    const page = normalizePage({ items: [{ id: 't1', slug: 'go', name: '##Go' }], nextCursor: 'next' }, normalizeTopic);
    expect(page.items[0].name).toBe('Go');
    expect(page.nextCursor).toBe('next');
  });

  it('내용이 비어 있는 Remoin도 중첩 원문을 보존한다', () => {
    const moin = normalizeMoin({ id: 'r1', kind: 'remoin', author: { id: 'u1', username: 'kim' }, remoinMoin: { id: 'p1', content: '원문', author: { id: 'u2', username: 'jang' } } });
    expect(moin.kind).toBe('remoin');
    expect(moin.quoteMoin).toMatchObject({ id: 'p1', content: '원문' });
  });
});
