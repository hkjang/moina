import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/ToastProvider";
import { AdminSettingsPage } from "./AdminSettingsPage";

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(),
  reload: vi.fn(),
  settings: {
    items: [
      {
        key: "service.general",
        value: {
          serviceName: "MOINA",
          publicBaseUrl: "https://moina.example",
          sessionMinutes: 720,
          defaultTimezone: "Asia/Seoul",
          allowRegistration: false,
        },
        revision: 3,
      },
    ],
  },
  workflow: {
    enabled: false,
    actions: ["post.publish"],
    approverRoles: [],
  },
  roles: { items: [] },
}));

vi.mock("../../api/client", async () => {
  const actual =
    await vi.importActual<typeof import("../../api/client")>(
      "../../api/client",
    );
  return { ...actual, apiRequest: mocks.apiRequest };
});

vi.mock("../../hooks/useApiQuery", () => ({
  useApiQuery: (path: string) => ({
    data:
      path === "/admin/settings"
        ? mocks.settings
        : path === "/admin/workflow"
          ? mocks.workflow
          : mocks.roles,
    loading: false,
    error: null,
    reload: mocks.reload,
  }),
}));

describe("관리자 서비스 기본 설정", () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.apiRequest.mockResolvedValue({});
    mocks.reload.mockReset();
  });

  afterEach(() => cleanup());

  it("사이트 기본 주소를 불러오고 service.general에 저장한다", async () => {
    render(
      <MemoryRouter>
        <ToastProvider>
          <AdminSettingsPage />
        </ToastProvider>
      </MemoryRouter>,
    );

    const input = await screen.findByRole("textbox", {
      name: /^사이트 기본 주소/,
    });
    expect(input).toHaveValue("https://moina.example");
    fireEvent.change(input, {
      target: { value: "https://community.example" },
    });

    const card = screen
      .getByRole("heading", { name: "서비스 기본" })
      .closest("section");
    expect(card).not.toBeNull();
    fireEvent.click(
      within(card as HTMLElement).getByRole("button", { name: "저장" }),
    );

    await waitFor(() =>
      expect(mocks.apiRequest).toHaveBeenCalledWith(
        "/admin/settings/service.general",
        expect.objectContaining({
          method: "PUT",
          body: expect.objectContaining({
            value: expect.objectContaining({
              publicBaseUrl: "https://community.example",
            }),
          }),
        }),
      ),
    );
  });
});
