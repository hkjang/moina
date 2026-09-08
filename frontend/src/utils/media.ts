export const MEDIA_ACCEPT = 'image/jpeg,image/png,image/gif,image/webp,video/mp4,video/webm';
export const MEDIA_TYPES = new Set(MEDIA_ACCEPT.split(','));
export const IMAGE_ACCEPT = 'image/jpeg,image/png,image/gif,image/webp';
export const IMAGE_TYPES = new Set(IMAGE_ACCEPT.split(','));

export type ComposerMediaType = 'image' | 'video';
export type ComposerUploadStatus = 'queued' | 'uploading' | 'uploaded' | 'error' | 'cancelled';

export function mediaTypeFor(file: Pick<File, 'type'>): ComposerMediaType | undefined {
  if (!MEDIA_TYPES.has(file.type)) return undefined;
  return file.type.startsWith('video/') ? 'video' : 'image';
}

const HEIC_TYPES = new Set(['image/heic', 'image/heif', 'image/heic-sequence', 'image/heif-sequence']);
const HEIC_EXTENSIONS = ['.heic', '.heif'];

export const HEIC_GUIDANCE =
  'HEIC/HEIF 사진은 아직 첨부할 수 없습니다. iPhone은 설정 > 카메라 > 포맷에서 ‘높은 호환성’을 선택하면 앞으로 찍는 사진이 JPEG으로 저장되고, 이미 찍은 사진은 JPEG으로 내보낸 뒤 첨부해 주세요.';

// 브라우저가 HEIC의 MIME을 모르면 type이 빈 문자열이나 application/octet-stream으로
// 오므로 확장자도 함께 본다. 지원 형식 판정에서 이미 거절된 파일에만 쓴다.
export function isHEIC(file: Pick<File, 'name' | 'type'>) {
  if (HEIC_TYPES.has(file.type.toLowerCase())) return true;
  const name = file.name.toLowerCase();
  return HEIC_EXTENSIONS.some((extension) => name.endsWith(extension));
}

// 아이폰 기본 촬영 형식인 HEIC은 형식 목록만 보여 주면 왜 거절됐는지 알기 어려우므로
// 거절한 파일이 HEIC이면 다시 시도할 방법을 함께 안내한다.
export function unsupportedMediaMessage(files: Pick<File, 'name' | 'type'>[], fallback: string) {
  const heic = files.filter(isHEIC);
  if (heic.length === 0) return fallback;
  if (heic.length === files.length) return HEIC_GUIDANCE;
  return `${fallback} ${HEIC_GUIDANCE}`;
}

export function clipboardImages(clipboardData: DataTransfer | null) {
  if (!clipboardData) return [];
  const itemFiles = Array.from(clipboardData.items || [])
    .filter((item) => item.kind === 'file' && item.type.startsWith('image/'))
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file));
  return (itemFiles.length > 0 ? itemFiles : Array.from(clipboardData.files || []))
    .filter((file) => file.type.startsWith('image/'));
}

export function uploadStatusLabel(status: ComposerUploadStatus) {
  if (status === 'queued') return '업로드 대기';
  if (status === 'uploading') return '업로드 중';
  if (status === 'uploaded') return '업로드 완료';
  if (status === 'cancelled') return '업로드 취소됨';
  return '업로드 실패';
}
