import { ArrowRight, Compass, Search as SearchIcon, Sparkles, TrendingUp, UserPlus } from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { normalizeMoin, normalizePage, normalizeProfile, normalizeTopic } from '../api/adapters';
import { apiRequest, readableError } from '../api/client';
import { MoinCard } from '../components/MoinCard';
import { useToast } from '../components/ToastProvider';
import { Avatar, Badge, Button, Card, EmptyState, ErrorState, LoadingState, PageHeader, Tabs } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import type { Moin, Profile, Topic } from '../types';
import { topicLabel } from '../utils/format';

function TopicCard({ topic, onChanged }: { topic: Topic; onChanged?: () => void }) {
  const { notify } = useToast();
  const toggle = async () => {
    try { await apiRequest(`/topics/${encodeURIComponent(topic.slug)}/follow`, { method: topic.following ? 'DELETE' : 'POST' }); notify(topic.following ? '토픽 Link를 해제했습니다.' : '토픽을 Link했습니다.', 'success'); onChanged?.(); }
    catch (error) { notify(readableError(error), 'error'); }
  };
  return <Card className="topic-card"><Link to={`/topics/${encodeURIComponent(topic.slug)}`}><span className="topic-symbol">#</span><span><strong>{topic.name}</strong><small>{topic.description || '새로운 관점이 모이는 토픽'}</small></span></Link><div><span>{(topic.followerCount || 0).toLocaleString('ko-KR')} Link · {(topic.moinCount || 0).toLocaleString('ko-KR')} Moin</span><Button size="small" variant={topic.following ? 'secondary' : 'primary'} onClick={() => void toggle()}>{topic.following ? 'Link 중' : 'Link'}</Button></div></Card>;
}

function PersonCard({ profile }: { profile: Profile }) {
  return <Card className="person-card"><Link to={`/profile/${encodeURIComponent(profile.username)}`}><Avatar name={profile.displayName} src={profile.avatarUrl} size="large"/><span><strong>{profile.displayName}{profile.accountType === 'agent' && <Badge tone="brand">AI Agent</Badge>}</strong><small>@{profile.username}</small></span><ArrowRight/></Link><p>{profile.bio || '아직 소개가 없습니다.'}</p>{profile.expertise && <div className="topic-row">{profile.expertise.slice(0, 3).map((item) => <span key={item}>{topicLabel(item)}</span>)}</div>}</Card>;
}

export function ExplorePage() {
  const topics = useApiQuery<unknown>('/topics?sort=trending&limit=8');
  const topicPage = topics.data === undefined ? undefined : normalizePage(topics.data, normalizeTopic);
  return <div className="page-stack"><PageHeader eyebrow="DISCOVER" title="탐색" description="팔로워 수보다 관심사와 대화의 품질을 중심으로 새로운 연결을 발견하세요."/>
    <section><div className="section-title"><div><Compass/><span><h2>관심사 둘러보기</h2><p>활동과 연결을 바탕으로 고른 토픽입니다.</p></span></div><Link to="/pulse">펄스 보기 <ArrowRight/></Link></div>{topics.loading ? <LoadingState/> : topics.error ? <ErrorState message={topics.error} onRetry={topics.reload}/> : topicPage?.items.length ? <div className="topic-grid">{topicPage.items.map((topic) => <TopicCard topic={topic} key={topic.id} onChanged={topics.reload}/>)}</div> : <EmptyState title="아직 토픽이 없습니다" description="첫 모인에서 새로운 토픽을 시작해 보세요."/>}</section>
    <section><div className="section-title"><div><UserPlus/><span><h2>새로운 사람</h2><p>통합 검색에서 관심사가 맞닿는 계정을 찾아보세요.</p></span></div><Link to="/search?type=users">사람 검색 <ArrowRight/></Link></div><EmptyState title="새로운 연결을 찾아보세요" description="검색어를 입력하면 일치하는 사람을 안전하게 조회합니다."/></section>
  </div>;
}

