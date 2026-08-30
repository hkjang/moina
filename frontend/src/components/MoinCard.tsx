import {
  BarChart3,
  Bookmark,
  Heart,
  Lightbulb,
  MessageCircle,
  Quote,
  Repeat2,
  Share2,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { apiRequest, readableError } from "../api/client";
import {
  toggleMoinBookmark,
  toggleMoinRemoin,
  toggleMoinSignal,
} from "../state/moinMutations";
import type { Moin, SignalType } from "../types";
import { formatRelativeTime, topicLabel } from "../utils/format";
import { useToast } from "./ToastProvider";
import { Avatar, Badge } from "./ui";

const signalLabels: Record<SignalType, string> = {
  like: "공감",
  useful: "유용함",
  insight: "새로운 관점",
  question: "논의 필요",
  verify: "근거 확인",
};

export function MoinCard({
  moin,
  onMoinChange,
  compact = false,
}: {
  moin: Moin;
  onMoinChange?: (next: Moin) => void;
  compact?: boolean;
}) {
  const { notify } = useToast();
  const [current, setCurrent] = useState(moin);
  const [pending, setPending] = useState(false);
  const currentRef = useRef(moin);
  const pendingRef = useRef(false);
  useEffect(() => {
    currentRef.current = moin;
    setCurrent(moin);
  }, [moin]);
  const commit = (next: Moin) => {
    currentRef.current = next;
    setCurrent(next);
    onMoinChange?.(next);
  };
  const mutate = async (
    next: Moin,
    request: () => Promise<unknown>,
    success?: string | ((result: unknown) => string | undefined),
    reconcile?: (result: unknown, optimistic: Moin, previous: Moin) => Moin,
  ) => {
    if (pendingRef.current) return;
    const previous = currentRef.current;
    pendingRef.current = true;
    commit(next);
    setPending(true);
    try {
      const result = await request();
      if (reconcile) commit(reconcile(result, next, previous));
      const message = typeof success === "function" ? success(result) : success;
      if (message) notify(message, "success");
    } catch (error) {
      commit(previous);
      notify(readableError(error), "error");
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  };
  const activeSignals = current.viewer?.signals || [];
  const react = (type: SignalType) => {
    const active = activeSignals.includes(type);
    return mutate(toggleMoinSignal(currentRef.current, type), () =>
      apiRequest(`/posts/${encodeURIComponent(current.id)}/reactions`, {
        method: active ? "DELETE" : "POST",
        body: { type },
      }),
    );
  };
  const bookmark = () => {
    const active = current.viewer?.bookmarked === true;
    return mutate(
      toggleMoinBookmark(currentRef.current),
      () =>
        apiRequest(`/posts/${encodeURIComponent(current.id)}/bookmark`, {
          method: active ? "DELETE" : "POST",
        }),
      active ? "포켓에서 꺼냈습니다." : "포켓에 저장했습니다.",
    );
  };
  const remoin = () => {
    const active = current.viewer?.remoined === true;
    const pendingApproval = (result: unknown) =>
      !active &&
      result !== null &&
      typeof result === "object" &&
      (result as { status?: string }).status === "pending_approval";
    return mutate(
      toggleMoinRemoin(currentRef.current),
      () =>
        apiRequest(`/posts/${encodeURIComponent(current.id)}/remoin`, {
          method: active ? "DELETE" : "POST",
        }),
      (result) =>
        active
          ? "리모인을 취소했습니다."
          : pendingApproval(result)
            ? "리모인이 승인 대기 상태로 접수되었습니다."
            : "내 플로우에 리모인했습니다.",
      (result, optimistic, previous) =>
        pendingApproval(result)
          ? {
              ...optimistic,
              counts: {
                ...optimistic.counts,
                remoins: previous.counts?.remoins || 0,
              },
            }
          : optimistic,
    );
  };
  const likeCount = current.counts?.signals?.like || 0;
  return (
    <article
      className={`moin-card${compact ? " compact" : ""}`}
      aria-busy={pending || undefined}
    >
      <Link
        className="moin-avatar"
        to={`/profile/${encodeURIComponent(current.author.username)}`}
        aria-label={`${current.author.displayName} 프로필`}
      >
        <Avatar
          name={current.author.displayName}
          src={current.author.avatarUrl}
        />
      </Link>
      <div className="moin-body">
        {current.kind === "remoin" && (
          <p className="remoin-label">
            <Repeat2 />
            Remoin
          </p>
        )}
        <header className="moin-header">
          <Link to={`/profile/${encodeURIComponent(current.author.username)}`}>
            <strong>{current.author.displayName}</strong>
            {current.author.accountType === "agent" && (
              <Badge tone="brand">AI</Badge>
            )}
            <span>
              @{current.author.username} ·{" "}
              {formatRelativeTime(current.createdAt)}
            </span>
          </Link>
        </header>
        {current.content && (
          <Link
            to={`/moin/${encodeURIComponent(current.id)}`}
            className="moin-content"
          >
            {current.content}
          </Link>
        )}
        {current.media && current.media.length > 0 && (
          <div
            className={`moin-media media-${Math.min(current.media.length, 4)}`}
          >
            {current.media.map((media) =>
              media.type === "image" ? (
                <img
                  key={media.id}
                  src={media.url}
                  alt={media.alt || "모인 첨부 이미지"}
                />
              ) : (
                <video
                  key={media.id}
                  src={media.url}
                  controls
                  aria-label={media.alt || "모인 첨부 영상"}
                />
              ),
            )}
          </div>
        )}
        {current.quoteMoin && (
          <div className="quote-moin">
            <Quote />
            <MoinCard moin={current.quoteMoin} compact />
          </div>
        )}
        {current.topics && current.topics.length > 0 && (
          <div className="topic-row">
            {current.topics.map((topic) => (
              <Link
                key={topic.id}
                to={`/topics/${encodeURIComponent(topic.slug)}`}
              >
                {topicLabel(topic.name)}
              </Link>
            ))}
          </div>
        )}
        {current.recommendation && current.recommendation.length > 0 && (
          <details className="why-moin">
            <summary>
              <BarChart3 />이 모인이 보이는 이유
            </summary>
            <div>
              {current.recommendation.map((reason) => (
                <p key={reason.label}>
                  <span>{reason.label}</span>
                  <strong>+{reason.score}</strong>
                </p>
              ))}
            </div>
          </details>
        )}
        {!compact && (
          <footer className="moin-actions">
            <Link
              to={`/moin/${encodeURIComponent(current.id)}`}
              aria-label={`에코 ${current.counts?.echoes || 0}개`}
            >
              <MessageCircle />
              <span>{current.counts?.echoes || 0}</span>
            </Link>
            <button
              type="button"
              disabled={pending}
              className={current.viewer?.remoined ? "active success" : ""}
              onClick={() => void remoin()}
              aria-pressed={current.viewer?.remoined}
            >
              <Repeat2 />
              <span>{current.counts?.remoins || 0}</span>
              <span className="action-label">리모인</span>
            </button>
            <button
              type="button"
              disabled={pending}
              className={activeSignals.includes("like") ? "active danger" : ""}
              onClick={() => void react("like")}
              aria-pressed={activeSignals.includes("like")}
              title={signalLabels.like}
            >
              <Heart />
              <span>{likeCount}</span>
              <span className="action-label">공감</span>
            </button>
            <button
              type="button"
              disabled={pending}
              className={
                activeSignals.includes("insight") ? "active brand" : ""
              }
              onClick={() => void react("insight")}
              aria-pressed={activeSignals.includes("insight")}
              title={signalLabels.insight}
            >
              <Lightbulb />
              <span>{current.counts?.signals?.insight || 0}</span>
              <span className="action-label">인사이트</span>
            </button>
            <button
              type="button"
              disabled={pending}
              className={current.viewer?.bookmarked ? "active brand" : ""}
              onClick={() => void bookmark()}
              aria-pressed={current.viewer?.bookmarked}
            >
              <Bookmark />
              <span className="action-label">포켓</span>
            </button>
            <Link
              to={`/flow?compose=1&quote=${encodeURIComponent(current.id)}`}
              aria-label="이 모인 인용"
            >
              <Quote />
              <span className="action-label">인용</span>
            </Link>
            <button
              type="button"
              onClick={() =>
                void navigator.clipboard
                  ?.writeText(`${window.location.origin}/moin/${current.id}`)
                  .then(() => notify("모인 주소를 복사했습니다.", "success"))
                  .catch(() => notify("주소를 복사하지 못했습니다.", "error"))
              }
            >
              <Share2 />
              <span className="action-label">공유</span>
            </button>
          </footer>
        )}
      </div>
    </article>
  );
}
