import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/ToastProvider";
import { AdminSMTPPage } from "./AdminSMTPPage";

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(), reload: vi.fn(), reloadDeliveries: vi.fn(),
  settings: {
    enabled: true, host: "smtp.internal", port: 587, security: "starttls" as const,
    username: "mailer", fromAddress: "no-reply@example.com", fromName: "MOINA",
    timeoutSeconds: 15, allowPrivateNetwork: true, passwordConfigured: true,
    notify: { approval_requested: true, approval_decided: true, mention: true, echo: false, digest: true, security: true },
  } as Record<string, unknown>,
  deliveries: {
    items: [
      { id: "mail_1", event: "test", recipient: "admin@example.com", subject: "MOINA SMTP 연결 테스트", status: "sent", attempts: 1, createdAt: "2026-09-16T01:00:00Z", updatedAt: "2026-09-16T01:00:00Z" },
      { id: "ntf_2", event: "mention", recipient: "person@example.com", subject: "[MOINA] 멘션", status: "failed", attempts: 3, errorMessage: "SMTP 서버 연결 실패: connection refused", createdAt: "2026-09-16T00:00:00Z", updatedAt: "2026-09-16T00:10:00Z" },
    ],
    summary: { total: 2, status: { sent: 1, failed: 1, queued: 0 } },
  } as { items: unknown[]; summary: unknown },
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, apiRequest: mocks.apiRequest };
});
vi.mock("../../hooks/useApiQuery", () => ({
  useApiQuery: (path: string) => path.startsWith("/admin/smtp/deliveries")
    ? { data: mocks.deliveries, loading: false, error: null, reload: mocks.reloadDeliveries }
    : { data: mocks.settings, loading: false, error: null, reload: mocks.reload },
}));

const renderPage = () => render(<MemoryRouter><ToastProvider><AdminSMTPPage/></ToastProvider></MemoryRouter>);

describe("관리자 SMTP 설정", () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.reload.mockReset();
    mocks.reloadDeliveries.mockReset();
    mocks.apiRequest.mockResolvedValue({ recipient: "admin@example.com" });
    mocks.settings.enabled = true;
  });
  afterEach(() => cleanup());

  it("조회 전용 비밀번호 상태를 PUT에 보내지 않고 저장된 비밀번호를 유지한다", async () => {
    renderPage();
    await screen.findByDisplayValue("smtp.internal");
    fireEvent.click(screen.getByRole("button", { name: "SMTP 설정 저장" }));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith("/admin/smtp", expect.objectContaining({ method: "PUT" })));
    const body = mocks.apiRequest.mock.calls[0][1].body as Record<string, unknown>;
    expect(body).not.toHaveProperty("passwordConfigured");
    expect(body).toMatchObject({ password: "", clearPassword: false, host: "smtp.internal", allowPrivateNetwork: true, skipTlsVerify: false });
    expect(body.notify).toEqual({ approval_requested: true, approval_decided: true, mention: true, echo: false, digest: true, security: true });
  });

  it("이벤트 스위치를 끄면 그 종류만 notify에서 꺼져 저장된다", async () => {
    renderPage();
    await screen.findByDisplayValue("smtp.internal");
    const mention = screen.getByRole("switch", { name: /^멘션/ });
    expect(mention).toBeChecked();
    expect(screen.getByRole("switch", { name: /^내 Moin의 Echo/ })).not.toBeChecked();
    fireEvent.click(mention);
    fireEvent.click(screen.getByRole("button", { name: "SMTP 설정 저장" }));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalled());
    const body = mocks.apiRequest.mock.calls[0][1].body as { notify: Record<string, boolean> };
    expect(body.notify).toMatchObject({ mention: false, echo: false, approval_requested: true, digest: true });
  });

  it("발송 기록에 성공과 실패를 본문 없이 보여 준다", async () => {
    renderPage();
    await screen.findByDisplayValue("smtp.internal");
    const table = screen.getByRole("table", { name: "메일 발송 기록" });
    expect(table).toHaveTextContent("admin@example.com");
    expect(table).toHaveTextContent("MOINA SMTP 연결 테스트");
    expect(table).toHaveTextContent("보냄");
    expect(table).toHaveTextContent("실패");
    expect(table).toHaveTextContent("3회 시도");
    expect(table).toHaveTextContent("connection refused");
    expect(screen.getByText(/보냄 1 · 실패 1 · 대기 0/)).toBeInTheDocument();
  });

  it("메일이 꺼져 있고 기록도 없으면 발송 기록 카드와 이벤트 스위치를 그리지 않는다", async () => {
    mocks.settings.enabled = false;
    const previous = mocks.deliveries.items;
    mocks.deliveries.items = [];
    try {
      renderPage();
      await screen.findByRole("switch", { name: /이메일 알림 사용/ });
      expect(screen.queryByText("발송 기록")).not.toBeInTheDocument();
      expect(screen.queryByRole("switch", { name: /^멘션/ })).not.toBeInTheDocument();
    } finally {
      mocks.deliveries.items = previous;
    }
  });

  it("저장 완료 후 관리자 이메일로 테스트 메일을 보낸다", async () => {
    const calls: string[] = [];
    mocks.apiRequest.mockImplementation(async (path: string) => {
      calls.push(path);
      return path.endsWith("/test") ? { recipient: "admin@example.com" } : {};
    });
    renderPage();
    await screen.findByDisplayValue("smtp.internal");
    fireEvent.click(screen.getByRole("button", { name: "저장 후 테스트 메일" }));
    await waitFor(() => expect(calls).toEqual(["/admin/smtp", "/admin/smtp/test"]));
    expect(await screen.findByText(/admin@example.com로 테스트 메일/)).toBeInTheDocument();
    expect(mocks.reloadDeliveries).toHaveBeenCalled();
  });

  it("암호화 없음 선택 시 인증 정보와 저장된 비밀번호를 제거한다", async () => {
    renderPage();
    await screen.findByDisplayValue("smtp.internal");
    fireEvent.change(screen.getByRole("combobox", { name: "연결 보안" }), { target: { value: "none" } });
    fireEvent.click(screen.getByRole("button", { name: "SMTP 설정 저장" }));
    await waitFor(() => {
      const body = mocks.apiRequest.mock.calls[0][1].body as Record<string, unknown>;
      expect(body).toMatchObject({ security: "none", username: "", password: "", clearPassword: true });
    });
  });
});
