import { PenLine, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { MoinCard } from '../components/MoinCard';
import { MoinComposer } from '../components/MoinComposer';
import { Avatar, Button, EmptyState, ErrorState, Modal, SkeletonFeed, Tabs } from '../components/ui';
import { useAuth } from '../auth/AuthContext';
import { useApiQuery } from '../hooks/useApiQuery';
import type { CursorPage, Moin } from '../types';
import { normalizeMoin, normalizePage } from '../api/adapters';

export default function FlowPage() {
  const { user } = useAuth();
  const [params, setParams] = useSearchParams();
  const mode = params.get('mode') === 'following' ? 'following' : 'for_me';
  const compose = params.get('compose') === '1';
  const quoteID = params.get('quote') || '';
  const [items, setItems] = useState<Moin[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const query = useApiQuery<unknown>(`/feed?mode=${mode}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
  const quoteQuery = useApiQuery<unknown>(compose && quoteID ? `/posts/${encodeURIComponent(quoteID)}` : null);
  const quoteMoin = quoteQuery.data ? normalizeMoin(quoteQuery.data) : undefined;
  const page: CursorPage<Moin> | undefined = query.data === undefined ? undefined : normalizePage(query.data, normalizeMoin);
  useEffect(() => { setCursor(null); setItems([]); }, [mode]);
  useEffect(() => { if (page?.items) setItems((current) => cursor ? [...current, ...page.items] : page.items); }, [cursor, query.data]);
  const setMode = (next: string) => setParams(next === 'following' ? { mode: 'following' } : {}, { replace: true });
  const clearQuote = () => { const next = new URLSearchParams(params); next.delete('quote'); setParams(next, { replace: true }); };
  const closeComposer = () => { const next = new URLSearchParams(params); next.delete('compose'); next.delete('quote'); setParams(next, { replace: true }); };
  const refresh = () => { setCursor(null); void query.reload(); };
  return <div className="feed-page">
    <header className="sticky-feed-header"><div><h1>플로우</h1><Button size="icon" variant="ghost" aria-label="피드 새로고침" onClick={refresh}><RefreshCw/></Button></div><Tabs value={mode} onChange={setMode} label="피드 선택" items={[{ value: 'for_me', label: 'For Me' }, { value: 'following', label: 'Following' }]}/></header>
    <button className="composer-prompt" type="button" onClick={() => setParams({ ...Object.fromEntries(params), compose: '1' })}><Avatar name={user?.displayName || '나'} src={user?.avatarUrl}/><span>지금 어떤 생각을 나누고 싶나요?</span><PenLine/></button>
    {query.loading && items.length === 0 ? <SkeletonFeed/> : query.error && items.length === 0 ? <ErrorState message={query.error} onRetry={query.reload}/> : items.length === 0 ? <EmptyState title={mode === 'following' ? '플로우가 아직 조용합니다' : '추천할 모인을 찾고 있습니다'} description={mode === 'following' ? '관심 있는 사람이나 토픽을 Link하면 새 모인이 여기에 흐릅니다.' : '첫 모인을 남기거나 관심 토픽을 선택해 For Me를 시작하세요.'} action={<Button variant="primary" onClick={() => setParams({ compose: '1', ...(mode === 'following' ? { mode } : {}) })}>첫 모인 작성</Button>}/> : <div className="feed-list">{items.map((moin) => <MoinCard moin={moin} key={moin.id} onChanged={query.reload}/>)}</div>}
    {page?.nextCursor && <div className="load-more"><Button onClick={() => setCursor(page.nextCursor || null)} disabled={query.loading}>{query.loading ? '불러오는 중…' : '더 보기'}</Button></div>}
    <Modal open={compose} onOpenChange={(open) => !open && closeComposer()} title={quoteID ? 'Quote Moin' : '새 모인'} description="짧은 생각, 링크 또는 이미지를 나누세요.">{quoteQuery.loading ? <SkeletonFeed/> : quoteQuery.error ? <ErrorState message={quoteQuery.error} onRetry={quoteQuery.reload}/> : <MoinComposer quoteMoin={quoteMoin} onClearQuote={clearQuote} onCreated={() => { closeComposer(); refresh(); }}/>}</Modal>
  </div>;
}