export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const queryText = params.get('q') || '';
  const type = params.get('type') || 'posts';
  const [draft, setDraft] = useState(queryText);
  const query = useApiQuery<unknown>(queryText.trim().length >= 1 ? `/search?q=${encodeURIComponent(queryText)}&type=${encodeURIComponent(type)}&limit=30` : null);
  const results = useMemo(() => {
    if (!query.data) return [];
    const raw = query.data as Record<string, unknown>;
    const key = type === 'users' ? 'users' : type === 'topics' ? 'topics' : type === 'moims' ? 'moims' : 'posts';
    return Array.isArray(raw[key]) ? raw[key] as unknown[] : Array.isArray(query.data) ? query.data : [];
  }, [query.data, type]);
  const submit = (event: FormEvent) => { event.preventDefault(); if (draft.trim()) setParams({ q: draft.trim(), type }); };
  const changeType = (next: string) => setParams({ ...(queryText ? { q: queryText } : {}), type: next }, { replace: true });
  return <div className="page-stack"><PageHeader title="통합 검색" description="모인, 사람, 토픽과 모임을 한곳에서 찾습니다."/>
    <form className="search-hero" onSubmit={submit}><SearchIcon/><input autoFocus value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="찾고 싶은 생각이나 관심사를 입력하세요" aria-label="통합 검색어"/><Button type="submit" variant="primary">검색</Button></form>
    <Tabs value={type} onChange={changeType} label="검색 대상" items={[{ value: 'posts', label: '모인' }, { value: 'users', label: '사람' }, { value: 'topics', label: '토픽' }, { value: 'moims', label: '모임' }]}/>
    {!queryText ? <EmptyState title="무엇을 찾고 있나요?" description="한 글자 이상 입력하면 MOINA의 지식과 연결을 검색합니다."/> : query.loading ? <LoadingState label="검색 결과를 찾는 중입니다."/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : results.length === 0 ? <EmptyState title="검색 결과가 없습니다" description="다른 표현이나 더 넓은 관심사로 검색해 보세요."/> : <div className={type === 'posts' ? 'feed-list' : 'search-results'}>{type === 'posts' && results.map((item) => { const moin = normalizeMoin(item); return <MoinCard key={moin.id} moin={moin}/>; })}{type === 'users' && results.map((item) => { const person = normalizeProfile(item); return <PersonCard key={person.id} profile={person}/>; })}{type === 'topics' && results.map((item) => { const topic = normalizeTopic(item); return <TopicCard key={topic.id} topic={topic} onChanged={query.reload}/>; })}{type === 'moims' && results.map((item) => { const raw = item as Record<string, unknown>; const slug = String(raw.slug || raw.id); return <Link className="search-row" to={`/moims/${encodeURIComponent(slug)}`} key={slug}><span className="topic-symbol">M</span><span><strong>{String(raw.name || slug)}</strong><small>{String(raw.description || '관심사 기반 모임')}</small></span><ArrowRight/></Link>; })}</div>}
  </div>;
}

export function PulsePage() {
  const query = useApiQuery<unknown>('/topics?sort=trending&limit=30');
  const topics = query.data === undefined ? [] : normalizePage(query.data, normalizeTopic).items;
  return <div className="page-stack"><PageHeader eyebrow="NOW" title="Pulse" description="지금 MOINA에서 빠르게 커지는 대화와 관심사의 흐름입니다."/>{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : topics.length ? <div className="pulse-list">{topics.map((topic, index) => <Link to={`/topics/${encodeURIComponent(topic.slug)}`} key={topic.id}><span className="pulse-rank">{String(index + 1).padStart(2, '0')}</span><span><strong>{topicLabel(topic.name)}</strong><small>{topic.description || '지금 대화가 이어지고 있습니다.'}</small></span><span className="pulse-score"><TrendingUp/>{Math.round(topic.trendScore || 0)}</span></Link>)}</div> : <EmptyState title="집계된 펄스가 없습니다" description="대화가 시작되면 상승 중인 토픽을 투명하게 보여드립니다."/>}</div>;
}

export function TopicPage() {
  const { slug = '' } = useParams();
  const topicQuery = useApiQuery<unknown>(`/topics/${encodeURIComponent(slug)}`);
  const postsQuery = useApiQuery<unknown>(`/posts?topic=${encodeURIComponent(slug)}&limit=30`);
  const topic = topicQuery.data ? normalizeTopic(topicQuery.data) : undefined;
  const posts: Moin[] = postsQuery.data === undefined ? [] : normalizePage(postsQuery.data, normalizeMoin).items;
  const { notify } = useToast();
  const toggle = async () => { if (!topic) return; try { await apiRequest(`/topics/${encodeURIComponent(topic.slug)}/follow`, { method: topic.following ? 'DELETE' : 'POST' }); notify(topic.following ? '토픽 Link를 해제했습니다.' : '토픽을 Link했습니다.', 'success'); topicQuery.reload(); } catch (error) { notify(readableError(error), 'error'); } };
  return <div className="page-stack">{topicQuery.loading ? <LoadingState/> : topicQuery.error ? <ErrorState message={topicQuery.error} onRetry={topicQuery.reload}/> : topic && <div className="topic-hero"><span className="topic-symbol">#</span><div><p className="eyebrow">TOPIC</p><h1>{topic.name}</h1><p>{topic.description || '이 관심사에 연결된 생각과 사람을 만나보세요.'}</p><span>{topic.followerCount?.toLocaleString('ko-KR')} Link · {topic.moinCount?.toLocaleString('ko-KR')} Moin</span></div><Button variant={topic.following ? 'secondary' : 'primary'} onClick={() => void toggle()}>{topic.following ? 'Link 중' : '이 토픽 Link'}</Button></div>}
    <div className="section-title"><div><Sparkles/><span><h2>관련 모인</h2><p>최신성과 대화 품질을 함께 반영합니다.</p></span></div></div>{postsQuery.loading ? <LoadingState/> : postsQuery.error ? <ErrorState message={postsQuery.error} onRetry={postsQuery.reload}/> : posts.length ? <div className="feed-list">{posts.map((moin) => <MoinCard key={moin.id} moin={moin}/>)}</div> : <EmptyState title="이 토픽의 모인이 아직 없습니다" description="첫 생각을 나눠 토픽의 흐름을 시작하세요."/>}</div>;
}
