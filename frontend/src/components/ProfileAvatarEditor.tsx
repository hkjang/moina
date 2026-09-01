import { ClipboardPaste, ImagePlus, LoaderCircle, Trash2, Upload } from 'lucide-react';
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ClipboardEvent,
  type DragEvent,
} from 'react';
import { apiRequest, readableError } from '../api/client';
import { useApiQuery } from '../hooks/useApiQuery';
import { clipboardImages, IMAGE_ACCEPT, IMAGE_TYPES } from '../utils/media';
import { useToast } from './ToastProvider';
import { Avatar, Button } from './ui';

interface MediaConfig {
  maxUploadBytes?: number;
  acceptedTypes?: string[];
}

interface UploadedMedia {
  id: string;
  url?: string;
}

type UploadStatus = 'idle' | 'uploading' | 'ready' | 'error';

function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes.toLocaleString('ko-KR')}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toLocaleString('ko-KR', { maximumFractionDigits: 1 })}KiB`;
  return `${(bytes / (1024 * 1024)).toLocaleString('ko-KR', { maximumFractionDigits: 1 })}MiB`;
}

export function ProfileAvatarEditor({
  name,
  initialAvatarId,
  initialAvatarUrl,
  value,
  disabled,
  onChange,
  onBusyChange,
}: {
  name: string;
  initialAvatarId: string;
  initialAvatarUrl: string;
  value: string;
  disabled?: boolean;
  onChange: (avatarId: string) => void;
  onBusyChange?: (busy: boolean) => void;
}) {
  const { notify } = useToast();
  const hintID = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const draftID = useRef('');
  const previewObjectURL = useRef('');
  const activeUploadPreview = useRef('');
  const uploadGeneration = useRef(0);
  const uploadController = useRef<AbortController | null>(null);
  const config = useApiQuery<MediaConfig>('/media/config');
  const [preview, setPreview] = useState(initialAvatarUrl);
  const [status, setStatus] = useState<UploadStatus>('idle');
  const [error, setError] = useState('');
  const [dragging, setDragging] = useState(false);
  const busy = status === 'uploading';
  const maxUploadBytes = Math.max(1, config.data?.maxUploadBytes || 10 * 1024 * 1024);
  const acceptedTypes = new Set(
    (config.data?.acceptedTypes || [...IMAGE_TYPES]).filter((type) => IMAGE_TYPES.has(type)),
  );

  useEffect(() => {
    onBusyChange?.(busy);
  }, [busy, onBusyChange]);

  useEffect(() => {
    if (draftID.current && initialAvatarId === draftID.current) {
      draftID.current = '';
      if (previewObjectURL.current) URL.revokeObjectURL(previewObjectURL.current);
      previewObjectURL.current = '';
      setPreview(initialAvatarUrl);
      setStatus('idle');
      setError('');
      return;
    }
    if (!draftID.current && value === initialAvatarId) setPreview(initialAvatarUrl);
  }, [initialAvatarId, initialAvatarUrl, value]);

  useEffect(() => () => {
    uploadGeneration.current += 1;
    uploadController.current?.abort();
    if (activeUploadPreview.current) URL.revokeObjectURL(activeUploadPreview.current);
    if (previewObjectURL.current) URL.revokeObjectURL(previewObjectURL.current);
    if (draftID.current) {
      void apiRequest(`/media/${encodeURIComponent(draftID.current)}`, { method: 'DELETE' }).catch(() => undefined);
    }
  }, []);

  const removeDraft = (id: string) => {
    if (!id) return;
    void apiRequest(`/media/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => undefined);
  };

  const upload = async (file: File) => {
    if (disabled || busy) return;
    if (!config.data || config.loading || config.backgroundLoading) {
      notify(config.error ? '이미지 업로드 설정을 불러오지 못했습니다. 다시 시도해 주세요.' : '이미지 업로드 설정을 확인 중입니다. 잠시 후 다시 시도해 주세요.', config.error ? 'error' : 'info');
      return;
    }
    if (!acceptedTypes.has(file.type) || file.size < 1) {
      notify('비어 있지 않은 JPEG, PNG, GIF 또는 WebP 이미지를 선택해 주세요.', 'error');
      return;
    }
    if (file.size > maxUploadBytes) {
      notify(`프로필 이미지는 ${fileSize(maxUploadBytes)} 이하만 업로드할 수 있습니다.`, 'error');
      return;
    }

    const generation = ++uploadGeneration.current;
    const controller = new AbortController();
    uploadController.current = controller;
    const priorPreview = preview;
    const priorDraftID = draftID.current;
    const priorObjectURL = previewObjectURL.current;
    const localPreview = URL.createObjectURL(file);
    activeUploadPreview.current = localPreview;
    setPreview(localPreview);
    setStatus('uploading');
    setError('');
    try {
      const body = new FormData();
      body.append('file', file);
      const result = await apiRequest<UploadedMedia>('/media', { method: 'POST', body, signal: controller.signal });
      if (generation !== uploadGeneration.current) {
        removeDraft(result.id);
        return;
      }
      if (priorDraftID) removeDraft(priorDraftID);
      if (priorObjectURL) URL.revokeObjectURL(priorObjectURL);
      draftID.current = result.id;
      onChange(result.id);
      if (result.url) {
        URL.revokeObjectURL(localPreview);
        activeUploadPreview.current = '';
        previewObjectURL.current = '';
        setPreview(result.url);
      } else {
        activeUploadPreview.current = '';
        previewObjectURL.current = localPreview;
      }
      setStatus('ready');
      notify('프로필 이미지를 준비했습니다. 프로필 저장을 눌러 적용하세요.', 'success');
    } catch (cause) {
      if (generation !== uploadGeneration.current) return;
      URL.revokeObjectURL(localPreview);
      activeUploadPreview.current = '';
      setPreview(priorPreview);
      setStatus('error');
      setError(readableError(cause));
    } finally {
      if (uploadController.current === controller) uploadController.current = null;
      if (inputRef.current) inputRef.current.value = '';
    }
  };

  const selectFiles = (files: FileList | File[] | null, source: 'picker' | 'paste' | 'drop') => {
    const images = Array.from(files || []).filter((file) => file.type.startsWith('image/'));
    if (images.length === 0) {
      if (source !== 'picker') notify('클립보드나 끌어 놓은 항목에서 이미지를 찾지 못했습니다.', 'error');
      return;
    }
    if (images.length > 1) notify('프로필 이미지에는 첫 번째 이미지 한 장만 사용합니다.', 'info');
    void upload(images[0]);
  };

  const paste = (event: ClipboardEvent<HTMLDivElement>) => {
    const images = clipboardImages(event.clipboardData);
    if (images.length === 0) return;
    event.preventDefault();
    selectFiles(images, 'paste');
  };

  const remove = () => {
    if (disabled || busy) return;
    uploadGeneration.current += 1;
    uploadController.current?.abort();
    removeDraft(draftID.current);
    draftID.current = '';
    if (previewObjectURL.current) URL.revokeObjectURL(previewObjectURL.current);
    previewObjectURL.current = '';
    setPreview('');
    setStatus('idle');
    setError('');
    onChange('');
  };

  return <div
    className={`profile-avatar-editor${dragging ? ' is-dragging' : ''}`}
    onPaste={paste}
    onDragEnter={(event: DragEvent<HTMLDivElement>) => { if (event.dataTransfer.types.includes('Files')) { event.preventDefault(); setDragging(true); } }}
    onDragOver={(event: DragEvent<HTMLDivElement>) => { if (event.dataTransfer.types.includes('Files')) event.preventDefault(); }}
    onDragLeave={(event: DragEvent<HTMLDivElement>) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false); }}
    onDrop={(event: DragEvent<HTMLDivElement>) => { event.preventDefault(); setDragging(false); selectFiles(event.dataTransfer.files, 'drop'); }}
  >
    <button
      type="button"
      className="profile-avatar-target"
      aria-label="프로필 이미지 선택"
      aria-describedby={hintID}
      disabled={disabled || busy || config.loading}
      onClick={() => inputRef.current?.click()}
    >
      <Avatar name={name || '사용자'} src={preview || undefined} size="large"/>
      <span className="profile-avatar-camera"><ImagePlus aria-hidden="true"/></span>
    </button>
    <div className="profile-avatar-copy">
      <strong>프로필 이미지</strong>
      <p id={hintID}>정사각형 이미지가 가장 자연스럽습니다. 선택하거나 끌어 놓고, 캡처한 이미지는 이 영역에서 Ctrl+V로 붙여 넣으세요.</p>
      <small>JPEG, PNG, GIF, WebP · 최대 {fileSize(maxUploadBytes)}</small>
      <div className="profile-avatar-actions">
        <Button type="button" size="small" onClick={() => inputRef.current?.click()} disabled={disabled || busy || config.loading}>
          <Upload aria-hidden="true"/>{preview ? '이미지 교체' : '이미지 선택'}
        </Button>
        <span className="profile-paste-hint"><ClipboardPaste aria-hidden="true"/>Ctrl+V</span>
        {(preview || value) && <Button type="button" size="small" variant="ghost" onClick={remove} disabled={disabled || busy}>
          <Trash2 aria-hidden="true"/>이미지 제거
        </Button>}
      </div>
      {status === 'uploading' && <span className="profile-avatar-status" role="status"><LoaderCircle className="spin" aria-hidden="true"/>이미지 업로드 중…</span>}
      {status === 'ready' && <span className="profile-avatar-status positive" role="status">업로드 완료 · 저장하면 전체 화면에 반영됩니다.</span>}
      {status === 'error' && <span className="field-error" role="alert">업로드 실패: {error}</span>}
    </div>
    <input
      ref={inputRef}
      className="sr-only"
      type="file"
      accept={IMAGE_ACCEPT}
      aria-label="프로필 이미지 파일"
      tabIndex={-1}
      onChange={(event) => selectFiles(event.target.files, 'picker')}
    />
  </div>;
}
