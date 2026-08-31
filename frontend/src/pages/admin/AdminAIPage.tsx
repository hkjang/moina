import { Bot } from "lucide-react";
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
  ensureEndpointHost,
  formatAllowedHosts,
  parseAllowedHosts,
} from "../../utils/allowedHosts";
import { AdminTitle } from "./components";

interface AIUpdateSettings {
  enabled?: boolean;
  baseUrl?: string;
  apiKey?: string;
  clearApiKey?: boolean;
  model?: string;
  apiStyle?: "responses" | "chat_completions";
  defaultMaxTokens?: number;
  maxTokens?: number;
  timeoutSeconds?: number;
  allowInsecureHttp?: boolean;
  allowedHosts?: string[];
  privateAllowedHosts?: string[];
}

interface AISettingsView extends AIUpdateSettings {
  apiKeyConfigured?: boolean;
}

export function AdminAIPage() {
  const { notify } = useToast();
  const query = useApiQuery<AISettingsView>("/admin/ai");
  const [form, setForm] = useState<AIUpdateSettings>({
    enabled: false,
    apiStyle: "responses",
    defaultMaxTokens: 4096,
    maxTokens: 262144,
    timeoutSeconds: 300,
  });
  const [hostsText, setHostsText] = useState("");
  const [privateHostsText, setPrivateHostsText] = useState("");
  const [apiKeyConfigured, setApiKeyConfigured] = useState(false);
  const [working, setWorking] = useState<"save" | "test" | null>(null);
  useEffect(() => {
    if (query.data) {
      const { apiKeyConfigured: configured = false, ...editable } = query.data;
      setApiKeyConfigured(configured);
      setForm({ ...editable, apiKey: "" });
      setHostsText(formatAllowedHosts(query.data.allowedHosts));
      setPrivateHostsText(formatAllowedHosts(query.data.privateAllowedHosts));
    }
  }, [query.data]);
  const save = async (test = false) => {
    const hostResult = ensureEndpointHost(
      parseAllowedHosts(hostsText),
      form.baseUrl,
    );
    if (hostResult.invalid)
      return notify("API 기본 주소 형식을 확인해 주세요.", "error");
    if (hostResult.added) setHostsText(formatAllowedHosts(hostResult.hosts));
    setWorking(test ? "test" : "save");
    try {
      await apiRequest("/admin/ai", {
        method: "PUT",
        // GET view의 apiKeyConfigured는 read-only이므로 저장 입력을
        // 명시적으로 구성한다. 서버는 알 수 없는 필드를 엄격히 거부한다.
        body: {
          enabled: form.enabled,
          baseUrl: form.baseUrl,
          apiKey: form.apiKey,
          clearApiKey: form.clearApiKey,
          model: form.model,
          apiStyle: form.apiStyle,
          defaultMaxTokens: form.defaultMaxTokens,
          maxTokens: form.maxTokens,
          timeoutSeconds: form.timeoutSeconds,
          allowInsecureHttp: form.allowInsecureHttp,
          allowedHosts: hostResult.hosts,
          privateAllowedHosts: parseAllowedHosts(privateHostsText),
        },
      });
      if (test) await apiRequest("/admin/ai/test", { method: "POST" });
      notify(
        `${test ? "AI 설정을 저장하고 스트리밍 연결을 확인했습니다." : "AI 설정을 저장했습니다."}${hostResult.added ? ` ${hostResult.added} 호스트를 허용 목록에 자동 추가했습니다.` : ""}`,
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
        title="AI 설정"
        description="OpenAI 호환 공급자와 최대 256K 토큰 스트리밍 정책을 관리합니다."
      />
      {query.loading ? (
        <LoadingState />
      ) : query.error ? (
        <ErrorState message={query.error} onRetry={query.reload} />
      ) : (
        <Card>
          <SectionHeader
            title="AI 공급자"
            description="자격 증명은 ENCRYPTION_KEY로 암호화되고 화면에 다시 노출되지 않습니다."
            action={
              <SwitchField
                label="AI 기능 사용"
                description="ai:use 권한 사용자에게 메뉴를 표시합니다."
                checked={form.enabled === true}
                onChange={(enabled) => setForm({ ...form, enabled })}
              />
            }
          />
          {form.enabled && (
            <>
              <div className="form-grid">
                <Field
                  label="API 기본 주소"
                  help="오프라인망의 OpenAI 호환 엔드포인트를 입력하세요."
                >
                  <input
                    type="url"
                    required
                    value={form.baseUrl || ""}
                    onChange={(event) =>
                      setForm({ ...form, baseUrl: event.target.value })
                    }
                    placeholder="https://ai.internal/v1"
                  />
                </Field>
                <Field label="모델 이름">
                  <input
                    required
                    value={form.model || ""}
                    onChange={(event) =>
                      setForm({ ...form, model: event.target.value })
                    }
                  />
                </Field>
                <Field
                  label="API Key"
                  help={
                    apiKeyConfigured
                      ? "비워두면 저장된 키를 유지합니다."
                      : "공급자가 요구하는 경우 입력하세요."
                  }
                >
                  <input
                    type="password"
                    value={form.apiKey || ""}
                    disabled={form.clearApiKey}
                    onChange={(event) =>
                      setForm({
                        ...form,
                        apiKey: event.target.value,
                        clearApiKey: false,
                      })
                    }
                  />
                </Field>
                <Field label="API 방식">
                  <select
                    value={form.apiStyle}
                    onChange={(event) =>
                      setForm({
                        ...form,
                        apiStyle: event.target.value as AIUpdateSettings["apiStyle"],
                      })
                    }
                  >
                    <option value="responses">Responses API</option>
                    <option value="chat_completions">
                      Chat Completions API
                    </option>
                  </select>
                </Field>
                <Field label="사용자 기본 최대 토큰">
                  <input
                    type="number"
                    min="1"
                    max="262144"
                    value={form.defaultMaxTokens || 4096}
                    onChange={(event) =>
                      setForm({
                        ...form,
                        defaultMaxTokens: Math.min(
                          262144,
                          Math.max(1, Number(event.target.value)),
                        ),
                      })
                    }
                  />
                </Field>
                <Field label="서비스 최대 토큰" help="최대 262,144(256K)">
                  <input
                    type="number"
                    min="1"
                    max="262144"
                    value={form.maxTokens || 262144}
                    onChange={(event) =>
                      setForm({
                        ...form,
                        maxTokens: Math.min(
                          262144,
                          Math.max(1, Number(event.target.value)),
                        ),
                      })
                    }
                  />
                </Field>
                <Field label="요청 제한 시간(초)">
                  <input
                    type="number"
                    min="10"
                    max="3600"
                    value={form.timeoutSeconds || 300}
                    onChange={(event) =>
                      setForm({
                        ...form,
                        timeoutSeconds: Number(event.target.value),
                      })
                    }
                  />
                </Field>
                <Field
                  label="AI 허용 Host"
                  help="줄바꿈 또는 쉼표로 구분합니다. API hostname이 없으면 저장할 때 자동 추가합니다."
                >
                  <textarea
                    rows={4}
                    spellCheck={false}
                    value={hostsText}
                    onChange={(event) => setHostsText(event.target.value)}
                    placeholder={"ai.internal\nvllm.internal"}
                  />
                </Field>
                <Field
                  label="사설망 AI Host"
                  help="RFC1918·ULA로 해석되는 폐쇄망 DNS 이름만 명시합니다. 전체 허용 Host에도 같은 이름이 있어야 합니다."
                >
                  <textarea
                    rows={3}
                    spellCheck={false}
                    value={privateHostsText}
                    onChange={(event) =>
                      setPrivateHostsText(event.target.value)
                    }
                    placeholder="ai.internal"
                  />
                </Field>
              </div>
              {apiKeyConfigured && (
                <SwitchField
                  label="저장된 API Key 삭제"
                  description="저장하면 기존 API Key 암호문을 제거합니다."
                  checked={form.clearApiKey === true}
                  onChange={(checked) =>
                    setForm({
                      ...form,
                      clearApiKey: checked,
                      apiKey: checked ? "" : form.apiKey,
                    })
                  }
                />
              )}
              <SwitchField
                label="폐쇄망 HTTP 허용"
                description="TLS가 없는 신뢰된 내부 AI 엔드포인트에서만 사용하세요."
                checked={form.allowInsecureHttp === true}
                onChange={(checked) =>
                  setForm({ ...form, allowInsecureHttp: checked })
                }
              />
              <div className="streaming-note">
                <Bot />
                <span>
                  <strong>응답은 항상 SSE로 스트리밍됩니다.</strong>
                  <p>
                    사용자는 관리자가 정한 서비스 상한 안에서 요청별 토큰 수를
                    선택할 수 있습니다.
                  </p>
                </span>
              </div>
            </>
          )}
          <div className="form-actions">
            <Button
              onClick={() => void save(true)}
              disabled={
                Boolean(working) ||
                (form.enabled && (!form.baseUrl || !form.model))
              }
            >
              {working === "test" ? "연결 확인 중…" : "저장 후 연결 테스트"}
            </Button>
            <Button
              variant="primary"
              onClick={() => void save(false)}
              disabled={
                Boolean(working) ||
                (form.enabled && (!form.baseUrl || !form.model))
              }
            >
              {working === "save" ? "저장 중…" : "AI 설정 저장"}
            </Button>
          </div>
        </Card>
      )}
    </div>
  );
}
