import { describe, expect, it } from 'vitest';
import { clipboardImages, IMAGE_ACCEPT, MEDIA_ACCEPT, mediaTypeFor, uploadStatusLabel } from './media';

describe('composer media contract', () => {
  it.each(['image/jpeg', 'image/png', 'image/gif', 'image/webp'])('%s를 이미지로 허용한다', (type) => expect(mediaTypeFor({ type })).toBe('image'));
  it.each(['video/mp4', 'video/webm'])('%s를 영상으로 허용한다', (type) => expect(mediaTypeFor({ type })).toBe('video'));
  it('서버 미지원 MIME은 거절한다', () => expect(mediaTypeFor({ type: 'video/quicktime' })).toBeUndefined());
  it('file accept가 서버 MIME 목록을 모두 포함한다', () => expect(MEDIA_ACCEPT.split(',')).toEqual(['image/jpeg', 'image/png', 'image/gif', 'image/webp', 'video/mp4', 'video/webm']));
  it('프로필 accept는 image MIME만 포함한다', () => expect(IMAGE_ACCEPT.split(',')).toEqual(['image/jpeg', 'image/png', 'image/gif', 'image/webp']));
  it('clipboard item에서 이미지만 추출한다', () => {
    const image = new File(['image'], 'capture.png', { type: 'image/png' });
    const text = new File(['text'], 'memo.txt', { type: 'text/plain' });
    const clipboard = {
      items: [
        { kind: 'file', type: image.type, getAsFile: () => image },
        { kind: 'file', type: text.type, getAsFile: () => text },
      ],
      files: [image, text],
    } as unknown as DataTransfer;
    expect(clipboardImages(clipboard)).toEqual([image]);
  });
  it('업로드 상태를 한국어로 제공한다', () => expect(uploadStatusLabel('cancelled')).toBe('업로드 취소됨'));
});
