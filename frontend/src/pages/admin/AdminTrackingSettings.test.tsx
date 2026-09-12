import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/ToastProvider";
import { parseAllowedHosts, snippetBytes } from "../../utils/tracking";
import { AdminTrackingSettings } from "./AdminTrackingSettings";

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(), reload: vi.fn(),
  settings: {
    enabled: false, provider: "none", momentoUrl: "", momentoSiteId: "", momentoProxy: true, momentoAllowPrivateNetwork: false,
    measurementId: "", matomoUrl: "", matomoSiteId: "", customSnippet: "", allowedHosts: [] as string[], includeAdmin: false, placement: "head",
    providers: ["none", "momento", "ga4", "gtm", "matomo", "custom"], maxSnippetBytes: 8192, snippetPreview: "",
  },
  violations: { items: [] as Array<Record<string, unknown>> },
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, apiRequest: mocks.apiRequest };
});
vi.mock("../../hooks/useApiQuery", () => ({
  useApiQuery: (path: string) => ({
    data: path === "/admin/analytics" ? mocks.settings : mocks.violations,
    loading: false, error: null, reload: mocks.reload,
  }),
}));

const renderCard = () => render(<MemoryRouter><ToastProvider><AdminTrackingSettings/></ToastProvider></MemoryRouter>);

describe("관리자 방문 추적 설정", () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.reload.mockReset();
    mocks.apiRequest.mockResolvedValue({});
    mocks.settings = { ...mocks.settings, enabled: false, provider: "none", allowedHosts: [], customSnippet: "", snippetPreview: "" };
    mocks.violations = { items: [] };
  });
  afterEach(() => cleanup());

  it("꺼진 상태에서는 provider 입력을 숨기고 저장 시 enabled=false를 보낸다", async () => {
    renderCard();
    expect(screen.getByText("비활성")).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Provider/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "방문 추적 저장" }));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith("/admin/analytics", expect.objectContaining({ method: "PUT" })));
    const body = mocks.apiRequest.mock.calls[0][1].body as Record<string, unknown>;
    expect(body).toMatchObject({ enabled: false, provider: "none", allowedHosts: [] });
    expect(body).not.toHaveProperty("snippetPreview");
    expect(body).not.toHaveProperty("providers");
  });

  it("켜면 Momento가 첫 provider로 선택되고 프록시가 기본이며 허용 출처는 줄 단위로 보낸다", async () => {
    renderCard();
    fireEvent.click(screen.getByRole("switch", { name: /방문 추적 사용/ }));
    const provider = await screen.findByLabelText(/^Provider/);
    expect((provider as HTMLSelectElement).value).toBe("momento");
    expect((provider as HTMLSelectElement).options[0].value).toBe("momento");
    expect(screen.getByRole("switch", { name: /같은 오리진 프록시 사용/ })).toBeChecked();
    fireEvent.change(screen.getByLabelText(/Momento 수집기 주소/), { target: { value: "https://momento.corp.example" } });
    fireEvent.change(screen.getByLabelText("Momento 사이트 ID"), { target: { value: "site-7" } });
    fireEvent.change(screen.getByLabelText(/추가 허용 출처/), { target: { value: "https://a.example\n https://b.example:8443 \n\n" } });
    fireEvent.click(screen.getByRole("button", { name: "방문 추적 저장" }));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalled());
    const body = mocks.apiRequest.mock.calls[0][1].body as Record<string, unknown>;
    expect(body).toMatchObject({
      enabled: true, provider: "momento", momentoUrl: "https://momento.corp.example", momentoSiteId: "site-7", momentoProxy: true,
      allowedHosts: ["https://a.example", "https://b.example:8443"], includeAdmin: false, placement: "head",
    });
  });

  it("8KB를 넘는 추적 코드는 오류를 보이고 저장 버튼을 막는다", async () => {
    mocks.settings = { ...mocks.settings, enabled: true, provider: "custom" };
    renderCard();
    const textarea = await screen.findByLabelText(/추적 코드/);
    fireEvent.change(textarea, { target: { value: "<script>" + "x".repeat(8192) + "</script>" } });
    expect(await screen.findByRole("alert")).toHaveTextContent("8KB");
    expect(screen.getByRole("button", { name: "방문 추적 저장" })).toBeDisabled();
    expect(mocks.apiRequest).not.toHaveBeenCalled();
  });

  it("차단된 출처를 보여 주고 허용을 누르면 allow API를 부른 뒤 다시 읽는다", async () => {
    mocks.settings = { ...mocks.settings, enabled: true, provider: "custom", customSnippet: "<script src=\"https://t.example/t.js\"></script>" };
    mocks.violations = { items: [
      { origin: "https://collect.example", directive: "connect-src", page: "https://moina.example/flow", count: 12, firstSeen: "2026-09-12T10:00:00Z", lastSeen: "2026-09-12T10:05:00Z", allowed: false },
      { origin: "https://t.example", directive: "script-src", page: "https://moina.example/flow", count: 1, firstSeen: "2026-09-12T09:00:00Z", lastSeen: "2026-09-12T09:00:00Z", allowed: true },
    ] };
    renderCard();
    expect(await screen.findByText("https://collect.example")).toBeInTheDocument();
    expect(screen.getByText("허용됨")).toBeInTheDocument();
    const allowButtons = screen.getAllByRole("button", { name: "허용" });
    expect(allowButtons).toHaveLength(1);
    fireEvent.click(allowButtons[0]);
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith("/admin/analytics/violations/allow", { method: "POST", body: { origin: "https://collect.example" } }));
    await waitFor(() => expect(mocks.reload).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "기록 비우기" }));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith("/admin/analytics/violations", { method: "DELETE" }));
  });

  it("helpers: 허용 출처 파싱과 스니펫 바이트 계산", () => {
    expect(parseAllowedHosts("https://a.example, https://b.example\n\nhttps://c.example ")).toEqual(["https://a.example", "https://b.example", "https://c.example"]);
    expect(snippetBytes("한글")).toBe(6);
  });
});
