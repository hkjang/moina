import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiRequest } from "../api/client";
import { clearApiQueryCache } from "../hooks/apiQueryClient";
import type { Moin } from "../types";
import { MoinComposer } from "./MoinComposer";
import { ToastProvider } from "./ToastProvider";
import { Modal } from "./ui";

vi.mock("../api/client", async (load) => {
  const actual = await load<typeof import("../api/client")>();
  return { ...actual, apiRequest: vi.fn() };
});

vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({
    user: { id: "u1", username: "tester", displayName: "테스터" },
  }),
}));

const mockedRequest = vi.mocked(apiRequest);

function renderComposer({
  editMoin,
  replyToId,
  moimId,
  moimName,
  quoteMoin,
  onCreated = vi.fn(),
  onUpdated = vi.fn(),
}: {
  editMoin?: Moin;
  replyToId?: string;
  moimId?: string;
  moimName?: string;
  quoteMoin?: Moin;
  onCreated?: () => void;
  onUpdated?: (next: Moin) => void;
} = {}) {
  const rendered = render(
    <ToastProvider>
      <MoinComposer
        editMoin={editMoin}
        replyToId={replyToId}
        moimId={moimId}
        moimName={moimName}
        quoteMoin={quoteMoin}
        onCreated={onCreated}
        onUpdated={onUpdated}
      />
    </ToastProvider>,
  );
  const input =
    rendered.container.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("미디어 입력을 찾지 못했습니다.");
  return { ...rendered, input };
}

async function waitForMediaIntake() {
  const addButton = await screen.findByRole("button", {
    name: /^이미지 또는 영상 첨부/,
  });
  await waitFor(() => expect(addButton).toBeEnabled());
}

function asFileList(files: File[]) {
  const list = [...files] as unknown as FileList;
  Object.defineProperty(list, "item", {
    configurable: true,
    value: (index: number) => files[index] || null,
  });
  return list;
}

function clipboardData(files: File[] = [], text = "") {
  const items = [
    ...files.map((file) => ({
      kind: "file" as const,
      type: file.type,
      getAsFile: () => file,
    })),
    ...(text
      ? [
          {
            kind: "string" as const,
            type: "text/plain",
            getAsFile: () => null,
          },
        ]
      : []),
  ];
  return {
    files: asFileList(files),
    items,
    types: [
      ...(files.length ? ["Files"] : []),
      ...(text ? ["text/plain"] : []),
    ],
    getData: (type: string) => (type === "text/plain" ? text : ""),
  } as unknown as DataTransfer;
}

function dispatchPaste(target: Element, files: File[] = [], text = "") {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    configurable: true,
    value: clipboardData(files, text),
  });
  fireEvent(target, event);
  return event;
}

function dispatchDrop(target: Element, files: File[]) {
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    configurable: true,
    value: clipboardData(files),
  });
  fireEvent(target, event);
  return event;
}

function editMoin(media: Moin["media"] = []): Moin {
  return {
    id: "moin-edit",
    content: "기존 모인 내용",
    author: {
      id: "u1",
      username: "tester",
      displayName: "테스터",
    },
    createdAt: "2026-08-31T00:00:00Z",
    visibility: "public",
    media,
  };
}

