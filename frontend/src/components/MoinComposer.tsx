import {
  ArrowLeft,
  ArrowRight,
  ClipboardPaste,
  ImagePlus,
  LoaderCircle,
  Quote,
  RefreshCw,
  Upload,
  X,
} from "lucide-react";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ClipboardEvent as ReactClipboardEvent,
  type DragEvent,
  type FormEvent,
  type KeyboardEvent,
} from "react";
import { normalizeMoin, normalizeProfile } from "../api/adapters";
import { apiRequest, readableError } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { useApiQuery } from "../hooks/useApiQuery";
import type { Moin, Profile } from "../types";
import {
  clipboardImages,
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
  size?: number;
  file?: File;
  identity?: string;
  ownsPreview: boolean;
  type: ComposerMediaType;
  alt: string;
  status: ComposerUploadStatus;
}

interface MediaConfig {
  maxUploadBytes?: number;
  maxPerPost?: number;
  acceptedTypes?: string[];
}

interface MentionSearch {
  start: number;
  end: number;
  query: string;
}

const activeMention = (value: string, caret: number): MentionSearch | null => {
  const before = value.slice(0, caret);
  const match = before.match(
    /(?:^|[^\p{L}\p{N}._-])@([\p{L}\p{N}._-]{0,39})$/u,
  );
  if (!match) return null;
  const query = match[1];
  return { start: caret - query.length - 1, end: caret, query };
};

const localID = () =>
  globalThis.crypto?.randomUUID?.() ||
  `media-${Date.now()}-${Math.random().toString(36).slice(2)}`;

const fileIdentity = (file: File) =>
  `${file.name}\u0000${file.size}\u0000${file.type}\u0000${file.lastModified}`;

const clipboardName = (file: File, index: number) => {
  const extension = file.type.split("/")[1]?.replace("jpeg", "jpg") || "png";
  return `클립보드 이미지 ${index + 1}.${extension}`;
};

const fileSize = (bytes?: number) => {
  if (!bytes || bytes < 1) return "크기 정보 없음";
  if (bytes < 1024) return `${bytes.toLocaleString("ko-KR")}B`;
  if (bytes < 1024 * 1024)
    return `${(bytes / 1024).toLocaleString("ko-KR", { maximumFractionDigits: 1 })}KiB`;
  return `${(bytes / (1024 * 1024)).toLocaleString("ko-KR", { maximumFractionDigits: 1 })}MiB`;
};

const initialMedia = (moin?: Moin): MediaUpload[] =>
  (moin?.media || []).map((item, index) => ({
    localId: `existing-${item.id}`,
    id: item.id,
    url: item.url,
    previewUrl: item.url,
    ownsPreview: false,
    name:
      item.filename ||
      `기존 ${item.type === "video" ? "영상" : "이미지"} ${index + 1}`,
    size: item.size,
    type: item.type,
    alt: item.alt || "",
    status: "uploaded",
  }));

