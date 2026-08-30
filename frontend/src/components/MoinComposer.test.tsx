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
import { MoinComposer } from "./MoinComposer";
import { ToastProvider } from "./ToastProvider";

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

function renderComposer() {
  const rendered = render(
    <ToastProvider>
      <MoinComposer onCreated={vi.fn()} />
    </ToastProvider>,
  );
  const input =
    rendered.container.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("미디어 입력을 찾지 못했습니다.");
  return { ...rendered, input };
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
    fireEvent.click(screen.getByRole("button", { name: "업로드 취소" }));
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
    fireEvent.click(screen.getAllByRole("button", { name: "업로드 취소" })[0]);
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

  it("관리자가 설정한 모인당 첨부 개수를 적용한다", async () => {
    mockedRequest.mockImplementation((path) =>
      path === "/media/config"
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 1 })
        : Promise.resolve({ id: "media-1" }),
    );
    const { input } = renderComposer();
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "이미지 또는 영상 첨부 (최대 1개)",
        }),
      ).toBeInTheDocument(),
    );
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
});
