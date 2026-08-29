import { ImagePlus, LoaderCircle, Quote, RefreshCw, X } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { apiRequest, readableError } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { useApiQuery } from "../hooks/useApiQuery";
import type { Moin } from "../types";
import {
  MEDIA_ACCEPT,
  mediaTypeFor,
  uploadStatusLabel,
  type ComposerMediaType,
  type ComposerUploadStatus,
} from "../utils/media";
import { useToast } from "./ToastProvider";
import { Avatar, Button, IconButton } from "./ui";

interface MediaUpload {
  localId: string;
  id?: string;
  url?: string;
  previewUrl: string;
  name: string;
  file: File;
  type: ComposerMediaType;
  alt: string;
  status: ComposerUploadStatus;
}

interface MediaConfig {
  maxUploadBytes?: number;
  maxPerPost?: number;
  acceptedTypes?: string[];
}

const localID = () =>
  globalThis.crypto?.randomUUID?.() ||
  `media-${Date.now()}-${Math.random().toString(36).slice(2)}`;

export function MoinComposer({
  replyToId,
  quoteMoin,
  onClearQuote,
  placeholder = "지금 어떤 생각을 나누고 싶나요?",
  onCreated,
}: {
  replyToId?: string;
  quoteMoin?: Moin;
  onClearQuote?: () => void;
  placeholder?: string;
  onCreated: () => void;
}) {
  const { user } = useAuth();
  const { notify } = useToast();
  const mediaConfig = useApiQuery<MediaConfig>("/media/config");
  const [content, setContent] = useState("");
  const [visibility, setVisibility] = useState("public");
  const [media, setMedia] = useState<MediaUpload[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const mediaRef = useRef<MediaUpload[]>([]);
  const controllers = useRef(new Map<string, AbortController>());
  const uploadQueue = useRef<Promise<void>>(Promise.resolve());
  const scheduledUploads = useRef(new Set<string>());
  const cancelledUploads = useRef(new Set<string>());
  const disposed = useRef(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const remaining = 5000 - [...content].length;
  const uploading = media.some(
    (item) => item.status === "queued" || item.status === "uploading",
  );
  const completed = media.filter((item) => item.status === "uploaded").length;
  const maxPerPost = Math.min(
    12,
    Math.max(1, mediaConfig.data?.maxPerPost || 4),
  );
  const maxUploadBytes = Math.max(
    1,
    mediaConfig.data?.maxUploadBytes || 10 * 1024 * 1024,
  );

  useEffect(() => {
    mediaRef.current = media;
  }, [media]);
  useEffect(
    () => {
      disposed.current = false;
      return () => {
        disposed.current = true;
        mediaRef.current.forEach((item) =>
          cancelledUploads.current.add(item.localId),
        );
        controllers.current.forEach((controller) => controller.abort());
        mediaRef.current.forEach((item) => URL.revokeObjectURL(item.previewUrl));
      };
    },
    [],
  );

  const updateMedia = (localId: string, update: Partial<MediaUpload>) =>
    setMedia((current) =>
      current.map((item) =>
        item.localId === localId ? { ...item, ...update } : item,
      ),
    );

  const uploadOne = async (item: MediaUpload) => {
    if (disposed.current || cancelledUploads.current.has(item.localId)) return;
    const controller = new AbortController();
    controllers.current.set(item.localId, controller);
    updateMedia(item.localId, { status: "uploading" });
    try {
      const form = new FormData();
      form.append("file", item.file);
      const result = await apiRequest<{ id: string; url?: string }>("/media", {
        method: "POST",
        body: form,
        signal: controller.signal,
      });
      updateMedia(item.localId, { ...result, status: "uploaded" });
    } catch (error) {
      if (controller.signal.aborted)
        updateMedia(item.localId, { status: "cancelled" });
      else {
        updateMedia(item.localId, { status: "error" });
        notify(`${item.name}: ${readableError(error)}`, "error");
      }
    } finally {
      controllers.current.delete(item.localId);
    }
  };

  const enqueueUpload = (item: MediaUpload) => {
    if (scheduledUploads.current.has(item.localId)) return;
    scheduledUploads.current.add(item.localId);
    uploadQueue.current = uploadQueue.current
      .then(async () => {
        if (!disposed.current && !cancelledUploads.current.has(item.localId))
          await uploadOne(item);
      })
      .finally(() => scheduledUploads.current.delete(item.localId));
  };

  const fileIdentity = (file: File) =>
    `${file.name}\u0000${file.size}\u0000${file.type}\u0000${file.lastModified}`;

  const upload = (files: FileList | null) => {
    if (!files) return;
    const identities = new Set(media.map((item) => fileIdentity(item.file)));
    const duplicates: File[] = [];
    const candidates = [...files].filter((file) => {
      const identity = fileIdentity(file);
      if (identities.has(identity)) {
        duplicates.push(file);
        return false;
      }
      identities.add(identity);
      return true;
    });
    const available = Math.max(0, maxPerPost - media.length);
    const selected = candidates.slice(0, available);
    const invalid = selected.filter((file) => !mediaTypeFor(file));
    const oversized = selected.filter((file) => file.size > maxUploadBytes);
    const entries: MediaUpload[] = selected.flatMap((file) => {
      const type = mediaTypeFor(file);
      return type && file.size <= maxUploadBytes
        ? [
            {
              localId: localID(),
              previewUrl: URL.createObjectURL(file),
              name: file.name,
              file,
              type,
              alt: "",
              status: "queued" as const,
            },
          ]
        : [];
    });
    if (invalid.length)
      notify(
        "JPEG, PNG, GIF, WebP 이미지 또는 MP4, WebM 영상만 첨부할 수 있습니다.",
        "error",
      );
    if (oversized.length)
      notify(
        `파일당 최대 ${(maxUploadBytes / (1024 * 1024)).toLocaleString("ko-KR", { maximumFractionDigits: 1 })}MiB까지 첨부할 수 있습니다.`,
        "error",
      );
    if (duplicates.length)
      notify("이미 추가한 파일은 중복으로 첨부하지 않았습니다.", "error");
    if (candidates.length > available)
      notify(
        `모인 하나에 미디어를 최대 ${maxPerPost}개까지 첨부할 수 있습니다.`,
        "error",
      );
    setMedia((current) => [...current, ...entries]);
    entries.forEach(enqueueUpload);
    if (fileRef.current) fileRef.current.value = "";
  };

  const cancel = (item: MediaUpload) => {
    cancelledUploads.current.add(item.localId);
    controllers.current.get(item.localId)?.abort();
    if (item.status === "queued")
      updateMedia(item.localId, { status: "cancelled" });
  };

  const remove = (item: MediaUpload) => {
    cancelledUploads.current.add(item.localId);
    controllers.current.get(item.localId)?.abort();
    URL.revokeObjectURL(item.previewUrl);
    setMedia((current) =>
      current.filter((entry) => entry.localId !== item.localId),
    );
  };

  const retry = (item: MediaUpload) => {
    if (controllers.current.has(item.localId)) return;
    cancelledUploads.current.delete(item.localId);
    updateMedia(item.localId, { status: "queued" });
    enqueueUpload(item);
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!content.trim() || remaining < 0 || uploading) return;
    setSubmitting(true);
    try {
      const uploadedMedia = media.filter(
        (item): item is MediaUpload & { id: string } =>
          item.status === "uploaded" && Boolean(item.id),
      );
      const mediaIds = uploadedMedia.map((item) => item.id);
      const mediaAltTexts = Object.fromEntries(
        uploadedMedia.map((item) => [item.id, item.alt.trim()]),
      );
      await apiRequest("/posts", {
        method: "POST",
        body: {
          content: content.trim(),
          visibility,
          mediaIds,
          mediaAltTexts,
          ...(replyToId ? { replyToId } : {}),
          ...(quoteMoin ? { quoteMoinId: quoteMoin.id } : {}),
        },
      });
      setContent("");
      media.forEach((item) => URL.revokeObjectURL(item.previewUrl));
      setMedia([]);
      notify(
        replyToId ? "에코를 남겼습니다." : "모인을 플로우에 보냈습니다.",
        "success",
      );
      onCreated();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form className="moin-composer" onSubmit={submit}>
      <Avatar name={user?.displayName || "나"} src={user?.avatarUrl} />
      <div>
        <textarea
          rows={replyToId ? 3 : 4}
          value={content}
          onChange={(event) => setContent(event.target.value)}
          placeholder={placeholder}
          aria-label={replyToId ? "에코 내용" : "모인 내용"}
          maxLength={5100}
        />
        {quoteMoin && (
          <div className="composer-quote">
            <Quote />
            <span>
              <strong>
                {quoteMoin.author.displayName}{" "}
                <small>@{quoteMoin.author.username}</small>
              </strong>
              <p>{quoteMoin.content || "리모인한 원문"}</p>
            </span>
            {onClearQuote && (
              <IconButton
                type="button"
                label="인용 취소"
                onClick={onClearQuote}
              >
                <X />
              </IconButton>
            )}
          </div>
        )}
        {media.length > 0 && (
          <section className="composer-media" aria-label="첨부 미디어">
            <div className="upload-summary" role="status" aria-live="polite">
              <span>
                {completed}/{media.length}개 업로드 완료
              </span>
              {uploading && <progress aria-label="미디어 업로드 진행 중" />}
            </div>
            <div className="composer-media-grid">
              {media.map((item) => (
                <article
                  className={`composer-media-item status-${item.status}`}
                  key={item.localId}
                >
                  <div className="composer-media-preview">
                    {item.type === "image" ? (
                      <img src={item.previewUrl} alt={item.alt} />
                    ) : (
                      <video
                        src={item.previewUrl}
                        controls
                        preload="metadata"
                        aria-label={item.alt || `${item.name} 영상 미리보기`}
                      />
                    )}
                    <span>{uploadStatusLabel(item.status)}</span>
                  </div>
                  <div className="composer-media-fields">
                    <strong title={item.name}>{item.name}</strong>
                    <label>
                      <span>대체 텍스트</span>
                      <input
                        value={item.alt}
                        maxLength={500}
                        onChange={(event) =>
                          updateMedia(item.localId, { alt: event.target.value })
                        }
                        placeholder={
                          item.type === "video"
                            ? "영상의 핵심 장면과 내용을 설명해 주세요."
                            : "이미지에 보이는 내용을 설명해 주세요."
                        }
                      />
                    </label>
                    <small>
                      게시 후에도 보조 기술에서 미디어 설명으로 제공됩니다.
                    </small>
                    <div>
                      {(item.status === "queued" ||
                        item.status === "uploading") && (
                        <Button
                          type="button"
                          size="small"
                          onClick={() => cancel(item)}
                        >
                          업로드 취소
                        </Button>
                      )}
                      {(item.status === "error" ||
                        item.status === "cancelled") && (
                        <Button
                          type="button"
                          size="small"
                          onClick={() => retry(item)}
                        >
                          <RefreshCw />
                          다시 업로드
                        </Button>
                      )}
                      <IconButton
                        type="button"
                        label={`${item.name} 제거`}
                        onClick={() => remove(item)}
                      >
                        <X />
                      </IconButton>
                    </div>
                  </div>
                </article>
              ))}
            </div>
          </section>
        )}
        <footer>
          <div className="composer-tools">
            <input
              ref={fileRef}
              className="sr-only"
              id={`media-${replyToId || "new"}`}
              type="file"
              accept={MEDIA_ACCEPT}
              multiple
              onChange={(event) => upload(event.target.files)}
            />
            <IconButton
              type="button"
              label={`이미지 또는 영상 첨부 (최대 ${maxPerPost}개)`}
              onClick={() => fileRef.current?.click()}
              disabled={submitting || media.length >= maxPerPost}
            >
              <ImagePlus />
            </IconButton>
            <select
              aria-label="공개 범위"
              value={visibility}
              onChange={(event) => setVisibility(event.target.value)}
            >
              <option value="public">전체 공개</option>
              <option value="followers">연결한 사람</option>
            </select>
          </div>
          <span className={remaining < 0 ? "counter error" : "counter"}>
            {remaining.toLocaleString("ko-KR")}
          </span>
          <Button
            type="submit"
            variant="primary"
            disabled={
              submitting || uploading || !content.trim() || remaining < 0
            }
          >
            {submitting ? (
              <>
                <LoaderCircle className="spin" />
                처리 중
              </>
            ) : uploading ? (
              "업로드 대기"
            ) : replyToId ? (
              "에코"
            ) : (
              "모인하기"
            )}
          </Button>
        </footer>
      </div>
    </form>
  );
}
