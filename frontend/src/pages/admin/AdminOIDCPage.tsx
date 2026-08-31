import { LockKeyhole } from "lucide-react";
import { useEffect, useState } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import {
  Button,
  Card,
  ErrorState,
  Field,
  LoadingState,
  SectionHeader,
  SwitchField,
} from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import {
  endpointAuthority,
  ensureEndpointHost,
  formatAllowedHosts,
  parseAllowedHosts,
} from "../../utils/allowedHosts";
import { AdminTitle } from "./components";
import { roleRows } from "./helpers";

interface OIDCUpdateSettings {
  enabled?: boolean;
  issuerUrl?: string;
  clientId?: string;
  clientSecret?: string;
  clearClientSecret?: boolean;
  redirectUrl?: string;
  scopes?: string[];
  autoProvision?: boolean;
  defaultRoles?: string[];
  roleClaim?: string;
  roleMappings?: Record<string, string[]>;
  allowInsecureHttp?: boolean;
  allowedHosts?: string[];
  privateAllowedHosts?: string[];
}

interface OIDCSettingsView extends OIDCUpdateSettings {
  clientSecretConfigured?: boolean;
  effectiveRedirectUrl?: string;
}

export function AdminOIDCPage() {
  const { notify } = useToast();
  const query = useApiQuery<OIDCSettingsView>("/admin/oidc");
  const rolesQuery = useApiQuery<unknown>("/admin/roles");
  const [form, setForm] = useState<OIDCUpdateSettings>({
    enabled: false,
    scopes: ["openid", "profile", "email"],
    autoProvision: true,
    defaultRoles: [],
    roleClaim: "realm_access.roles",
    roleMappings: {},
  });
  const [mappingsText, setMappingsText] = useState("{}");
  const [hostsText, setHostsText] = useState("");
  const [privateHostsText, setPrivateHostsText] = useState("");
  const [clientSecretConfigured, setClientSecretConfigured] = useState(false);
  const [effectiveRedirectUrl, setEffectiveRedirectUrl] = useState("");
  const [working, setWorking] = useState<"save" | "test" | null>(null);
  const roles = roleRows(rolesQuery.data);
  const issuerAuthority = endpointAuthority(form.issuerUrl);
  const issuerPrivateAllowed =
    typeof issuerAuthority === "string" &&
    issuerAuthority.length > 0 &&
    parseAllowedHosts(privateHostsText).includes(issuerAuthority);
  const displayedRedirectUrl =
    form.redirectUrl?.trim() ||
    effectiveRedirectUrl ||
    `${window.location.origin}/api/v1/auth/oidc/callback`;
  useEffect(() => {
    if (query.data) {
      const {
        clientSecretConfigured: configured = false,
        effectiveRedirectUrl: effectiveRedirect = "",
        ...editable
      } = query.data;
      setClientSecretConfigured(configured);
      setEffectiveRedirectUrl(effectiveRedirect);
      setForm({
        ...editable,
        clientSecret: "",
        scopes: query.data.scopes?.length
          ? query.data.scopes
          : ["openid", "profile", "email"],
      });
      setMappingsText(JSON.stringify(query.data.roleMappings || {}, null, 2));
      setHostsText(formatAllowedHosts(query.data.allowedHosts));
      setPrivateHostsText(formatAllowedHosts(query.data.privateAllowedHosts));
    }
  }, [query.data]);
  const save = async (test = false) => {
    let roleMappings: Record<string, string[]>;
    try {
      const parsed = JSON.parse(mappingsText) as unknown;
      if (
        !parsed ||
        Array.isArray(parsed) ||
        typeof parsed !== "object" ||
        Object.values(parsed as Record<string, unknown>).some(
          (value) =>
            !Array.isArray(value) ||
            value.some((item) => typeof item !== "string"),
        )
      )
        throw new Error();
      roleMappings = parsed as Record<string, string[]>;
    } catch {
      notify(
        '역할 매핑은 {"Keycloak역할":["MOINA역할"]} 형식의 JSON이어야 합니다.',
        "error",
      );
      return;
    }
    const hostResult = ensureEndpointHost(
      parseAllowedHosts(hostsText),
      form.issuerUrl,
    );
    if (hostResult.invalid) {
      notify("Issuer URL 형식을 확인해 주세요.", "error");
      return;
    }
    if (hostResult.added) setHostsText(formatAllowedHosts(hostResult.hosts));
    setWorking(test ? "test" : "save");
    try {
      await apiRequest("/admin/oidc", {
        method: "PUT",
        // GET view의 clientSecretConfigured는 read-only이므로 저장 입력을
        // 명시적으로 구성한다. 서버는 알 수 없는 필드를 엄격히 거부한다.
        body: {
          enabled: form.enabled,
          issuerUrl: form.issuerUrl,
          clientId: form.clientId,
          clientSecret: form.clientSecret,
          clearClientSecret: form.clearClientSecret,
          redirectUrl: form.redirectUrl,
          scopes: form.scopes,
          autoProvision: form.autoProvision,
          defaultRoles: form.defaultRoles,
          roleClaim: form.roleClaim,
          roleMappings,
          allowInsecureHttp: form.allowInsecureHttp,
          allowedHosts: hostResult.hosts,
          privateAllowedHosts: parseAllowedHosts(privateHostsText),
        },
      });
      if (test) await apiRequest("/admin/oidc/test", { method: "POST" });
      notify(
        `${test ? "OIDC 설정을 저장하고 연결을 확인했습니다." : "OIDC 설정을 저장했습니다."}${hostResult.added ? ` ${hostResult.added} 호스트를 허용 목록에 자동 추가했습니다.` : ""}`,
        "success",
      );
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };
  return (
    <div className="page-stack">
      <AdminTitle
        title="Keycloak OIDC"
        description="Issuer URL, Client ID와 Client Secret만으로 SSO를 자동 연결합니다."
      />
      {query.loading || rolesQuery.loading ? (
        <LoadingState />
      ) : query.error || rolesQuery.error ? (
        <ErrorState
          message={
            query.error ||
            rolesQuery.error ||
            "OIDC 설정을 불러오지 못했습니다."
          }
          onRetry={() => {
            query.reload();
            rolesQuery.reload();
          }}
        />
      ) : (
        <Card>
          <SectionHeader
            title="OIDC 연결"
            description="Issuer Discovery 문서에서 인증·토큰 엔드포인트를 자동 구성합니다."
            action={
              <SwitchField
                label="Keycloak 로그인 사용"
                description="로그인 화면에 SSO 버튼을 표시합니다."
                checked={form.enabled === true}
                onChange={(enabled) => setForm({ ...form, enabled })}
              />
            }
          />
          <div className="form-grid">
            <Field label="Issuer URL">
              <input
                type="url"
                required={form.enabled}
                value={form.issuerUrl || ""}
                onChange={(event) =>
                  setForm({ ...form, issuerUrl: event.target.value })
                }
                placeholder="https://keycloak.internal/realms/moina"
              />
            </Field>
            <Field label="Client ID">
              <input
                required={form.enabled}
                value={form.clientId || ""}
                onChange={(event) =>
                  setForm({ ...form, clientId: event.target.value })
                }
                placeholder="moina"
              />
            </Field>
            <Field
              label="Client Secret"
              help={
                clientSecretConfigured
                  ? "비워두면 저장된 Secret을 유지합니다."
                  : "공개 클라이언트라면 비울 수 있습니다."
              }
            >
              <input
                type="password"
                value={form.clientSecret || ""}
                disabled={form.clearClientSecret}
                onChange={(event) =>
                  setForm({
                    ...form,
                    clientSecret: event.target.value,
                    clearClientSecret: false,
                  })
                }
              />
            </Field>
            <Field
              label="Redirect URL"
              help="비워두면 현재 접속 주소를 기준으로 자동 구성합니다."
            >
              <input
                type="url"
                value={form.redirectUrl || ""}
                onChange={(event) =>
                  setForm({ ...form, redirectUrl: event.target.value })
                }
                placeholder={`${window.location.origin}/api/v1/auth/oidc/callback`}
              />
            </Field>
            <Field label="Scopes">
              <input
                value={(form.scopes || []).join(" ")}
                onChange={(event) =>
                  setForm({
                    ...form,
                    scopes: event.target.value.split(/\s+/).filter(Boolean),
                  })
                }
              />
            </Field>
            <Field label="역할 Claim 경로">
              <input
                value={form.roleClaim || ""}
                onChange={(event) =>
                  setForm({ ...form, roleClaim: event.target.value })
                }
                placeholder="realm_access.roles"
              />
            </Field>
            <Field label="기본 역할">
              <select
                value={form.defaultRoles?.[0] || ""}
                onChange={(event) =>
                  setForm({ ...form, defaultRoles: [event.target.value] })
                }
              >
                <option value="">역할 선택</option>
                {roles.map((role) => (
                  <option key={role.name}>{role.name}</option>
                ))}
              </select>
            </Field>
          </div>
          <Field
            label="역할 매핑 JSON"
            help={'예: {"keycloak-admin":["admin"]}'}
          >
            <textarea
              rows={6}
              spellCheck={false}
              value={mappingsText}
              onChange={(event) => setMappingsText(event.target.value)}
            />
          </Field>
          <Field
            label="OIDC 허용 Host"
            help="줄바꿈 또는 쉼표로 구분합니다. Issuer hostname이 없으면 저장할 때 자동 추가합니다."
          >
            <textarea
              rows={4}
              spellCheck={false}
              value={hostsText}
              onChange={(event) => setHostsText(event.target.value)}
              placeholder={"keycloak.internal\nsso.internal"}
            />
          </Field>
          <Field
            label="사설망 OIDC Host"
            help="RFC1918·ULA로 해석되는 폐쇄망 DNS 이름만 명시합니다. IP, loopback, link-local과 metadata 주소는 항상 차단됩니다."
          >
            <textarea
              rows={3}
              spellCheck={false}
              value={privateHostsText}
              onChange={(event) => setPrivateHostsText(event.target.value)}
              placeholder="keycloak.internal"
            />
          </Field>
          <SwitchField
            label="Issuer 사설망 연결 허용"
            description={
              issuerAuthority
                ? `사설 IP로 해석되는 신뢰된 내부 OIDC에만 ${issuerAuthority} 접근을 허용하세요.`
                : "먼저 올바른 Issuer URL을 입력하세요."
            }
            checked={issuerPrivateAllowed}
            disabled={!issuerAuthority}
            onChange={(checked) => {
              if (!issuerAuthority) return;
              const privateHosts = parseAllowedHosts(privateHostsText).filter(
                (host) => host !== issuerAuthority,
              );
              if (checked) privateHosts.push(issuerAuthority);
              setPrivateHostsText(formatAllowedHosts(privateHosts));
              if (checked) {
                const hostResult = ensureEndpointHost(
                  parseAllowedHosts(hostsText),
                  form.issuerUrl,
                );
                if (!hostResult.invalid) {
                  setHostsText(formatAllowedHosts(hostResult.hosts));
                }
              }
            }}
          />
          {clientSecretConfigured && (
            <SwitchField
              label="저장된 Client Secret 삭제"
              description="저장하면 기존 Secret 암호문을 제거합니다."
              checked={form.clearClientSecret === true}
              onChange={(checked) =>
                setForm({
                  ...form,
                  clearClientSecret: checked,
                  clientSecret: checked ? "" : form.clientSecret,
                })
              }
            />
          )}
          <SwitchField
            label="첫 로그인 사용자 자동 등록"
            description="OIDC 프로필로 사용자를 만들고 기본 역할을 부여합니다."
            checked={form.autoProvision !== false}
            onChange={(checked) => setForm({ ...form, autoProvision: checked })}
          />
          <SwitchField
            label="폐쇄망 HTTP 허용"
            description="TLS가 없는 신뢰된 내부망에서만 명시적으로 사용하세요."
            checked={form.allowInsecureHttp === true}
            onChange={(checked) =>
              setForm({ ...form, allowInsecureHttp: checked })
            }
          />
          <div className="callback-box">
            <LockKeyhole />
            <span>
              <strong>Keycloak Valid redirect URI</strong>
              <code>{displayedRedirectUrl}</code>
            </span>
          </div>
          <div className="form-actions">
            <Button
              onClick={() => void save(true)}
              disabled={Boolean(working) || !form.issuerUrl || !form.clientId}
            >
              {working === "test" ? "연결 확인 중…" : "저장 후 연결 테스트"}
            </Button>
            <Button
              variant="primary"
              onClick={() => void save(false)}
              disabled={
                Boolean(working) ||
                (form.enabled && (!form.issuerUrl || !form.clientId))
              }
            >
              {working === "save" ? "저장 중…" : "OIDC 설정 저장"}
            </Button>
          </div>
        </Card>
      )}
    </div>
  );
}
