import { ArrowLeft, GitBranch } from 'lucide-react';
import { useNavigate, useParams } from 'react-router-dom';
import { MoinCard } from '../components/MoinCard';
import { MoinComposer } from '../components/MoinComposer';
import { Button, EmptyState, ErrorState, LoadingState } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import type { CursorPage, Moin } from '../types';
import { normalizeMoin, normalizePage } from '../api/adapters';

export default function MoinDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const post = useApiQuery<unknown>(`/posts/${encodeURIComponent(id)}`);
  const replies = useApiQuery<unknown>(`/posts/${encodeURIComponent(id)}/replies?limit=100`);
  const moin = post.data ? normalizeMoin(post.data) : undefined;
  const echoPage: CursorPage<Moin> | undefined = replies.data === undefined ? undefined : normalizePage(replies.data, normalizeMoin);
  return <div className="detail-page"><header className="detail-topbar"><Button size="icon" variant="ghost" aria-label="뒤로 가기" onClick={() => navigate(-1)}><ArrowLeft/></Button><div><h1>Chain</h1><p>연결된 생각과 에코</p></div></header>
    {post.loading ? <LoadingState/> : post.error ? <ErrorState message={post.error} onRetry={post.reload}/> : moin ? <MoinCard moin={moin}/> : <EmptyState title="모인을 찾을 수 없습니다" description="삭제되었거나 볼 수 없는 모인입니다."/>}
    {moin && <MoinComposer replyToId={moin.id} moimId={moin.moimId} placeholder={moin.moimId ? "모임 멤버에게 에코를 남겨보세요." : "이 생각에 에코를 남겨보세요."} onCreated={replies.reload}/>}
    <section className="chain-section"><header><GitBranch/><h2>에코 {echoPage?.items.length || 0}개</h2></header>{replies.loading ? <LoadingState/> : replies.error ? <ErrorState message={replies.error} onRetry={replies.reload}/> : echoPage?.items.length ? echoPage.items.map((reply) => <MoinCard key={reply.id} moin={reply}/>) : <EmptyState title="아직 에코가 없습니다" description="첫 번째 관점을 보태 대화를 시작해 보세요."/>}</section>
  </div>;
}
