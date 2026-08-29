import { Bookmark } from 'lucide-react';
import { normalizeMoin, normalizePage } from '../api/adapters';
import { MoinCard } from '../components/MoinCard';
import { EmptyState, ErrorState, LoadingState, PageHeader } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';

export default function PocketPage() {
  const query = useApiQuery<unknown>('/posts?bookmarked=true&limit=100');
  const items = query.data === undefined ? [] : normalizePage(query.data, normalizeMoin).items;
  return <div className="page-stack"><PageHeader title="Pocket" description="다시 보고 싶은 모인을 나만의 지식 서랍에 모아두세요."/>{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : items.length ? <div className="feed-list">{items.map((moin) => <MoinCard moin={moin} key={moin.id} onChanged={query.reload}/>)}</div> : <EmptyState title="Pocket이 비어 있습니다" description="모인의 북마크 버튼을 눌러 나중에 볼 생각을 저장하세요." action={<span className="state-icon"><Bookmark/></span>}/>}</div>;
}