export function MoinComposer({
  replyToId,
  quoteMoin,
  onClearQuote,
  placeholder = "지금 어떤 생각을 나누고 싶나요?",
  onCreated,
  editMoin,
  onUpdated,
  autoFocus = false,
  onStateChange,
}: {
  replyToId?: string;
  quoteMoin?: Moin;
  onClearQuote?: () => void;
  placeholder?: string;
  onCreated?: () => void;
  editMoin?: Moin;
  onUpdated?: (next: Moin) => void;
  autoFocus?: boolean;
  onStateChange?: (state: { dirty: boolean; busy: boolean }) => void;
}) {
  const { user } = useAuth();
  const { notify } = useToast();
  const mediaHintDetailsID = useId();
  const mediaHintLimitID = useId();
  const mediaConfig = useApiQuery<MediaConfig>("/media/config", {
    ttlMs: 0,
    staleWhileRevalidateMs: 0,
  });
  const [content, setContent] = useState(editMoin?.content || "");
  const [visibility, setVisibility] = useState<string>(
    editMoin?.visibility || "public",
  );
  const [media, setMedia] = useState<MediaUpload[]>(() =>
    initialMedia(editMoin),
  );
  const [submitting, setSubmitting] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [mentionSearch, setMentionSearch] = useState<MentionSearch | null>(null);
  const [mentionCandidates, setMentionCandidates] = useState<Profile[]>([]);
  const [mentionLoading, setMentionLoading] = useState(false);
  const [mentionIndex, setMentionIndex] = useState(0);
  const mediaRef = useRef<MediaUpload[]>(media);
  const controllers = useRef(new Map<string, AbortController>());
  const uploadQueue = useRef<Promise<void>>(Promise.resolve());
  const scheduledUploads = useRef(new Set<string>());
  const cancelledUploads = useRef(new Set<string>());
  const disposed = useRef(false);
  const submittingRef = useRef(false);
  const protectedMediaIDs = useRef(new Set<string>());
  const dragDepth = useRef(0);
  const formRef = useRef<HTMLFormElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const mentionTimer = useRef<number | undefined>(undefined);
  const mentionSequence = useRef(0);
  const clipboardSequence = useRef(0);
  const addFilesRef = useRef<
    (
      files: Iterable<File> | ArrayLike<File> | null,
      source?: "picker" | "paste" | "drop",
    ) => number
  >(() => 0);
  const remaining = 5000 - [...content].length;
  const uploading = media.some(
    (item) => item.status === "queued" || item.status === "uploading",
  );
  const unresolved = media.some(
    (item) => item.status === "error" || item.status === "cancelled",
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
  const mediaIntakeReady =
    Boolean(mediaConfig.data) &&
    !mediaConfig.loading &&
    !mediaConfig.backgroundLoading;
  const initialMediaIDs = useRef(
    new Set((editMoin?.media || []).map((item) => item.id)),
  );
  const overLimit =
    media.length > maxPerPost &&
    (!editMoin ||
      media.some(
        (item) => !item.id || !initialMediaIDs.current.has(item.id),
      ));
  const initialDraft = useRef({
    content: editMoin?.content || "",
    visibility: editMoin?.visibility || "public",
    media: JSON.stringify(
      (editMoin?.media || []).map((item) => [item.id, item.alt || ""]),
    ),
  });
  const mediaDraft = JSON.stringify(
    media.map((item) => [item.id || item.localId, item.alt]),
  );
  const dirty =
    content !== initialDraft.current.content ||
    visibility !== initialDraft.current.visibility ||
    mediaDraft !== initialDraft.current.media;

  useEffect(() => {
    mediaRef.current = media;
  }, [media]);
  useEffect(() => {
    onStateChange?.({ dirty, busy: submitting || uploading });
  }, [dirty, onStateChange, submitting, uploading]);
  useEffect(() => {
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirty && !submitting && !uploading) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warnBeforeUnload);
    return () => window.removeEventListener("beforeunload", warnBeforeUnload);
  }, [dirty, submitting, uploading]);
  useEffect(
    () => {
      disposed.current = false;
      return () => {
        disposed.current = true;
        window.clearTimeout(mentionTimer.current);
        mediaRef.current.forEach((item) =>
          cancelledUploads.current.add(item.localId),
        );
        controllers.current.forEach((controller) => controller.abort());
        mediaRef.current.forEach((item) => {
          if (item.ownsPreview) URL.revokeObjectURL(item.previewUrl);
          if (
            item.ownsPreview &&
            item.id &&
            !protectedMediaIDs.current.has(item.id)
          )
            void apiRequest(`/media/${encodeURIComponent(item.id)}`, {
              method: "DELETE",
            }).catch(() => undefined);
        });
      };
    },
    [],
  );

  const updateMedia = (localId: string, update: Partial<MediaUpload>) => {
    const next = mediaRef.current.map((item) =>
      item.localId === localId ? { ...item, ...update } : item,
    );
    mediaRef.current = next;
    setMedia(next);
  };

  const uploadOne = async (item: MediaUpload) => {
    if (
      !item.file ||
      disposed.current ||
      cancelledUploads.current.has(item.localId)
    )
      return;
    const controller = new AbortController();
    controllers.current.set(item.localId, controller);
    updateMedia(item.localId, { status: "uploading" });
    let removeAbortListener: () => void = () => undefined;
    let cleanupScheduled = false;
    try {
      const form = new FormData();
      form.append("file", item.file);
      const uploadRequest = apiRequest<{ id: string; url?: string }>("/media", {
        method: "POST",
        body: form,
        signal: controller.signal,
      });
      void uploadRequest
        .then((lateResult) => {
          if (
            !cleanupScheduled &&
            (disposed.current ||
              controller.signal.aborted ||
              cancelledUploads.current.has(item.localId))
          ) {
            cleanupScheduled = true;
            void apiRequest(`/media/${encodeURIComponent(lateResult.id)}`, {
              method: "DELETE",
            }).catch(() => undefined);
          }
        })
        .catch(() => undefined);
      const aborted = new Promise<never>((_resolve, reject) => {
        const onAbort = () =>
          reject(new DOMException("업로드 취소됨", "AbortError"));
        removeAbortListener = () =>
          controller.signal.removeEventListener("abort", onAbort);
        if (controller.signal.aborted) onAbort();
        else controller.signal.addEventListener("abort", onAbort, { once: true });
      });
      const result = await Promise.race([uploadRequest, aborted]);
      const wasCancelled = cancelledUploads.current.has(item.localId);
      if (disposed.current || wasCancelled) {
        if (!disposed.current && wasCancelled)
          updateMedia(item.localId, { status: "cancelled" });
        if (!cleanupScheduled) {
          cleanupScheduled = true;
          void apiRequest(`/media/${encodeURIComponent(result.id)}`, {
            method: "DELETE",
          }).catch(() => undefined);
        }
        return;
      }
      updateMedia(item.localId, { ...result, status: "uploaded" });
    } catch (error) {
      if (disposed.current) return;
      if (controller.signal.aborted)
        updateMedia(item.localId, { status: "cancelled" });
      else {
        updateMedia(item.localId, { status: "error" });
        notify(`${item.name}: ${readableError(error)}`, "error");
      }
    } finally {
      removeAbortListener();
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
      .finally(() => {
        scheduledUploads.current.delete(item.localId);
        const latest = mediaRef.current.find(
          (entry) => entry.localId === item.localId,
        );
        if (
          latest?.status === "queued" &&
          !disposed.current &&
          !cancelledUploads.current.has(item.localId)
        )
          enqueueUpload(latest);
      });
  };

  const addFiles = (
    files: Iterable<File> | ArrayLike<File> | null,
    source: "picker" | "paste" | "drop" = "picker",
  ) => {
    if (submittingRef.current) return 0;
    if (!files) return 0;
    const incoming = Array.from(files);
    if (incoming.length === 0) return 0;
    if (!mediaIntakeReady) {
      if (source === "picker" && fileRef.current) fileRef.current.value = "";
      notify(
        mediaConfig.error
          ? "미디어 첨부 설정을 불러오지 못했습니다. 다시 시도해 주세요."
          : "미디어 첨부 설정을 확인 중입니다. 잠시 후 다시 시도해 주세요.",
        mediaConfig.error ? "error" : "info",
      );
      return 0;
    }
    const currentMedia = mediaRef.current;
    const identities = new Set(
      currentMedia.flatMap((item) => (item.identity ? [item.identity] : [])),
    );
    const duplicates: File[] = [];
    const candidates = incoming.filter((file) => {
      const identity = fileIdentity(file);
      if (identities.has(identity)) {
        duplicates.push(file);
        return false;
      }
      identities.add(identity);
      return true;
    });
    const available = Math.max(0, maxPerPost - currentMedia.length);
    const invalid = candidates.filter(
      (file) => !mediaTypeFor(file) || file.size === 0,
    );
    const oversized = candidates.filter(
      (file) => Boolean(mediaTypeFor(file)) && file.size > maxUploadBytes,
    );
    const valid = candidates.filter(
      (file) =>
        Boolean(mediaTypeFor(file)) &&
        file.size > 0 &&
        file.size <= maxUploadBytes,
    );
    const selected = valid.slice(0, available);
    const entries: MediaUpload[] = selected.flatMap((file) => {
      const type = mediaTypeFor(file);
      return type
        ? [
            {
              localId: localID(),
              previewUrl: URL.createObjectURL(file),
              ownsPreview: true,
              name:
                source === "paste"
                  ? clipboardName(file, clipboardSequence.current++)
                  : file.name,
              file,
              size: file.size,
              identity: fileIdentity(file),
              type,
              alt: "",
              status: "queued" as const,
            },
          ]
        : [];
    });
    if (invalid.length)
      notify(
        "비어 있지 않은 JPEG, PNG, GIF, WebP 이미지 또는 MP4, WebM 영상만 첨부할 수 있습니다.",
        "error",
      );
    if (oversized.length)
      notify(
        `파일당 최대 ${(maxUploadBytes / (1024 * 1024)).toLocaleString("ko-KR", { maximumFractionDigits: 1 })}MiB까지 첨부할 수 있습니다.`,
        "error",
      );
    if (duplicates.length)
      notify("이미 추가한 파일은 중복으로 첨부하지 않았습니다.", "error");
    if (valid.length > available)
      notify(
        `모인 하나에 미디어를 최대 ${maxPerPost}개까지 첨부할 수 있습니다.`,
        "error",
      );
    const next = [...currentMedia, ...entries];
    mediaRef.current = next;
    setMedia(next);
    entries.forEach(enqueueUpload);
    if (source === "picker" && fileRef.current) fileRef.current.value = "";
    if (entries.length > 0 && source !== "picker")
      notify(
        source === "paste"
          ? `클립보드 이미지 ${entries.length}개를 첨부했습니다.`
          : `끌어 놓은 미디어 ${entries.length}개를 첨부했습니다.`,
        "success",
      );
    return entries.length;
  };
  addFilesRef.current = addFiles;

  const paste = (event: ReactClipboardEvent<HTMLFormElement>) => {
    const images = clipboardImages(event.clipboardData);
    if (images.length === 0) return;
    event.preventDefault();
    addFiles(images, "paste");
  };

  useEffect(() => {
    const form = formRef.current;
    const dialog = form?.closest<HTMLElement>('[role="dialog"]');
    if (!form || !dialog) return;
    const pasteOutsideForm = (event: globalThis.ClipboardEvent) => {
      if (event.defaultPrevented) return;
      if (event.target && form.contains(event.target as Node)) return;
      const images = clipboardImages(event.clipboardData);
      if (images.length === 0) return;
      event.preventDefault();
      addFilesRef.current(images, "paste");
    };
    dialog.addEventListener("paste", pasteOutsideForm);
    return () => dialog.removeEventListener("paste", pasteOutsideForm);
  }, []);

  const hasDraggedFiles = (event: DragEvent<HTMLFormElement>) =>
    Array.from(event.dataTransfer.types || []).includes("Files");

  const dragEnter = (event: DragEvent<HTMLFormElement>) => {
    if (!hasDraggedFiles(event)) return;
    event.preventDefault();
    if (submittingRef.current || !mediaIntakeReady) return;
    dragDepth.current += 1;
    setDragging(true);
  };

  const dragOver = (event: DragEvent<HTMLFormElement>) => {
    if (!hasDraggedFiles(event)) return;
    event.preventDefault();
    if (submittingRef.current || !mediaIntakeReady) return;
    event.dataTransfer.dropEffect = "copy";
  };

  const dragLeave = (event: DragEvent<HTMLFormElement>) => {
    if (!hasDraggedFiles(event)) return;
    event.preventDefault();
    if (submittingRef.current) return;
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };

  const drop = (event: DragEvent<HTMLFormElement>) => {
    if (!hasDraggedFiles(event)) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    addFiles(event.dataTransfer.files, "drop");
  };

  const cancel = (item: MediaUpload) => {
    if (submittingRef.current) return;
    const latest =
      mediaRef.current.find((entry) => entry.localId === item.localId) || item;
    cancelledUploads.current.add(item.localId);
    controllers.current.get(item.localId)?.abort();
    if (latest.status === "queued" || latest.status === "uploading")
      updateMedia(item.localId, { status: "cancelled" });
    else if (latest.status === "uploaded") {
      if (latest.id)
        void apiRequest(`/media/${encodeURIComponent(latest.id)}`, {
          method: "DELETE",
        }).catch(() => undefined);
      updateMedia(item.localId, {
        id: undefined,
        url: undefined,
        status: "cancelled",
      });
    }
  };

  const remove = (item: MediaUpload) => {
    if (submittingRef.current) return;
    const latest =
      mediaRef.current.find((entry) => entry.localId === item.localId) || item;
    cancelledUploads.current.add(item.localId);
    controllers.current.get(item.localId)?.abort();
    if (latest.ownsPreview) URL.revokeObjectURL(latest.previewUrl);
    if (latest.ownsPreview && latest.id)
      void apiRequest(`/media/${encodeURIComponent(latest.id)}`, {
        method: "DELETE",
      }).catch(() =>
        notify(
          "화면에서는 첨부를 제거했지만 서버 정리는 나중에 다시 시도됩니다.",
          "error",
        ),
      );
    const next = mediaRef.current.filter(
      (entry) => entry.localId !== item.localId,
    );
    mediaRef.current = next;
    setMedia(next);
  };

  const retry = (item: MediaUpload) => {
    if (submittingRef.current) return;
    if (controllers.current.has(item.localId)) return;
    cancelledUploads.current.delete(item.localId);
    updateMedia(item.localId, { status: "queued" });
    enqueueUpload(item);
  };

  const move = (item: MediaUpload, offset: -1 | 1) => {
    if (submittingRef.current) return;
    const current = mediaRef.current;
    const from = current.findIndex((entry) => entry.localId === item.localId);
    const to = from + offset;
    if (from < 0 || to < 0 || to >= current.length) return;
    const next = [...current];
    [next[from], next[to]] = [next[to], next[from]];
    mediaRef.current = next;
    setMedia(next);
  };

  const closeMentions = () => {
    window.clearTimeout(mentionTimer.current);
    mentionSequence.current += 1;
    setMentionSearch(null);
    setMentionCandidates([]);
    setMentionLoading(false);
    setMentionIndex(0);
  };

  const findMentions = (value: string, caret: number) => {
    const active = activeMention(value, caret);
    window.clearTimeout(mentionTimer.current);
    if (!active) {
      closeMentions();
      return;
    }
    const sequence = ++mentionSequence.current;
    setMentionSearch(active);
    setMentionCandidates([]);
    setMentionLoading(true);
    setMentionIndex(0);
    mentionTimer.current = window.setTimeout(() => {
      const path = active.query
        ? `/search?q=${encodeURIComponent(active.query)}&type=users&limit=6`
        : "/search?recommended=true&type=users&limit=6";
      void apiRequest<unknown>(path)
        .then((result) => {
          if (disposed.current || sequence !== mentionSequence.current) return;
          const users =
            result && typeof result === "object" && Array.isArray((result as { users?: unknown[] }).users)
              ? (result as { users: unknown[] }).users
              : [];
          setMentionCandidates(users.map(normalizeProfile));
        })
        .catch(() => {
          if (!disposed.current && sequence === mentionSequence.current)
            setMentionCandidates([]);
        })
        .finally(() => {
          if (!disposed.current && sequence === mentionSequence.current)
            setMentionLoading(false);
        });
    }, 180);
  };

  const insertMention = (candidate: Profile) => {
    if (!mentionSearch) return;
    const next = `${content.slice(0, mentionSearch.start)}@${candidate.username} ${content.slice(mentionSearch.end)}`;
    const caret = mentionSearch.start + candidate.username.length + 2;
    setContent(next);
    closeMentions();
    window.requestAnimationFrame(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(caret, caret);
    });
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (
      submittingRef.current ||
      !content.trim() ||
      remaining < 0 ||
      overLimit ||
      mediaRef.current.some((item) => item.status !== "uploaded")
    )
      return;
    submittingRef.current = true;
    setSubmitting(true);
    try {
      const uploadedMedia = mediaRef.current.filter(
        (item): item is MediaUpload & { id: string } =>
          item.status === "uploaded" && Boolean(item.id),
      );
      const mediaIds = uploadedMedia.map((item) => item.id);
      const mediaAltTexts = Object.fromEntries(
        uploadedMedia.map((item) => [item.id, item.alt.trim()]),
      );
      protectedMediaIDs.current = new Set(mediaIds);
      if (editMoin) {
        const result = await apiRequest<unknown>(
          `/posts/${encodeURIComponent(editMoin.id)}`,
          {
            method: "PATCH",
            body: {
              content: content.trim(),
              mediaIds,
              mediaAltTexts,
            },
          },
        );
        const updated = normalizeMoin(result);
        const retained = new Set(mediaIds);
        (editMoin.media || []).forEach((item) => {
          if (!retained.has(item.id))
            void apiRequest(`/media/${encodeURIComponent(item.id)}`, {
              method: "DELETE",
            }).catch(() => undefined);
        });
        notify("모인과 첨부 미디어를 수정했습니다.", "success");
        onUpdated?.(updated);
        return;
      }
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
      mediaRef.current.forEach((item) => {
        if (item.ownsPreview) URL.revokeObjectURL(item.previewUrl);
      });
      mediaRef.current = [];
      setMedia([]);
      notify(
        replyToId ? "에코를 남겼습니다." : "모인을 플로우에 보냈습니다.",
        "success",
      );
      onCreated?.();
    } catch (error) {
      protectedMediaIDs.current.clear();
      if (disposed.current)
        mediaRef.current.forEach((item) => {
          if (item.ownsPreview && item.id)
            void apiRequest(`/media/${encodeURIComponent(item.id)}`, {
              method: "DELETE",
            }).catch(() => undefined);
        });
      if (!disposed.current) notify(readableError(error), "error");
    } finally {
      submittingRef.current = false;
      if (!disposed.current) setSubmitting(false);
    }
  };

  const shortcutSubmit = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (mentionSearch && (mentionLoading || mentionCandidates.length > 0)) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        if (mentionCandidates.length > 0)
          setMentionIndex((current) =>
            event.key === "ArrowDown"
              ? (current + 1) % mentionCandidates.length
              : (current - 1 + mentionCandidates.length) % mentionCandidates.length,
          );
        return;
      }
      if ((event.key === "Enter" || event.key === "Tab") && mentionCandidates[mentionIndex]) {
        event.preventDefault();
        insertMention(mentionCandidates[mentionIndex]);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        closeMentions();
        return;
      }
    }
    if (
      event.key === "Enter" &&
      (event.ctrlKey || event.metaKey) &&
      !event.nativeEvent.isComposing
    ) {
      event.preventDefault();
      event.currentTarget.form?.requestSubmit();
    }
  };

  return (
    <form
      ref={formRef}
      className={`moin-composer${dragging ? " is-dragging" : ""}`}
      onSubmit={submit}
      onPaste={paste}
      onDragEnter={dragEnter}
      onDragOver={dragOver}
      onDragLeave={dragLeave}
      onDrop={drop}
      aria-label={editMoin ? "모인 수정" : "새 모인 작성"}
    >
      {dragging && (
        <div className="composer-drop-overlay" role="status">
          <Upload />
          <strong>여기에 놓아 미디어 첨부</strong>
        </div>
      )}
      <Avatar name={user?.displayName || "나"} src={user?.avatarUrl} />
      <div>
        <textarea
          ref={textareaRef}
          rows={replyToId ? 3 : 4}
          autoFocus={autoFocus}
          disabled={submitting}
          value={content}
          onChange={(event) => {
            setContent(event.target.value);
            findMentions(event.target.value, event.target.selectionStart);
          }}
          onKeyDown={shortcutSubmit}
          onClick={(event) => findMentions(event.currentTarget.value, event.currentTarget.selectionStart)}
          onBlur={() => window.setTimeout(() => closeMentions(), 100)}
          aria-autocomplete="list"
          aria-expanded={mentionSearch !== null}
          aria-controls={mentionSearch ? "moin-mention-list" : undefined}
          aria-activedescendant={mentionCandidates[mentionIndex] ? `moin-mention-${mentionCandidates[mentionIndex].id}` : undefined}
          placeholder={placeholder}
          aria-label={editMoin ? "수정할 모인 내용" : replyToId ? "에코 내용" : "모인 내용"}
          maxLength={5100}
        />
        {mentionSearch && (
          <div className="composer-mentions" id="moin-mention-list" role="listbox" aria-label="멘션할 사용자">
            {mentionLoading ? (
              <span className="composer-mention-state"><LoaderCircle className="spin"/>사용자를 찾는 중…</span>
            ) : mentionCandidates.length > 0 ? mentionCandidates.map((candidate, index) => (
              <button
                id={`moin-mention-${candidate.id}`}
                key={candidate.id}
                type="button"
                role="option"
                aria-selected={index === mentionIndex}
                className={index === mentionIndex ? "active" : ""}
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setMentionIndex(index)}
                onClick={() => insertMention(candidate)}
              >
                <Avatar name={candidate.displayName} src={candidate.avatarUrl}/>
                <span><strong>{candidate.displayName}</strong><small>@{candidate.username}</small></span>
              </button>
            )) : (
              <span className="composer-mention-state">일치하는 사용자가 없습니다.</span>
            )}
          </div>
        )}
        <button
          type="button"
          className="composer-intake-hint"
          aria-label="미디어 추가 영역"
          aria-describedby={`${mediaHintDetailsID} ${mediaHintLimitID}`}
          onClick={() => fileRef.current?.click()}
          disabled={
            submitting || !mediaIntakeReady || media.length >= maxPerPost
          }
        >
          <ClipboardPaste aria-hidden="true" />
          <span id={mediaHintDetailsID}>
            <strong>
              {mediaIntakeReady
                ? "캡처 이미지는 Ctrl/⌘+V"
                : mediaConfig.error
                  ? "미디어 첨부 설정 확인 실패"
                  : "미디어 첨부 설정 확인 중"}
            </strong>
            <small>
              JPEG·PNG·GIF·WebP / MP4·WebM을 끌어 놓거나 첨부 버튼으로
              선택할 수도 있습니다.
            </small>
          </span>
          <small id={mediaHintLimitID}>
            {mediaIntakeReady
              ? `${media.length}/${maxPerPost}개 · 파일당 최대 ${(maxUploadBytes / (1024 * 1024)).toLocaleString("ko-KR", { maximumFractionDigits: 1 })}MiB`
              : "설정 확인 후 첨부 가능"}
          </small>
        </button>
        {!mediaConfig.data && mediaConfig.error && (
          <div className="composer-config-error" role="alert">
            <span>{mediaConfig.error}</span>
            <Button
              type="button"
              size="small"
              onClick={() => void mediaConfig.reload()}
              disabled={mediaConfig.loading}
            >
              다시 시도
            </Button>
          </div>
        )}
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
                disabled={submitting}
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
              {uploading && (
                <progress
                  aria-label="미디어 업로드 진행 중"
                  value={completed}
                  max={media.length}
                />
              )}
            </div>
            {unresolved && (
              <p className="composer-media-warning" role="alert">
                실패하거나 취소한 첨부를 제거하거나 다시 업로드해야 게시할 수
                있습니다.
              </p>
            )}
            {overLimit && (
              <p className="composer-media-warning" role="alert">
                현재 설정에서는 첨부를 최대 {maxPerPost}개까지 게시할 수
                있습니다. 초과한 첨부를 제거해 주세요.
              </p>
            )}
            <div className="composer-media-grid">
              {media.map((item, index) => (
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
                    <small className="composer-media-meta">
                      {item.type === "video" ? "영상" : "이미지"} ·{" "}
                      {fileSize(item.size)}
                    </small>
                    <label>
                      <span>대체 텍스트</span>
                      <input
                        aria-label={`${item.name} 대체 텍스트`}
                        value={item.alt}
                        disabled={submitting}
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
                      <IconButton
                        type="button"
                        label={`${item.name} 앞으로 이동`}
                        onClick={() => move(item, -1)}
                        disabled={submitting || index === 0}
                      >
                        <ArrowLeft />
                      </IconButton>
                      <IconButton
                        type="button"
                        label={`${item.name} 뒤로 이동`}
                        onClick={() => move(item, 1)}
                        disabled={submitting || index === media.length - 1}
                      >
                        <ArrowRight />
                      </IconButton>
                      {(item.status === "queued" ||
                        item.status === "uploading") && (
                        <Button
                          type="button"
                          size="small"
                          aria-label={`${item.name} 업로드 취소`}
                          onClick={() => cancel(item)}
                          disabled={submitting}
                        >
                          업로드 취소
                        </Button>
                      )}
                      {(item.status === "error" ||
                        item.status === "cancelled") && (
                        <Button
                          type="button"
                          size="small"
                          aria-label={`${item.name} 다시 업로드`}
                          onClick={() => retry(item)}
                          disabled={submitting}
                        >
                          <RefreshCw />
                          다시 업로드
                        </Button>
                      )}
                      <IconButton
                        type="button"
                        label={`${item.name} 제거`}
                        onClick={() => remove(item)}
                        disabled={submitting}
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
              id={`media-${replyToId || editMoin?.id || "new"}`}
              type="file"
              aria-label="첨부할 이미지 또는 영상 파일"
              tabIndex={-1}
              disabled={
                submitting || !mediaIntakeReady || media.length >= maxPerPost
              }
              accept={MEDIA_ACCEPT}
              multiple
              onChange={(event) => addFiles(event.target.files)}
            />
            <IconButton
              type="button"
              label={`이미지 또는 영상 첨부 (최대 ${maxPerPost}개)`}
              onClick={() => fileRef.current?.click()}
              disabled={
                submitting || !mediaIntakeReady || media.length >= maxPerPost
              }
            >
              <ImagePlus />
            </IconButton>
            {!editMoin && (
              <select
                aria-label="공개 범위"
                value={visibility}
                disabled={submitting}
                onChange={(event) => setVisibility(event.target.value)}
              >
                <option value="public">전체 공개</option>
                <option value="followers">연결한 사람</option>
              </select>
            )}
          </div>
          <span className={remaining < 0 ? "counter error" : "counter"}>
            {remaining.toLocaleString("ko-KR")}
            <small>Ctrl/⌘+Enter</small>
          </span>
          <Button
            type="submit"
            variant="primary"
            disabled={
              submitting ||
              uploading ||
              unresolved ||
              overLimit ||
              !content.trim() ||
              remaining < 0
            }
          >
            {submitting ? (
              <>
                <LoaderCircle className="spin" />
                처리 중
              </>
            ) : uploading ? (
              "업로드 대기"
            ) : unresolved || overLimit ? (
              "첨부 확인"
            ) : editMoin ? (
              "변경사항 저장"
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