describe("MoinComposer media upload", () => {
  beforeEach(() => {
    mockedRequest.mockReset();
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({
            maxUploadBytes: 10 * 1024 * 1024,
            maxPerPost: 4,
            acceptedTypes: [],
          })
        : Promise.resolve({}),
    );
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: vi.fn(() => "blob:moina-preview"),
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: vi.fn(),
    });
  });
  afterEach(() => {
    cleanup();
    clearApiQueryCache({ abort: true });
  });

  it("서버 미디어 설정을 확인하기 전에는 첨부를 시작하지 않는다", async () => {
    let resolveConfig:
      | ((value: { maxUploadBytes: number; maxPerPost: number }) => void)
      | undefined;
    const config = new Promise<{
      maxUploadBytes: number;
      maxPerPost: number;
    }>((resolve) => {
      resolveConfig = resolve;
    });
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config") return config;
      if (path === "/media") return Promise.resolve({ id: "media-ready" });
      return Promise.resolve({});
    });
    const { input } = renderComposer();
    const textarea = screen.getByLabelText("모인 내용");
    const image = new File(["clipboard"], "캡처.png", {
      type: "image/png",
    });

    expect(input).toBeDisabled();
    const earlyPaste = dispatchPaste(textarea, [image]);
    expect(earlyPaste.defaultPrevented).toBe(true);
    expect(
      await screen.findByText(
        "미디어 첨부 설정을 확인 중입니다. 잠시 후 다시 시도해 주세요.",
      ),
    ).toBeInTheDocument();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(0);

    resolveConfig?.({ maxUploadBytes: 1024, maxPerPost: 4 });
    await waitForMediaIntake();
    dispatchPaste(textarea, [image]);
    await screen.findByText("1/1개 업로드 완료");
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(1);
  });

  it("MP4/WebM을 선택하고 실제 영상 preview와 업로드 취소를 제공한다", async () => {
    let uploadSignal: AbortSignal | undefined;
    mockedRequest.mockImplementation((path, options) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 10 * 1024 * 1024, maxPerPost: 4 })
        : new Promise((_resolve, reject) => {
            uploadSignal = options?.signal ?? undefined;
            uploadSignal?.addEventListener("abort", () =>
              reject(new DOMException("취소됨", "AbortError")),
            );
          }),
    );
    const { container, input } = renderComposer();
    await waitForMediaIntake();
    expect(input.accept).toContain("video/mp4");
    expect(input.accept).toContain("video/webm");

    const video = new File(["video"], "소개.mp4", { type: "video/mp4" });
    fireEvent.change(input, { target: { files: [video] } });

    expect(await screen.findByText("소개.mp4")).toBeInTheDocument();
    expect(container.querySelector("video")).toHaveAttribute(
      "src",
      "blob:moina-preview",
    );
    expect(
      screen.getByPlaceholderText("영상의 핵심 장면과 내용을 설명해 주세요."),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /업로드 취소$/ }));
    expect(uploadSignal?.aborted).toBe(true);
    expect(await screen.findByText("업로드 취소됨")).toBeInTheDocument();
  });

  it("업로드된 ID별 대체 텍스트를 모인 생성 요청에 포함한다", async () => {
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({
          maxUploadBytes: 10 * 1024 * 1024,
          maxPerPost: 4,
        });
      if (path === "/media")
        return Promise.resolve({ id: "media-1", url: "/api/v1/media/media-1" });
      return Promise.resolve({ id: "post-1" });
    });
    const { input } = renderComposer();
    await waitForMediaIntake();
    const image = new File(["image"], "구조도.png", { type: "image/png" });
    fireEvent.change(input, { target: { files: [image] } });
    await screen.findByText("1/1개 업로드 완료");

    fireEvent.change(screen.getByLabelText("모인 내용"), {
      target: { value: "새로운 구조를 공유합니다." },
    });
    fireEvent.change(
      screen.getByPlaceholderText("이미지에 보이는 내용을 설명해 주세요."),
      { target: { value: "MOINA 서비스 구성도" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "모인하기" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith("/posts", expect.anything()),
    );
    expect(mockedRequest).toHaveBeenCalledWith(
      "/posts",
      expect.objectContaining({
        method: "POST",
        body: expect.objectContaining({
          mediaIds: ["media-1"],
          mediaAltTexts: { "media-1": "MOINA 서비스 구성도" },
        }),
      }),
    );
  });

  it("모임 작성기는 공개 범위를 고정하고 moimId를 생성 요청에 포함한다", async () => {
    const onCreated = vi.fn();
    renderComposer({
      moimId: "moim-engineering",
      moimName: "엔지니어링",
      onCreated,
    });

    expect(
      screen.getByLabelText("공개 범위: 엔지니어링 모임 멤버"),
    ).toHaveTextContent("모임 공개");
    expect(screen.queryByRole("combobox", { name: "공개 범위" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("모인 내용"), {
      target: { value: "모임의 첫 대화를 시작합니다." },
    });
    fireEvent.click(screen.getByRole("button", { name: "모인하기" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts",
        expect.objectContaining({
          method: "POST",
          body: expect.objectContaining({
            content: "모임의 첫 대화를 시작합니다.",
            visibility: "moim",
            moimId: "moim-engineering",
          }),
        }),
      ),
    );
    expect(onCreated).toHaveBeenCalledTimes(1);
  });

  it("모임 Echo도 부모 moimId와 고정 공개 범위를 전송한다", async () => {
    renderComposer({ replyToId: "moin-parent", moimId: "moim-private" });
    fireEvent.change(screen.getByLabelText("에코 내용"), {
      target: { value: "모임 안에서만 보일 답글입니다." },
    });
    fireEvent.click(screen.getByRole("button", { name: "에코" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts",
        expect.objectContaining({
          method: "POST",
          body: expect.objectContaining({
            visibility: "moim",
            moimId: "moim-private",
            replyToId: "moin-parent",
          }),
        }),
      ),
    );
  });

  it("모임 Moin 인용도 원문의 모임 공개 범위를 사용한다", async () => {
    renderComposer({
      quoteMoin: {
        id: "moin-source",
        content: "모임 원문",
        visibility: "moim",
        moimId: "moim-source",
        createdAt: "2026-09-01T00:00:00Z",
        author: { id: "u2", username: "source", displayName: "원문 작성자" },
      },
    });
    expect(screen.getByText("모임 공개")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("모인 내용"), {
      target: { value: "모임 안에서만 인용합니다." },
    });
    fireEvent.click(screen.getByRole("button", { name: "모인하기" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts",
        expect.objectContaining({
          body: expect.objectContaining({
            visibility: "moim",
            moimId: "moim-source",
            quoteMoinId: "moin-source",
          }),
        }),
      ),
    );
  });

  it("모임 인용을 해제하면 기본 공개 범위로 안전하게 돌아간다", async () => {
    const source: Moin = {
      id: "moin-source-clear",
      content: "해제할 모임 원문",
      visibility: "moim",
      moimId: "moim-clear",
      createdAt: "2026-09-01T00:00:00Z",
      author: { id: "u2", username: "source", displayName: "원문 작성자" },
    };
    const rendered = render(
      <ToastProvider>
        <MoinComposer quoteMoin={source} onClearQuote={() => undefined} />
      </ToastProvider>,
    );
    fireEvent.change(screen.getByLabelText("모인 내용"), {
      target: { value: "인용을 해제하고 남길 일반 Moin" },
    });

    rendered.rerender(
      <ToastProvider>
        <MoinComposer onClearQuote={() => undefined} />
      </ToastProvider>,
    );

    expect(screen.queryByText("모임 공개")).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "공개 범위" })).toHaveValue("public");
    fireEvent.click(screen.getByRole("button", { name: "모인하기" }));
    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith("/posts", expect.anything()),
    );
    const postCall = [...mockedRequest.mock.calls]
      .reverse()
      .find(([path]) => path === "/posts");
    expect(postCall?.[1]?.body).toMatchObject({ visibility: "public" });
    expect(postCall?.[1]?.body).not.toHaveProperty("moimId");
    expect(postCall?.[1]?.body).not.toHaveProperty("quoteMoinId");
  });

  it("게시 요청 중 재제출과 편집을 잠가 중복 Moin을 만들지 않는다", async () => {
    let finish: (() => void) | undefined;
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/posts")
        return new Promise((resolve) => {
          finish = () => resolve({ id: "post-one" });
        });
      return Promise.resolve({});
    });
    const { input } = renderComposer();
    const textarea = screen.getByLabelText("모인 내용");
    fireEvent.change(textarea, { target: { value: "한 번만 게시" } });
    const form = screen.getByRole("form", { name: "새 모인 작성" });

    fireEvent.submit(form);
    fireEvent.submit(form);

    await waitFor(() => expect(textarea).toBeDisabled());
    expect(input).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("button", { name: "미디어 추가 영역" })).toBeDisabled();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/posts"),
    ).toHaveLength(1);
    finish?.();
  });

  it("다중 선택 파일을 동시에 전송하지 않고 FIFO로 직렬 업로드한다", async () => {
    let uploadCalls = 0;
    let activeUploads = 0;
    let maximumActiveUploads = 0;
    let resolveFirst: (() => void) | undefined;
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({
          maxUploadBytes: 10 * 1024 * 1024,
          maxPerPost: 4,
        });
      if (path !== "/media") return Promise.resolve({});
      uploadCalls += 1;
      activeUploads += 1;
      maximumActiveUploads = Math.max(maximumActiveUploads, activeUploads);
      const id = `media-${uploadCalls}`;
      if (uploadCalls === 1)
        return new Promise((resolve) => {
          resolveFirst = () => {
            activeUploads -= 1;
            resolve({ id });
          };
        });
      activeUploads -= 1;
      return Promise.resolve({ id });
    });
    const { input } = renderComposer();
    await waitForMediaIntake();
    fireEvent.change(input, {
      target: {
        files: [
          new File(["first"], "첫째.png", { type: "image/png" }),
          new File(["second"], "둘째.png", { type: "image/png" }),
        ],
      },
    });

    await waitFor(() => expect(uploadCalls).toBe(1));
    expect(maximumActiveUploads).toBe(1);
    resolveFirst?.();
    await waitFor(() => expect(uploadCalls).toBe(2));
    await screen.findByText("2/2개 업로드 완료");
    expect(maximumActiveUploads).toBe(1);
  });

  it("취소와 실패가 발생해도 FIFO의 다음 파일을 계속 처리한다", async () => {
    let uploadCalls = 0;
    mockedRequest.mockImplementation((path, options) => {
      if (path === "/media/config")
        return Promise.resolve({
          maxUploadBytes: 10 * 1024 * 1024,
          maxPerPost: 4,
        });
      if (path !== "/media") return Promise.resolve({});
      uploadCalls += 1;
      if (uploadCalls === 1)
        return new Promise((_resolve, reject) =>
          options?.signal?.addEventListener("abort", () =>
            reject(new DOMException("취소됨", "AbortError")),
          ),
        );
      if (uploadCalls === 2) return Promise.reject(new Error("업로드 실패"));
      return Promise.resolve({ id: "media-3" });
    });
    const { input } = renderComposer();
    await waitForMediaIntake();
    fireEvent.change(input, {
      target: {
        files: [
          new File(["first"], "취소.png", { type: "image/png" }),
          new File(["second"], "실패.png", { type: "image/png" }),
          new File(["third"], "완료.png", { type: "image/png" }),
        ],
      },
    });

    await waitFor(() => expect(uploadCalls).toBe(1));
    fireEvent.click(screen.getAllByRole("button", { name: /업로드 취소$/ })[0]);
    await waitFor(() => expect(uploadCalls).toBe(3));
    await screen.findByText("1/3개 업로드 완료");
    expect(screen.getByText("업로드 취소됨")).toBeInTheDocument();
    expect(screen.getByText("업로드 실패")).toBeInTheDocument();
    expect(screen.getByText("업로드 완료")).toBeInTheDocument();
  });

  it("동일 파일을 한 번만 큐에 넣는다", async () => {
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 })
        : Promise.resolve({ id: "media-1" }),
    );
    const { input } = renderComposer();
    await waitForMediaIntake();
    const duplicate = new File(["same"], "같은.png", {
      type: "image/png",
      lastModified: 1,
    });
    fireEvent.change(input, { target: { files: [duplicate, duplicate] } });

    await screen.findByText("1/1개 업로드 완료");
    expect(screen.getAllByText("같은.png")).toHaveLength(1);
    expect(
      screen.getByText("이미 추가한 파일은 중복으로 첨부하지 않았습니다."),
    ).toBeInTheDocument();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(1);
  });

  it("업로드한 신규 첨부를 제거하면 미연결 media 정리를 요청한다", async () => {
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 })
        : path === "/media"
          ? Promise.resolve({ id: "media-remove" })
          : Promise.resolve({}),
    );
    const { input } = renderComposer();
    await waitForMediaIntake();
    fireEvent.change(input, {
      target: { files: [new File(["image"], "정리.png", { type: "image/png" })] },
    });
    await screen.findByText("1/1개 업로드 완료");

    fireEvent.click(screen.getByRole("button", { name: "정리.png 제거" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/media/media-remove",
        expect.objectContaining({ method: "DELETE" }),
      ),
    );
  });

  it("제거와 업로드 완료가 경합해도 뒤늦게 생성된 media를 정리한다", async () => {
    let finishUpload: ((value: { id: string }) => void) | undefined;
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media")
        return new Promise((resolve) => {
          finishUpload = resolve;
        });
      return Promise.resolve({});
    });
    const { input } = renderComposer();
    await waitForMediaIntake();
    fireEvent.change(input, {
      target: {
        files: [new File(["image"], "경합.png", { type: "image/png" })],
      },
    });
    await screen.findByText("업로드 중");

    fireEvent.click(screen.getByRole("button", { name: "경합.png 제거" }));
    finishUpload?.({ id: "media-raced" });

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/media/media-raced",
        expect.objectContaining({ method: "DELETE" }),
      ),
    );
  });

  it("취소 직후 업로드가 성공해도 정리하고 다시 업로드할 수 있게 전환한다", async () => {
    let finishUpload: ((value: { id: string }) => void) | undefined;
    let uploadCount = 0;
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media") {
        uploadCount += 1;
        if (uploadCount > 1) return Promise.resolve({ id: "media-next" });
        return new Promise((resolve) => {
          finishUpload = resolve;
        });
      }
      if (path === "/media/media-late-cancel") return new Promise(() => {});
      return Promise.resolve({});
    });
    const { input } = renderComposer();
    await waitForMediaIntake();
    fireEvent.change(input, {
      target: {
        files: [
          new File(["image"], "늦은취소.png", { type: "image/png" }),
          new File(["next"], "다음.png", { type: "image/png" }),
        ],
      },
    });
    await screen.findByText("업로드 중");

    fireEvent.click(
      screen.getByRole("button", { name: "늦은취소.png 업로드 취소" }),
    );
    expect(await screen.findByText("업로드 취소됨")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "늦은취소.png 다시 업로드" }),
    ).toBeInTheDocument();
    await screen.findByText("1/2개 업로드 완료");
    expect(uploadCount).toBe(2);
    finishUpload?.({ id: "media-late-cancel" });
    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/media/media-late-cancel",
        expect.objectContaining({ method: "DELETE" }),
      ),
    );
  });

  it("관리자가 설정한 모인당 첨부 개수를 적용한다", async () => {
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 1 })
        : Promise.resolve({ id: "media-1" }),
    );
    const { input } = renderComposer();
    await waitForMediaIntake();
    fireEvent.change(input, {
      target: {
        files: [
          new File(["a"], "a.png", { type: "image/png" }),
          new File(["b"], "b.png", { type: "image/png" }),
        ],
      },
    });
    await screen.findByText("a.png");
    expect(screen.queryByText("b.png")).not.toBeInTheDocument();
    expect(
      screen.getByText("모인 하나에 미디어를 최대 1개까지 첨부할 수 있습니다."),
    ).toBeInTheDocument();
  });

  it("Ctrl/Command+V로 붙여넣은 PNG를 한 번 업로드하고 미리보기와 완료 상태를 표시한다", async () => {
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({
          maxUploadBytes: 10 * 1024 * 1024,
          maxPerPost: 4,
        });
      if (path === "/media")
        return Promise.resolve({
          id: "media-paste",
          url: "/api/v1/media/media-paste",
        });
      return Promise.resolve({});
    });
    const { container } = renderComposer();
    await waitForMediaIntake();
    const textarea = screen.getByLabelText("모인 내용");
    const image = new File(["clipboard image"], "붙여넣은-이미지.png", {
      type: "image/png",
      lastModified: 1,
    });

    const event = dispatchPaste(textarea, [image]);

    expect(event.defaultPrevented).toBe(true);
    expect(await screen.findByText("1/1개 업로드 완료")).toBeInTheDocument();
    expect(screen.getByText("업로드 완료")).toBeInTheDocument();
    expect(container.querySelector(".composer-media-preview img")).toHaveAttribute(
      "src",
      "blob:moina-preview",
    );
    const uploads = mockedRequest.mock.calls.filter(([path]) => path === "/media");
    expect(uploads).toHaveLength(1);
    const body = uploads[0][1]?.body;
    expect(body).toBeInstanceOf(FormData);
    expect((body as FormData).get("file")).toBe(image);
  });

  it("텍스트와 비이미지 clipboard paste는 가로채거나 업로드하지 않는다", async () => {
    const { container } = renderComposer();
    await waitForMediaIntake();
    const textarea = screen.getByLabelText("모인 내용");

    const textEvent = dispatchPaste(textarea, [], "그대로 붙여넣을 텍스트");
    const documentFile = new File(["document"], "문서.pdf", {
      type: "application/pdf",
    });
    const fileEvent = dispatchPaste(textarea, [documentFile]);
    await Promise.resolve();
    await Promise.resolve();

    expect(textEvent.defaultPrevented).toBe(false);
    expect(fileEvent.defaultPrevented).toBe(false);
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(0);
    expect(container.querySelector(".composer-media-preview")).not.toBeInTheDocument();
  });

  it("clipboard paste에도 동일 파일 중복 제거와 모인당 첨부 한도를 적용한다", async () => {
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 1 })
        : Promise.resolve({ id: "media-1" }),
    );
    renderComposer();
    await waitForMediaIntake();
    const duplicate = new File(["same"], "같은.png", {
      type: "image/png",
      lastModified: 1,
    });
    const overflow = new File(["other"], "초과.png", {
      type: "image/png",
      lastModified: 2,
    });

    dispatchPaste(screen.getByLabelText("모인 내용"), [
      duplicate,
      duplicate,
      overflow,
    ]);

    expect(await screen.findByText("1/1개 업로드 완료")).toBeInTheDocument();
    expect(screen.getAllByText("클립보드 이미지 1.png")).toHaveLength(1);
    expect(screen.queryByText("초과.png")).not.toBeInTheDocument();
    expect(
      screen.getByText("이미 추가한 파일은 중복으로 첨부하지 않았습니다."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("모인 하나에 미디어를 최대 1개까지 첨부할 수 있습니다."),
    ).toBeInTheDocument();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(1);
  });

  it("dropzone에 놓은 이미지도 기존 미디어 큐로 업로드한다", async () => {
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 })
        : Promise.resolve({ id: "media-drop" }),
    );
    renderComposer();
    await waitForMediaIntake();
    const dropzone = await screen.findByRole("form", {
      name: "새 모인 작성",
    });
    const image = new File(["dropped"], "끌어놓은.png", {
      type: "image/png",
    });

    const event = dispatchDrop(dropzone, [image]);

    expect(event.defaultPrevented).toBe(true);
    expect(await screen.findByText("1/1개 업로드 완료")).toBeInTheDocument();
    expect(screen.getByText("끌어놓은.png")).toBeInTheDocument();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(1);
  });

  it("수정에서 기존 미디어 다음에 붙여넣은 미디어를 배치하고 전체 ID와 대체 텍스트를 PATCH한다", async () => {
    const original = editMoin([
      {
        id: "media-existing",
        type: "image",
        url: "/api/v1/media/media-existing",
        alt: "기존 설명",
      },
    ]);
    const updated: Moin = {
      ...original,
      content: "수정한 모인 내용",
      media: [
        original.media![0],
        {
          id: "media-pasted",
          type: "image",
          url: "/api/v1/media/media-pasted",
          alt: "새 이미지 설명",
        },
      ],
    };
    const onUpdated = vi.fn();
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media")
        return Promise.resolve({
          id: "media-pasted",
          url: "/api/v1/media/media-pasted",
        });
      if (path === "/posts/moin-edit") return Promise.resolve(updated);
      return Promise.resolve({});
    });
    renderComposer({ editMoin: original, onUpdated });
    await waitForMediaIntake();
    const textarea = screen.getByLabelText("수정할 모인 내용");
    expect(textarea).toHaveValue("기존 모인 내용");

    dispatchPaste(textarea, [
      new File(["new image"], "새-이미지.png", { type: "image/png" }),
    ]);
    await screen.findByText("2/2개 업로드 완료");
    fireEvent.change(textarea, { target: { value: "수정한 모인 내용" } });
    const altInputs = screen.getAllByPlaceholderText(
      "이미지에 보이는 내용을 설명해 주세요.",
    );
    fireEvent.change(altInputs[0], { target: { value: "수정한 기존 설명" } });
    fireEvent.change(altInputs[1], { target: { value: "새 이미지 설명" } });
    fireEvent.click(screen.getByRole("button", { name: "변경사항 저장" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts/moin-edit",
        expect.objectContaining({
          method: "PATCH",
          body: expect.objectContaining({
            content: "수정한 모인 내용",
            mediaIds: ["media-existing", "media-pasted"],
            mediaAltTexts: {
              "media-existing": "수정한 기존 설명",
              "media-pasted": "새 이미지 설명",
            },
          }),
        }),
      ),
    );
    expect(onUpdated).toHaveBeenCalledWith(
      expect.objectContaining({ id: "moin-edit", content: "수정한 모인 내용" }),
    );
  });

  it("수정에서 기존 미디어를 모두 제거하면 빈 mediaIds와 대체 텍스트를 PATCH한다", async () => {
    const original = editMoin([
      {
        id: "media-existing",
        type: "image",
        url: "/api/v1/media/media-existing",
        alt: "기존 설명",
      },
    ]);
    const onUpdated = vi.fn();
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/posts/moin-edit")
        return Promise.resolve({ ...original, media: [] });
      return Promise.resolve({});
    });
    renderComposer({ editMoin: original, onUpdated });

    fireEvent.click(screen.getByRole("button", { name: /제거$/ }));
    fireEvent.click(screen.getByRole("button", { name: "변경사항 저장" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts/moin-edit",
        expect.objectContaining({
          method: "PATCH",
          body: expect.objectContaining({
            mediaIds: [],
            mediaAltTexts: {},
          }),
        }),
      ),
    );
    expect(onUpdated).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/media/media-existing",
        expect.objectContaining({ method: "DELETE" }),
      ),
    );
  });

  it("수정에서 첨부 순서를 키보드 버튼으로 바꿔 PATCH 순서에 반영한다", async () => {
    const original = editMoin([
      { id: "media-first", type: "image", url: "/api/v1/media/media-first" },
      { id: "media-second", type: "image", url: "/api/v1/media/media-second" },
    ]);
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/posts/moin-edit") return Promise.resolve(original);
      return Promise.resolve({});
    });
    renderComposer({ editMoin: original });

    fireEvent.click(
      screen.getByRole("button", { name: "기존 이미지 2 앞으로 이동" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "변경사항 저장" }));

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts/moin-edit",
        expect.objectContaining({
          body: expect.objectContaining({
            mediaIds: ["media-second", "media-first"],
          }),
        }),
      ),
    );
  });

  it("관리자가 한도를 낮춰도 기존 첨부만 유지한 모인은 수정할 수 있다", async () => {
    const existing = Array.from({ length: 5 }, (_, index) => ({
      id: `media-${index + 1}`,
      type: "image" as const,
      url: `/api/v1/media/media-${index + 1}`,
    }));
    const original = editMoin(existing);
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/posts/moin-edit")
        return Promise.resolve({ ...original, content: "한도 변경 후 수정" });
      return Promise.resolve({});
    });
    renderComposer({ editMoin: original });
    await screen.findByRole("button", {
      name: "이미지 또는 영상 첨부 (최대 4개)",
    });
    fireEvent.change(screen.getByLabelText("수정할 모인 내용"), {
      target: { value: "한도 변경 후 수정" },
    });

    const save = screen.getByRole("button", { name: "변경사항 저장" });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() =>
      expect(mockedRequest).toHaveBeenCalledWith(
        "/posts/moin-edit",
        expect.objectContaining({
          body: expect.objectContaining({
            mediaIds: existing.map((item) => item.id),
          }),
        }),
      ),
    );
  });

  it("실패한 첨부가 남아 있으면 새 모인을 게시할 수 없다", async () => {
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media") return Promise.reject(new Error("업로드 실패"));
      return Promise.resolve({});
    });
    renderComposer();
    await waitForMediaIntake();
    const textarea = screen.getByLabelText("모인 내용");
    fireEvent.change(textarea, { target: { value: "실패 첨부가 있는 모인" } });
    dispatchPaste(textarea, [
      new File(["failed"], "실패.png", { type: "image/png" }),
    ]);

    await screen.findByText("업로드 실패");
    expect(screen.getByRole("button", { name: "첨부 확인" })).toBeDisabled();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/posts"),
    ).toHaveLength(0);
  });

  it("취소한 신규 첨부가 남아 있으면 기존 모인의 변경사항을 저장할 수 없다", async () => {
    const original = editMoin();
    mockedRequest.mockImplementation((path, options) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media")
        return new Promise((_resolve, reject) =>
          options?.signal?.addEventListener("abort", () =>
            reject(new DOMException("취소됨", "AbortError")),
          ),
        );
      return Promise.resolve(original);
    });
    renderComposer({ editMoin: original });
    await waitForMediaIntake();
    dispatchPaste(screen.getByLabelText("수정할 모인 내용"), [
      new File(["cancelled"], "취소.png", { type: "image/png" }),
    ]);
    await screen.findByText("업로드 중");

    fireEvent.click(screen.getByRole("button", { name: /업로드 취소$/ }));

    await screen.findByText("업로드 취소됨");
    expect(
      screen.getByRole("button", { name: "첨부 확인" }),
    ).toBeDisabled();
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/posts/moin-edit"),
    ).toHaveLength(0);
  });

  it("모달의 폼 밖 닫기 버튼에 포커스가 있어도 이미지를 한 번만 붙여넣는다", async () => {
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media") return Promise.resolve({ id: "media-dialog-paste" });
      return Promise.resolve({});
    });
    render(
      <ToastProvider>
        <Modal open onOpenChange={() => undefined} title="새 모인">
          <MoinComposer />
        </Modal>
      </ToastProvider>,
    );
    await waitForMediaIntake();
    const closeButton = screen.getByRole("button", { name: "창 닫기" });
    closeButton.focus();
    expect(closeButton).toHaveFocus();

    const event = dispatchPaste(closeButton, [
      new File(["dialog clipboard"], "dialog.png", {
        type: "image/png",
        lastModified: 10,
      }),
    ]);

    expect(event.defaultPrevented).toBe(true);
    await screen.findByText("1/1개 업로드 완료");
    expect(
      mockedRequest.mock.calls.filter(([path]) => path === "/media"),
    ).toHaveLength(1);
  });

  it("클립보드 이미지를 제거한 뒤에도 자동 파일명 번호를 재사용하지 않는다", async () => {
    let uploadCount = 0;
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === "/media") {
        uploadCount += 1;
        return Promise.resolve({ id: `media-sequence-${uploadCount}` });
      }
      return Promise.resolve({});
    });
    renderComposer();
    await waitForMediaIntake();
    const textarea = screen.getByLabelText("모인 내용");
    dispatchPaste(textarea, [
      new File(["first"], "first.png", { type: "image/png", lastModified: 1 }),
      new File(["second"], "second.png", { type: "image/png", lastModified: 2 }),
    ]);
    await screen.findByText("2/2개 업로드 완료");

    fireEvent.click(
      screen.getByRole("button", { name: "클립보드 이미지 1.png 제거" }),
    );
    await waitFor(() =>
      expect(screen.queryByText("클립보드 이미지 1.png")).not.toBeInTheDocument(),
    );
    dispatchPaste(textarea, [
      new File(["third"], "third.png", { type: "image/png", lastModified: 3 }),
    ]);

    await screen.findByText("2/2개 업로드 완료");
    expect(screen.getByText("클립보드 이미지 2.png")).toBeInTheDocument();
    expect(screen.getByText("클립보드 이미지 3.png")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "클립보드 이미지 3.png 제거" }),
    ).toBeInTheDocument();
  });

  it("미디어 추가 제어에 형식·한도 안내와 숨겨진 파일 입력 이름을 제공한다", async () => {
    const { input } = renderComposer();
    await waitForMediaIntake();
    const intake = screen.getByRole("button", { name: "미디어 추가 영역" });

    expect(intake).toHaveAccessibleDescription(/Ctrl\/⌘\+V/);
    expect(intake).toHaveAccessibleDescription(/JPEG·PNG·GIF·WebP/);
    expect(intake).toHaveAccessibleDescription(/0\/4개/);
    expect(input).toHaveAccessibleName("첨부할 이미지 또는 영상 파일");
  });

  it("@ 입력으로 사용자를 찾아 키보드로 멘션을 삽입한다", async () => {
    mockedRequest.mockImplementation((path) => {
      if (path === "/media/config")
        return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path.startsWith("/search?"))
        return Promise.resolve({ users: [{ id: "u2", username: "alice", displayName: "앨리스" }] });
      return Promise.resolve({});
    });
    renderComposer();
    await waitForMediaIntake();
    const textarea = screen.getByLabelText("모인 내용");
    fireEvent.change(textarea, { target: { value: "안녕 @ali", selectionStart: 7 } });

    expect(await screen.findByRole("option", { name: /앨리스.*@alice/ })).toBeInTheDocument();
    fireEvent.keyDown(textarea, { key: "Enter" });

    expect(textarea).toHaveValue("안녕 @alice ");
    expect(screen.queryByRole("listbox", { name: "멘션할 사용자" })).not.toBeInTheDocument();
  });
});
