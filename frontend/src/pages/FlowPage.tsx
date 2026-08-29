import { PenLine, RefreshCw } from 'lucide-react';
import { useSearchParams } from 'react-router-dom';
import { normalizeMoin } from '../api/adapters';
import { useAuth } from '../auth/AuthContext';
import { MoinCard } from '../components/MoinCard';
import { MoinComposer } from '../components/MoinComposer';
import { Avatar, Button, EmptyState, ErrorState, Modal, SkeletonFeed, Tabs } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import { useFeedPages, type FeedMode } from '../hooks/useFeedPages';

function FlowContent({ mode }: { mode: FeedMode }) {
  const { user } = useAuth();
  const [params, setParams] = useSearchParams();
  const compose = params.get('compose') === '1';
  const quoteID = params.get('quote') || '';
  const feed = useFeedPages(mode);
  const quoteQuery = useApiQuery<unknown>(compose && quoteID ? `/posts/${encodeURIComponent(quoteID)}` : null);
  const quoteMoin = quoteQuery.data ? normalizeMoin(quoteQuery.data) : undefined;
  const setMode = (next: string) => setParams(next === 'following' ? { mode: 'following' } : {}, { replace: true });
  const openComposer = () => setParams({ ...Object.fromEntries(params), compose: '1' });
  const clearQuote = () => { const next = new URLSearchParams(params); next.delete('quote'); setParams(next, { replace: true }); };
  const closeComposer = () => { const next = new URLSearchParams(params); next.delete('compose'); next.delete('quote'); setParams(next, { replace: true }); };

  return <div className="feed-page">
    <header className="sticky-feed-header"><div><h1>플로우</h1><Button size="icon" variant="ghost" aria-label="피드 새로고침" onClick={() => void feed.reloadFirstPage()} disabled={feed.loading}><RefreshCw/></Button></div><Tabs value={mode} onChange={setMode} label="피드 선택" items={[{ value: 'for_me', label: 'For Me' }, { value: 'following', label: 'Following' }]}/></header>
    <button className="composer-prompt" type="button" onClick={openComposer}><Avatar name={user?.displayName || '나'} src={user?.avatarUrl}/><span>지금 어떤 생각을 나누고 싶나요?</span><PenLine/></button>
    {feed.loading && feed.items.length === 0 ? <SkeletonFeed/> : feed.error && feed.items.length === 0 ? <ErrorState message={feed.error} onRetry={feed.reload}/> : feed.items.length === 0 ? <EmptyState title={mode === 'following' ? '플로우가 아직 조용합니다' : '추천할 모인을 찾고 있습니다'} description={mode === 'following' ? '관심 있는 사람이나 토픽을 Link하면 새 모인이 여기에 흐릅니다.' : '첫 모인을 남기거나 관심 토픽을 선택해 For Me를 시작하세요.'} action={<Button variant="primary" onClick={openComposer}>첫 모인 작성</Button>}/> : <div className="feed-list">{feed.items.map((moin) => <MoinCard moin={moin} key={moin.id} onMoinChange={feed.updateMoin}/>)}</div>}
    {feed.error && feed.items.length > 0 && <div className="feed-inline-error" role="alert"><span>{feed.error}</span><Button size="small" onClick={() => void feed.reload()}>다시 시도</Button></div>}
    {feed.nextCursor && <div className="load-more"><Button onClick={feed.loadMore} disabled={feed.loading}>{feed.loading ? '불러오는 중…' : '더 보기'}</Button></div>}
    <Modal open={compose} onOpenChange={(open) => !open && closeComposer()} title={quoteID ? 'Quote Moin' : '새 모인'} description="짧은 생각, 링크, 이미지 또는 영상을 나누세요.">{quoteQuery.loading ? <SkeletonFeed/> : quoteQuery.error ? <ErrorState message={quoteQuery.error} onRetry={quoteQuery.reload}/> : <MoinComposer quoteMoin={quoteMoin} onClearQuote={clearQuote} onCreated={() => { closeComposer(); void feed.reloadFirstPage(); }}/>}</Modal>
  </div>;
}

export default function FlowPage() {
  const [params] = useSearchParams();
  const mode: FeedMode = params.get('mode') === 'following' ? 'following' : 'for_me';
  return <FlowContent key={mode} mode={mode}/>;
}
