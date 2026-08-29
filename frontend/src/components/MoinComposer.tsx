import { ImagePlus, LoaderCircle, Quote, X } from 'lucide-react';
import { useRef, useState, type FormEvent } from 'react';
import { apiRequest, readableError } from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { useToast } from './ToastProvider';
import { Avatar, Button, IconButton } from './ui';
import type { Moin } from '../types';

interface MediaUpload { id: string; url?: string; name: string }

export function MoinComposer({ replyToId, quoteMoin, onClearQuote, placeholder = '지금 어떤 생각을 나누고 싶나요?', onCreated }: { replyToId?: string; quoteMoin?: Moin; onClearQuote?: () => void; placeholder?: string; onCreated: () => void }) {
  const { user } = useAuth();
  const { notify } = useToast();
  const [content, setContent] = useState('');
  const [visibility, setVisibility] = useState('public');
  const [media, setMedia] = useState<MediaUpload[]>([]);
  const [working, setWorking] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const remaining = 5000 - [...content].length;
  const upload = async (files: FileList | null) => {
    if (!files) return;
    const selected = [...files].slice(0, Math.max(0, 4 - media.length));
    setWorking(true);
    try {
      const created: MediaUpload[] = [];
      for (const file of selected) {
        const form = new FormData(); form.append('file', file);
        const result = await apiRequest<{ id: string; url?: string }>('/media', { method: 'POST', body: form });
        created.push({ ...result, name: file.name });
      }
      setMedia((current) => [...current, ...created]);
    } catch (error) { notify(readableError(error), 'error'); }
    finally { setWorking(false); if (fileRef.current) fileRef.current.value = ''; }
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!content.trim() || remaining < 0) return;
    setWorking(true);
    try {
      await apiRequest('/posts', { method: 'POST', body: { content: content.trim(), visibility, mediaIds: media.map((item) => item.id), ...(replyToId ? { replyToId } : {}), ...(quoteMoin ? { quoteMoinId: quoteMoin.id } : {}) } });
      setContent(''); setMedia([]); notify(replyToId ? '에코를 남겼습니다.' : '모인을 플로우에 보냈습니다.', 'success'); onCreated();
    } catch (error) { notify(readableError(error), 'error'); }
    finally { setWorking(false); }
  };
  return <form className="moin-composer" onSubmit={submit}>
    <Avatar name={user?.displayName || '나'} src={user?.avatarUrl}/>
    <div><textarea rows={replyToId ? 3 : 4} value={content} onChange={(event) => setContent(event.target.value)} placeholder={placeholder} aria-label={replyToId ? '에코 내용' : '모인 내용'} maxLength={5100}/>
      {quoteMoin && <div className="composer-quote"><Quote/><span><strong>{quoteMoin.author.displayName} <small>@{quoteMoin.author.username}</small></strong><p>{quoteMoin.content || '리모인한 원문'}</p></span>{onClearQuote && <IconButton type="button" label="인용 취소" onClick={onClearQuote}><X/></IconButton>}</div>}
      {media.length > 0 && <div className="composer-media">{media.map((item) => <span key={item.id}>{item.name}<IconButton type="button" label={`${item.name} 제거`} onClick={() => setMedia((current) => current.filter((entry) => entry.id !== item.id))}><X/></IconButton></span>)}</div>}
      <footer><div className="composer-tools"><input ref={fileRef} className="sr-only" id={`media-${replyToId || 'new'}`} type="file" accept="image/jpeg,image/png,image/gif,image/webp" multiple onChange={(event) => void upload(event.target.files)}/><IconButton type="button" label="이미지 첨부" onClick={() => fileRef.current?.click()} disabled={working || media.length >= 4}><ImagePlus/></IconButton><select aria-label="공개 범위" value={visibility} onChange={(event) => setVisibility(event.target.value)}><option value="public">전체 공개</option><option value="followers">연결한 사람</option></select></div><span className={remaining < 0 ? 'counter error' : 'counter'}>{remaining.toLocaleString('ko-KR')}</span><Button type="submit" variant="primary" disabled={working || !content.trim() || remaining < 0}>{working ? <><LoaderCircle className="spin"/>처리 중</> : replyToId ? '에코' : '모인하기'}</Button></footer>
    </div>
  </form>;
}
