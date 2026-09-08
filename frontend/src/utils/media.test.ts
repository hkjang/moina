import { describe, expect, it } from 'vitest';
import {
  clipboardImages,
  HEIC_GUIDANCE,
  IMAGE_ACCEPT,
  isHEIC,
  MEDIA_ACCEPT,
  mediaTypeFor,
  unsupportedMediaMessage,
  uploadStatusLabel,
} from './media';

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

describe('HEIC 안내', () => {
  const fallback = '비어 있지 않은 JPEG, PNG, GIF, WebP 이미지 또는 MP4, WebM 영상만 첨부할 수 있습니다.';

  it('HEIC MIME을 인식한다', () => expect(isHEIC({ name: 'IMG_0001', type: 'image/heic' })).toBe(true));
  it('대문자 MIME도 인식한다', () => expect(isHEIC({ name: 'IMG_0001', type: 'IMAGE/HEIF' })).toBe(true));
  it('MIME을 모르는 브라우저를 위해 확장자도 본다', () =>
    expect(isHEIC({ name: 'IMG_0001.HEIC', type: '' })).toBe(true));
  it('HEIC이 아닌 파일은 인식하지 않는다', () =>
    expect(isHEIC({ name: 'memo.txt', type: 'text/plain' })).toBe(false));
  it('HEIC은 첨부 형식 목록 대신 다시 시도할 방법을 안내한다', () =>
    expect(unsupportedMediaMessage([{ name: 'IMG_0001.heic', type: '' }], fallback)).toBe(HEIC_GUIDANCE));
  it('HEIC이 섞여 있으면 두 안내를 함께 보여 준다', () =>
    expect(
      unsupportedMediaMessage(
        [
          { name: 'IMG_0001.heic', type: 'image/heic' },
          { name: 'memo.txt', type: 'text/plain' },
        ],
        fallback,
      ),
    ).toBe(`${fallback} ${HEIC_GUIDANCE}`));
  it('HEIC이 없으면 기존 안내를 유지한다', () =>
    expect(unsupportedMediaMessage([{ name: 'memo.txt', type: 'text/plain' }], fallback)).toBe(fallback));
  it('거절한 파일이 없으면 기존 안내를 유지한다', () =>
    expect(unsupportedMediaMessage([], fallback)).toBe(fallback));
});
