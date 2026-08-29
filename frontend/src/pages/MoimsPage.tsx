import { ArrowRight, Network, Plus, Users } from 'lucide-react';
import { Link, useParams } from 'react-router-dom';
import { useState, type FormEvent } from 'react';
import { useAuth } from '../auth/AuthContext';
import { apiRequest, readableError } from '../api/client';
import { useToast } from '../components/ToastProvider';
import { normalizeMoin, normalizePage, normalizeTopic } from '../api/adapters';
import { MoinCard } from '../components/MoinCard';
import { Badge, Button, Card, EmptyState, ErrorState, Field, LoadingState, Modal, PageHeader } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import type { Moim } from '../types';
import { topicLabel } from '../utils/format';

function normalizeMoim(value: unknown): Moim { const raw = value as Record<string, unknown>; return { id: String(raw.id || raw.slug), slug: String(raw.slug || raw.id), name: String(raw.name || '이름 없는 모임'), description: typeof raw.description === 'string' ? raw.description : undefined, memberCount: Number(raw.memberCount || 0), moinCount: Number(raw.moinCount || raw.postCount || 0), joined: raw.joined === true, ownerId: typeof raw.ownerId === 'string' ? raw.ownerId : undefined, avatarUrl: typeof raw.avatarUrl === 'string' ? raw.avatarUrl : undefined, topics: Array.isArray(raw.topics) ? raw.topics.map(normalizeTopic) : [] }; }

export function MoimsPage() {
  const query = useApiQuery<unknown>('/moims?limit=50');
  const items = query.data === undefined ? [] : normalizePage(query.data, normalizeMoim).items;
  const { notify } = useToast(); const [open, setOpen] = useState(false); const [saving, setSaving] = useState(false); const [draft, setDraft] = useState({ name: '', slug: '', description: '' });
  const create = async (event: FormEvent) => { event.preventDefault(); setSaving(true); try { await apiRequest('/moims', { method: 'POST', body: draft }); notify('새 모임을 만들었습니다.', 'success'); setOpen(false); setDraft({ name: '', slug: '', description: '' }); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setSaving(false); } };
  return <div className="page-stack"><PageHeader eyebrow="COMMUNITIES" title="Moim" description="관심사를 중심으로 사람들이 함께 배우고 대화하는 공간입니다." actions={<Button variant="primary" onClick={() => setOpen(true)}><Plus/>모임 만들기</Button>}/>{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : items.length ? <div className="moim-grid">{items.map((moim) => <Card className="moim-card" key={moim.id}><Link to={`/moims/${encodeURIComponent(moim.slug)}`}><span className="moim-symbol"><Network/></span><span><strong>{moim.name}</strong><p>{moim.description || '함께 지식을 나누는 모임입니다.'}</p><small><Users/>{moim.memberCount?.toLocaleString('ko-KR')}명 · {moim.moinCount?.toLocaleString('ko-KR')} Moin</small></span><ArrowRight/></Link><div className="topic-row">{moim.topics?.map((topic) => <Badge key={topic.id}>{topicLabel(topic.name)}</Badge>)}</div></Card>)}</div> : <EmptyState title="아직 참여할 모임이 없습니다" description="첫 모임을 만들어 관심사 대화를 시작해 보세요." action={<Button variant="primary" onClick={() => setOpen(true)}>첫 모임 만들기</Button>}/>}<Modal open={open} onOpenChange={setOpen} title="새 Moim 만들기" description="관심사를 분명하게 드러내는 이름과 주소를 정하세요."><form className="settings-form" onSubmit={create}><Field label="모임 이름"><input required maxLength={80} value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })}/></Field><Field label="고유 주소" help="영문 소문자·숫자로 시작하는 3~50자이며 하이픈을 사용할 수 있습니다."><input required minLength={3} maxLength={50} pattern="[a-z0-9][a-z0-9-]{2,49}" value={draft.slug} onChange={(event) => setDraft({ ...draft, slug: event.target.value.toLowerCase().replace(/[^a-z0-9-]/g, '') })} placeholder="go-developers"/></Field><Field label="모임 소개"><textarea required rows={4} maxLength={500} value={draft.description} onChange={(event) => setDraft({ ...draft, description: event.target.value })}/></Field><div className="form-actions"><Button type="button" onClick={() => setOpen(false)}>취소</Button><Button type="submit" variant="primary" disabled={saving}>{saving ? '만드는 중…' : '모임 만들기'}</Button></div></form></Modal></div>;
}

export function MoimDetailPage() {
  const { user } = useAuth();
  const { slug = '' } = useParams();
  const query = useApiQuery<unknown>(`/moims/${encodeURIComponent(slug)}`);
  const posts = useApiQuery<unknown>(`/posts?moim=${encodeURIComponent(slug)}&limit=30`);
  const moim = query.data ? normalizeMoim(query.data) : undefined;
  const items = posts.data === undefined ? [] : normalizePage(posts.data, normalizeMoin).items;
  const { notify } = useToast(); const [joining, setJoining] = useState(false);
  const owner = Boolean(moim?.ownerId && moim.ownerId === user?.id);
  const join = async () => { if (!moim) return; setJoining(true); try { await apiRequest(`/moims/${encodeURIComponent(moim.slug)}/members`, { method: moim.joined ? 'DELETE' : 'POST' }); notify(moim.joined ? '모임에서 나왔습니다.' : '모임에 참여했습니다.', 'success'); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setJoining(false); } };
  return <div className="page-stack">{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : moim && <div className="moim-hero"><span className="moim-symbol"><Network/></span><div><p className="eyebrow">MOIM</p><h1>{moim.name}</h1><p>{moim.description}</p><span>{moim.memberCount?.toLocaleString('ko-KR')}명 참여 · {moim.moinCount?.toLocaleString('ko-KR')} Moin</span></div><Button variant={moim.joined ? 'secondary' : 'primary'} onClick={() => void join()} disabled={joining || owner}>{owner ? '소유자' : joining ? '처리 중…' : moim.joined ? '참여 중' : '참여하기'}</Button></div>}{posts.loading ? <LoadingState/> : posts.error ? <ErrorState message={posts.error} onRetry={posts.reload}/> : items.length ? <div className="feed-list">{items.map((moin) => <MoinCard key={moin.id} moin={moin}/>)}</div> : <EmptyState title="아직 모인이 없습니다" description="이 모임의 첫 대화를 시작해 보세요."/>}</div>;
}
