import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/ToastProvider";
import { ApiError } from "../../api/client";
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
    redirectUrl: "",
    effectiveRedirectUrl: "https://moina.example/api/v1/auth/oidc/callback",
    defaultRedirectUrl: "https://moina.example/api/v1/auth/oidc/callback",
    redirectUrlSource: "publicBaseUrl" as const,
    defaultRedirectUrlSource: "publicBaseUrl" as const,
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
    mocks.oidc.redirectUrl = "";
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
      expect(body).not.toHaveProperty("effectiveRedirectUrl");
      expect(body).not.toHaveProperty("defaultRedirectUrl");
      expect(body).not.toHaveProperty("redirectUrlSource");
      expect(body).not.toHaveProperty("defaultRedirectUrlSource");
      expect(body).toMatchObject({
        clientSecret: "",
        issuerUrl: "https://keycloak.internal/realms/moina",
        allowedHosts: ["keycloak.internal"],
      });
    },
  );

  it("OIDC Secret 삭제 의도를 명시적으로 보낸다", async () => {
    renderPage(<AdminOIDCPage />);
    expect(
      await screen.findByText(/저장된 Secret이 토큰 교환에 사용됩니다/),
    ).toBeInTheDocument();
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
    expect(updateBody("/admin/oidc")).not.toHaveProperty(
      "effectiveRedirectUrl",
    );
    expect(updateBody("/admin/oidc")).not.toHaveProperty(
      "defaultRedirectUrl",
    );
    expect(updateBody("/admin/oidc")).not.toHaveProperty(
      "redirectUrlSource",
    );
    expect(updateBody("/admin/oidc")).not.toHaveProperty(
      "defaultRedirectUrlSource",
    );
  });

  it("OIDC 연결 테스트가 요구한 정확한 사설 Host를 양쪽 목록에 자동 입력한다", async () => {
    mocks.apiRequest
      .mockResolvedValueOnce({})
      .mockRejectedValueOnce(new ApiError(
        "OIDC Discovery 대상 ‘login.internal:8443’의 DNS 결과(10.0.0.8)가 사설망 주소입니다.",
        502,
        "oidc_private_host_required",
        {
          stage: "discovery",
          targetHost: "login.internal:8443",
          resolvedAddresses: ["10.0.0.8"],
          addressReason: "private_network_not_allowed",
          action: "add_private_host",
        },
      ));
    renderPage(<AdminOIDCPage />);
    await screen.findByDisplayValue("https://keycloak.internal/realms/moina");

    fireEvent.click(screen.getByRole("button", { name: "저장 후 연결 테스트" }));

    expect(await screen.findByText("login.internal:8443")).toBeInTheDocument();
    expect(screen.getByText(/MOINA 컨테이너의 DNS 결과: 10.0.0.8/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "이 Host 자동 입력" }));

    expect(screen.getByRole("textbox", { name: /^OIDC 허용 Host/ })).toHaveValue(
      "keycloak.internal\nlogin.internal:8443",
    );
    expect(screen.getByRole("textbox", { name: /^사설망 OIDC Host/ })).toHaveValue(
      "login.internal:8443",
    );
  });

  it("서버가 계산한 Callback URL을 표시하고 명시적 Redirect URL을 즉시 우선한다", async () => {
    renderPage(<AdminOIDCPage />);

    expect(
      await screen.findByText(
        "https://moina.example/api/v1/auth/oidc/callback",
      ),
    ).toBeInTheDocument();

    fireEvent.change(
      screen.getByRole("textbox", { name: /^고급 Redirect URI 직접 지정/ }),
      {
        target: {
          value: "https://login.moina.example/api/v1/auth/oidc/callback",
        },
      },
    );

    expect(
      screen.getByText("https://login.moina.example/api/v1/auth/oidc/callback"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/직접 지정한 Redirect URI가 사이트 기본 주소보다 우선/),
    ).toBeInTheDocument();
  });

  it("이전에 저장된 Redirect URI override를 사이트 기본 주소로 복원한다", async () => {
    mocks.oidc.redirectUrl =
      "https://old-moina.example/api/v1/auth/oidc/callback";
    renderPage(<AdminOIDCPage />);

    expect(
      await screen.findByText(
        "https://old-moina.example/api/v1/auth/oidc/callback",
      ),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: /사이트 기본 주소 사용/ }),
    );

    expect(
      screen.getByRole("textbox", { name: /^고급 Redirect URI 직접 지정/ }),
    ).toHaveValue("");
    expect(
      screen.getByText("https://moina.example/api/v1/auth/oidc/callback"),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "OIDC 설정 저장" }));
    await waitFor(() =>
      expect(updateBody("/admin/oidc")).toMatchObject({ redirectUrl: "" }),
    );
  });

  it("Issuer authority를 명시적으로 사설망 허용 목록에 추가하고 제거한다", async () => {
    renderPage(<AdminOIDCPage />);
    const privateHost = await screen.findByRole("switch", {
      name: /^Issuer 사설망 연결 허용/,
    });
    const privateHosts = screen.getByRole("textbox", {
      name: /^사설망 OIDC Host/,
    });

    expect(privateHost).not.toBeChecked();
    fireEvent.click(privateHost);
    expect(privateHost).toBeChecked();
    expect(privateHosts).toHaveValue("keycloak.internal");

    fireEvent.click(screen.getByRole("button", { name: "OIDC 설정 저장" }));
    await waitFor(() =>
      expect(updateBody("/admin/oidc")).toMatchObject({
        allowedHosts: ["keycloak.internal"],
        privateAllowedHosts: ["keycloak.internal"],
      }),
    );

    fireEvent.click(privateHost);
    expect(privateHost).not.toBeChecked();
    expect(privateHosts).toHaveValue("");
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
