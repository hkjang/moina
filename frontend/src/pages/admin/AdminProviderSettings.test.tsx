import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/ToastProvider";
import { AdminAIPage } from "./AdminAIPage";
import { AdminOIDCPage } from "./AdminOIDCPage";

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(),
  reload: vi.fn(),
  oidc: {
    enabled: false,
    issuerUrl: "https://keycloak.internal/realms/moina",
    clientId: "moina",
    clientSecretConfigured: true,
    scopes: ["openid", "profile", "email"],
    autoProvision: true,
    defaultRoles: ["member"],
    roleClaim: "realm_access.roles",
    roleMappings: {},
    allowedHosts: ["keycloak.internal"],
    privateAllowedHosts: [],
    allowInsecureHttp: false,
  },
  ai: {
    enabled: false,
    baseUrl: "https://ai.internal/v1",
    apiKeyConfigured: true,
    model: "internal-model",
    apiStyle: "responses" as const,
    defaultMaxTokens: 4096,
    maxTokens: 262144,
    timeoutSeconds: 300,
    allowedHosts: ["ai.internal"],
    privateAllowedHosts: [],
    allowInsecureHttp: false,
  },
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>(
    "../../api/client",
  );
  return { ...actual, apiRequest: mocks.apiRequest };
});

vi.mock("../../hooks/useApiQuery", () => ({
  useApiQuery: (path: string) => ({
    data:
      path === "/admin/roles"
        ? { items: [{ name: "member", permissions: [] }] }
        : path === "/admin/ai"
          ? mocks.ai
          : mocks.oidc,
    loading: false,
    error: null,
    reload: mocks.reload,
  }),
}));

function renderPage(page: React.ReactNode) {
  return render(
    <MemoryRouter>
      <ToastProvider>{page}</ToastProvider>
    </MemoryRouter>,
  );
}

function updateBody(path: string) {
  const call = mocks.apiRequest.mock.calls.find(([calledPath]) => calledPath === path);
  expect(call).toBeDefined();
  return call?.[1]?.body as Record<string, unknown>;
}

describe("관리자 공급자 설정 저장 계약", () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.apiRequest.mockResolvedValue({});
    mocks.reload.mockReset();
    mocks.oidc.clientSecretConfigured = true;
  });

  afterEach(() => cleanup());

  it.each([true, false])(
    "OIDC 조회 전용 필드(%s)를 PUT에 재전송하지 않고 기존 Secret을 유지한다",
    async (configured) => {
      mocks.oidc.clientSecretConfigured = configured;
      renderPage(<AdminOIDCPage />);
      await screen.findByDisplayValue("https://keycloak.internal/realms/moina");

      fireEvent.click(screen.getByRole("button", { name: "OIDC 설정 저장" }));

      await waitFor(() =>
        expect(mocks.apiRequest).toHaveBeenCalledWith(
          "/admin/oidc",
          expect.objectContaining({ method: "PUT" }),
        ),
      );
      const body = updateBody("/admin/oidc");
      expect(body).not.toHaveProperty("clientSecretConfigured");
      expect(body).toMatchObject({
        clientSecret: "",
        issuerUrl: "https://keycloak.internal/realms/moina",
        allowedHosts: ["keycloak.internal"],
      });
    },
  );

  it("OIDC Secret 삭제 의도를 명시적으로 보낸다", async () => {
    renderPage(<AdminOIDCPage />);
    const clear = await screen.findByRole("switch", {
      name: /^저장된 Client Secret 삭제/,
    });
    fireEvent.click(clear);
    fireEvent.click(screen.getByRole("button", { name: "OIDC 설정 저장" }));

    await waitFor(() => expect(updateBody("/admin/oidc")).toMatchObject({
      clearClientSecret: true,
      clientSecret: "",
    }));
  });

  it("OIDC 연결 테스트는 올바른 설정 저장이 끝난 뒤 실행한다", async () => {
    const calls: string[] = [];
    mocks.apiRequest.mockImplementation(async (path: string) => {
      calls.push(path);
      return {};
    });
    renderPage(<AdminOIDCPage />);
    await screen.findByDisplayValue("https://keycloak.internal/realms/moina");

    fireEvent.click(screen.getByRole("button", { name: "저장 후 연결 테스트" }));

    await waitFor(() => expect(calls).toEqual(["/admin/oidc", "/admin/oidc/test"]));
    expect(updateBody("/admin/oidc")).not.toHaveProperty(
      "clientSecretConfigured",
    );
  });

  it("AI 조회 전용 필드도 PUT에 재전송하지 않는다", async () => {
    renderPage(<AdminAIPage />);
    fireEvent.click(screen.getByRole("button", { name: "AI 설정 저장" }));

    await waitFor(() =>
      expect(mocks.apiRequest).toHaveBeenCalledWith(
        "/admin/ai",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    const body = updateBody("/admin/ai");
    expect(body).not.toHaveProperty("apiKeyConfigured");
    expect(body).toMatchObject({
      apiKey: "",
      baseUrl: "https://ai.internal/v1",
      allowedHosts: ["ai.internal"],
    });
  });
});
