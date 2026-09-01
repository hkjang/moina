import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/ToastProvider";
import { AdminSMTPPage } from "./AdminSMTPPage";

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(), reload: vi.fn(),
  settings: {
    enabled: true, host: "smtp.internal", port: 587, security: "starttls" as const,
    username: "mailer", fromAddress: "no-reply@example.com", fromName: "MOINA",
    timeoutSeconds: 15, allowPrivateNetwork: true, passwordConfigured: true,
  },
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, apiRequest: mocks.apiRequest };
});
vi.mock("../../hooks/useApiQuery", () => ({
  useApiQuery: () => ({ data: mocks.settings, loading: false, error: null, reload: mocks.reload }),
}));

const renderPage = () => render(<MemoryRouter><ToastProvider><AdminSMTPPage/></ToastProvider></MemoryRouter>);

describe("관리자 SMTP 설정", () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.reload.mockReset();
    mocks.apiRequest.mockResolvedValue({ recipient: "admin@example.com" });
  });
  afterEach(() => cleanup());

  it("조회 전용 비밀번호 상태를 PUT에 보내지 않고 저장된 비밀번호를 유지한다", async () => {
    renderPage();
    await screen.findByDisplayValue("smtp.internal");
    fireEvent.click(screen.getByRole("button", { name: "SMTP 설정 저장" }));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith("/admin/smtp", expect.objectContaining({ method: "PUT" })));
    const body = mocks.apiRequest.mock.calls[0][1].body as Record<string, unknown>;
    expect(body).not.toHaveProperty("passwordConfigured");
    expect(body).toMatchObject({ password: "", clearPassword: false, host: "smtp.internal", allowPrivateNetwork: true });
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
