export const MEDIA_ACCEPT = 'image/jpeg,image/png,image/gif,image/webp,video/mp4,video/webm';
export const MEDIA_TYPES = new Set(MEDIA_ACCEPT.split(','));

export type ComposerMediaType = 'image' | 'video';
export type ComposerUploadStatus = 'queued' | 'uploading' | 'uploaded' | 'error' | 'cancelled';

export function mediaTypeFor(file: Pick<File, 'type'>): ComposerMediaType | undefined {
  if (!MEDIA_TYPES.has(file.type)) return undefined;
  return file.type.startsWith('video/') ? 'video' : 'image';
}

export function uploadStatusLabel(status: ComposerUploadStatus) {
  if (status === 'queued') return '업로드 대기';
  if (status === 'uploading') return '업로드 중';
  if (status === 'uploaded') return '업로드 완료';
  if (status === 'cancelled') return '업로드 취소됨';
  return '업로드 실패';
}
