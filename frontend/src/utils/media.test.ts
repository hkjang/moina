import { describe, expect, it } from 'vitest';
import { MEDIA_ACCEPT, mediaTypeFor, uploadStatusLabel } from './media';

describe('composer media contract', () => {
  it.each(['image/jpeg', 'image/png', 'image/gif', 'image/webp'])('%s를 이미지로 허용한다', (type) => expect(mediaTypeFor({ type })).toBe('image'));
  it.each(['video/mp4', 'video/webm'])('%s를 영상으로 허용한다', (type) => expect(mediaTypeFor({ type })).toBe('video'));
  it('서버 미지원 MIME은 거절한다', () => expect(mediaTypeFor({ type: 'video/quicktime' })).toBeUndefined());
  it('file accept가 서버 MIME 목록을 모두 포함한다', () => expect(MEDIA_ACCEPT.split(',' )).toEqual(['image/jpeg', 'image/png', 'image/gif', 'image/webp', 'video/mp4', 'video/webm']));
  it('업로드 상태를 한국어로 제공한다', () => expect(uploadStatusLabel('cancelled')).toBe('업로드 취소됨'));
});
