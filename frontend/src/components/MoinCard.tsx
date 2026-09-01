import {
  BarChart3,
  Bookmark,
  Heart,
  Lightbulb,
  MessageCircle,
  Pencil,
  Quote,
  Repeat2,
  Share2,
  Trash2,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { apiRequest, readableError } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import {
  toggleMoinBookmark,
  toggleMoinRemoin,
  toggleMoinSignal,
} from "../state/moinMutations";
import type { Moin, SignalType } from "../types";
import { formatRelativeTime, topicLabel } from "../utils/format";
import { useToast } from "./ToastProvider";
import { DraftNavigationGuard } from "./DraftNavigationGuard";
import { MoinComposer } from "./MoinComposer";
import { MoinContent } from "./MoinContent";
import { Avatar, Badge, Modal } from "./ui";

const signalLabels: Record<SignalType, string> = {
  like: "공감",
  useful: "유용함",
  insight: "새로운 관점",
  question: "논의 필요",
  verify: "근거 확인",
};

function MoinEditDialog({
  current,
  editTriggerRef,
  editorStateRef,
  onClose,
  onUpdated,
}: {
  current: Moin;
  editTriggerRef: { current: HTMLButtonElement | null };
  editorStateRef: { current: { dirty: boolean; busy: boolean } };
  onClose: (force?: boolean) => void;
  onUpdated: (next: Moin) => void;
}) {
  return (
    <>
      <DraftNavigationGuard
        stateRef={editorStateRef}
        busyMessage="업로드 또는 저장이 끝난 뒤 이동해 주세요."
        confirmMessage="수정 중인 내용과 첨부 변경을 버리고 이동할까요?"
        onProceed={() => onClose(true)}
      />
      <Modal
        open
        onOpenChange={(open) => !open && onClose()}
        title="모인 수정"
        description="내용을 고치고 기존 첨부를 정리하거나 새 이미지·영상을 추가하세요."
        restoreFocusRef={editTriggerRef}
      >
        <MoinComposer
          autoFocus
          editMoin={current}
          onStateChange={(state) => {
            editorStateRef.current = state;
          }}
          onUpdated={onUpdated}
        />
      </Modal>
    </>
  );
}

export function MoinCard({
  moin,
  onMoinChange,
  onMoinDelete,
  compact = false,
}: {
  moin: Moin;
  onMoinChange?: (next: Moin) => void;
  onMoinDelete?: (id: string) => void;
  compact?: boolean;
}) {
  const { user } = useAuth();
  const location = useLocation();
  const locationSignature = `${location.key}\u0000${location.pathname}\u0000${location.search}\u0000${location.hash}`;
  const { notify } = useToast();
  const [current, setCurrent] = useState(moin);
  const [pending, setPending] = useState(false);
  const [editing, setEditing] = useState(false);
  const [deleted, setDeleted] = useState(false);
  const currentRef = useRef(moin);
  const pendingRef = useRef(false);
  const editTriggerRef = useRef<HTMLButtonElement>(null);
  const editorStateRef = useRef({ dirty: false, busy: false });
  const editLocationRef = useRef("");
  useEffect(() => {
    currentRef.current = moin;
    setCurrent(moin);
  }, [moin]);
  useEffect(() => {
    if (editing && editLocationRef.current !== locationSignature) {
      editorStateRef.current = { dirty: false, busy: false };
      setEditing(false);
    }
  }, [editing, locationSignature]);
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
            : current.visibility === "moim"
              ? "모임 안에 리모인했습니다."
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
  const editable =
    !compact &&
    user?.id === current.author.id &&
    current.kind !== "remoin" &&
    (!current.status || current.status === "published");
  const changeEditing = (open: boolean, force = false) => {
    if (open) {
      editorStateRef.current = { dirty: false, busy: false };
      editLocationRef.current = locationSignature;
      setEditing(true);
      return;
    }
    if (!force && editorStateRef.current.busy) {
      notify("업로드 또는 저장이 끝난 뒤 수정 창을 닫아 주세요.", "error");
      return;
    }
    if (
      !force &&
      editorStateRef.current.dirty &&
      !window.confirm("수정 중인 내용과 첨부 변경을 버리고 닫을까요?")
    )
      return;
    editorStateRef.current = { dirty: false, busy: false };
    editLocationRef.current = "";
    setEditing(false);
  };
  const deleteMoin = async () => {
    if (
      pendingRef.current ||
      !window.confirm(
        "이 모인을 삭제할까요? 삭제한 내용은 피드에서 사라지며 되돌릴 수 없습니다.",
      )
    )
      return;
    pendingRef.current = true;
    setPending(true);
    try {
      await apiRequest(`/posts/${encodeURIComponent(current.id)}`, {
        method: "DELETE",
      });
      setDeleted(true);
      notify("모인을 삭제했습니다.", "success");
      onMoinDelete?.(current.id);
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  };
  if (deleted) return null;
  return (
    <>
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
            {current.visibility === "moim" && (
              <Badge tone="brand">모임</Badge>
            )}
            <span>
              @{current.author.username} ·{" "}
              {formatRelativeTime(current.createdAt)}
            </span>
          </Link>
          {editable && (
            <div className="moin-owner-actions">
              <button
                ref={editTriggerRef}
                type="button"
                className="ui-button ui-button-ghost ui-button-icon moin-edit-button"
                aria-label="모인 수정"
                title="모인 수정"
                onClick={() => changeEditing(true)}
                disabled={pending}
              >
                <Pencil />
              </button>
              <button
                type="button"
                className="ui-button ui-button-ghost ui-button-icon moin-delete-button"
                aria-label="모인 삭제"
                title="모인 삭제"
                onClick={() => void deleteMoin()}
                disabled={pending}
              >
                <Trash2 />
              </button>
            </div>
          )}
        </header>
        {current.content && (
          <MoinContent content={current.content} moinId={current.id}/>
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
              aria-label={`리모인 ${current.counts?.remoins || 0}개`}
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
              aria-label={`${signalLabels.like} ${likeCount}개`}
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
              aria-label={`${signalLabels.insight} ${current.counts?.signals?.insight || 0}개`}
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
              aria-label="포켓"
            >
              <Bookmark />
              <span className="action-label">포켓</span>
            </button>
            <Link
              to={`/flow?compose=1&quote=${encodeURIComponent(current.id)}`}
              state={{ moinaComposerEntry: true }}
              aria-label="이 모인 인용"
            >
              <Quote />
              <span className="action-label">인용</span>
            </Link>
            <button
              type="button"
              aria-label="모인 주소 복사"
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
      {editable &&
        editing &&
        editLocationRef.current === locationSignature && (
        <MoinEditDialog
          current={current}
          editTriggerRef={editTriggerRef}
          editorStateRef={editorStateRef}
          onClose={(force) => changeEditing(false, force)}
          onUpdated={(next) => {
            commit(next);
            changeEditing(false, true);
          }}
        />
      )}
    </>
  );
}
