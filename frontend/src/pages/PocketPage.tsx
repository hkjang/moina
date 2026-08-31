import { Bookmark } from "lucide-react";
import { useEffect, useState } from "react";
import { normalizeMoin, normalizePage } from "../api/adapters";
import { MoinCard } from "../components/MoinCard";
import {
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
} from "../components/ui";
import { useApiQuery } from "../hooks/useApiQuery";

export function mergePocketMoin<T extends { id: string; viewer?: { bookmarked?: boolean } }>(current: T[], next: T) {
  if (next.viewer?.bookmarked === false) return current.filter((item) => item.id !== next.id);
  return current.some((item) => item.id === next.id) ? current.map((item) => item.id === next.id ? next : item) : [next, ...current];
}

export default function PocketPage() {
  const query = useApiQuery<unknown>("/posts?bookmarked=true&limit=100");
  const [items, setItems] = useState(() =>
    query.data === undefined
      ? []
      : normalizePage(query.data, normalizeMoin).items,
  );
  useEffect(() => {
    if (query.data !== undefined)
      setItems(normalizePage(query.data, normalizeMoin).items);
  }, [query.data]);
  const updateMoin = (next: (typeof items)[number]) =>
	setItems((current) => mergePocketMoin(current, next));
  const removeMoin = (id: string) =>
    setItems((current) => current.filter((item) => item.id !== id));
  return (
    <div className="page-stack">
      <PageHeader
        title="Pocket"
        description="다시 보고 싶은 모인을 나만의 지식 서랍에 모아두세요."
      />
      {query.loading ? (
        <LoadingState />
      ) : query.error ? (
        <ErrorState message={query.error} onRetry={query.reload} />
      ) : items.length ? (
        <div className="feed-list">
          {items.map((moin) => (
            <MoinCard
              moin={moin}
              key={moin.id}
              onMoinChange={updateMoin}
              onMoinDelete={removeMoin}
            />
          ))}
        </div>
      ) : (
        <EmptyState
          title="Pocket이 비어 있습니다"
          description="모인의 북마크 버튼을 눌러 나중에 볼 생각을 저장하세요."
          action={
            <span className="state-icon">
              <Bookmark />
            </span>
          }
        />
      )}
    </div>
  );
}
