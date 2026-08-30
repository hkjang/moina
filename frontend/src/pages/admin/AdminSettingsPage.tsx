import { Save } from "lucide-react";
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
import { AdminTitle } from "./components";
import { roleRows } from "./helpers";
import {
  mergeWorkflowActions,
  registeredWorkflowActions,
  splitWorkflowActions,
} from "../../utils/workflowActions";

interface SettingRow {
  key: string;
  value?: unknown;
  sensitive?: boolean;
  configured?: boolean;
  revision?: number;
}
interface WorkflowSettings {
  enabled?: boolean;
  actions?: string[];
  approverRoles?: string[];
}
export function AdminSettingsPage() {
  const { notify } = useToast();
  const settings = useApiQuery<{ items?: SettingRow[] } | SettingRow[]>(
    "/admin/settings",
  );
  const workflowQuery = useApiQuery<WorkflowSettings>("/admin/workflow");
  const rolesQuery = useApiQuery<unknown>("/admin/roles");
  const [working, setWorking] = useState<string | null>(null);
  const rows = Array.isArray(settings.data)
    ? settings.data
    : settings.data?.items || [];
  const findValue = <T,>(key: string, fallback: T): T => {
    const value = rows.find((item) => item.key === key)?.value;
    return value && typeof value === "object"
      ? { ...fallback, ...(value as T) }
      : fallback;
  };
  const [general, setGeneral] = useState({
    serviceName: "moina",
    sessionMinutes: 720,
    defaultTimezone: "Asia/Seoul",
    allowRegistration: false,
  });
  const [api, setAPI] = useState({
    enabled: true,
    mcpEnabled: true,
    rateLimitPerMinute: 120,
  });
  const [media, setMedia] = useState({
    maxUploadBytes: 10 * 1024 * 1024,
    maxPerPost: 4,
    orphanTtlHours: 24,
  });
  const [network, setNetwork] = useState<{ trustedProxies: string[] }>({
    trustedProxies: [],
  });
  const [workflow, setWorkflow] = useState<WorkflowSettings>({
    enabled: false,
    actions: ["post.publish"],
    approverRoles: [],
  });
  useEffect(() => {
    if (settings.data) {
      setGeneral(findValue("service.general", general));
      setAPI(findValue("api.access", api));
      setMedia(findValue("media.config", media));
      setNetwork(findValue("network.proxy", network));
    }
  }, [settings.data]);
  useEffect(() => {
    if (workflowQuery.data) setWorkflow(workflowQuery.data);
  }, [workflowQuery.data]);
  const saveSetting = async (key: string, value: unknown) => {
    setWorking(key);
    const row = rows.find((item) => item.key === key);
    try {
      await apiRequest(`/admin/settings/${encodeURIComponent(key)}`, {
        method: "PUT",
        body: { value, sensitive: false, revision: row?.revision },
      });
      notify("서비스 설정을 저장했습니다.", "success");
      settings.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };
  const saveWorkflow = async () => {
    setWorking("workflow");
    try {
      await apiRequest("/admin/workflow", { method: "PUT", body: workflow });
      notify("검토·승인 정책을 저장했습니다.", "success");
      workflowQuery.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };
  const roleNames = roleRows(rolesQuery.data)
    .filter(
      (role) =>
        role.permissions?.includes("approvals:review") ||
        role.permissions?.includes("approvals:*") ||
        role.permissions?.includes("*"),
    )
    .map((role) => role.name);
  const workflowActionGroups = splitWorkflowActions(workflow.actions);
  return (
    <div className="page-stack">
      <AdminTitle
        title="일반 설정"
        description="서비스 공통 정책과 API·MCP, 미디어, 선택형 검토·승인 프로세스를 관리합니다."
      />
      {settings.loading || workflowQuery.loading || rolesQuery.loading ? (
        <LoadingState />
      ) : settings.error || workflowQuery.error || rolesQuery.error ? (
        <ErrorState
          message={
            settings.error ||
            workflowQuery.error ||
            rolesQuery.error ||
            "설정을 불러오지 못했습니다."
          }
          onRetry={() => {
            settings.reload();
            workflowQuery.reload();
            rolesQuery.reload();
          }}
        />
      ) : (
        <div className="settings-layout">
          <Card>
            <SectionHeader
              title="서비스 기본"
              action={
                <Button
                  variant="primary"
                  onClick={() => void saveSetting("service.general", general)}
                  disabled={working === "service.general"}
                >
                  <Save />
                  저장
                </Button>
              }
            />
            <div className="form-grid">
              <Field label="서비스 이름">
                <input
                  value={general.serviceName}
                  onChange={(event) =>
                    setGeneral({ ...general, serviceName: event.target.value })
                  }
                />
              </Field>
              <Field label="세션 유지 시간(분)">
                <input
                  type="number"
                  min="5"
                  max="1440"
                  value={general.sessionMinutes}
                  onChange={(event) =>
                    setGeneral({
                      ...general,
                      sessionMinutes: Number(event.target.value),
                    })
                  }
                />
              </Field>
              <Field label="기본 시간대">
                <select
                  value={general.defaultTimezone}
                  onChange={(event) =>
                    setGeneral({
                      ...general,
                      defaultTimezone: event.target.value,
                    })
                  }
                >
                  <option value="Asia/Seoul">Asia/Seoul</option>
                  <option value="UTC">UTC</option>
                </select>
              </Field>
            </div>
            <SwitchField
              label="사용자 가입 허용"
              description="기본값은 꺼짐입니다. 끄면 관리자 생성 또는 OIDC 자동 등록만 허용합니다."
              checked={general.allowRegistration}
              onChange={(checked) =>
                setGeneral({ ...general, allowRegistration: checked })
              }
            />
          </Card>
          <Card>
            <SectionHeader
              title="API 및 MCP"
              description="개인 키를 사용하는 외부 연동 정책입니다."
              action={
                <Button
                  variant="primary"
                  onClick={() => void saveSetting("api.access", api)}
                  disabled={working === "api.access"}
                >
                  <Save />
                  저장
                </Button>
              }
            />
            <SwitchField
              label="개인 API 키 인증"
              description="사용자가 발급한 최소 권한 키로 REST API에 접근합니다."
              checked={api.enabled}
              onChange={(checked) =>
                setAPI({
                  ...api,
                  enabled: checked,
                  mcpEnabled: checked ? api.mcpEnabled : false,
                })
              }
            />
            <SwitchField
              label="Streamable HTTP MCP"
              description="개인 키 권한 체계로 /mcp 도구를 제공합니다."
              checked={api.mcpEnabled}
              disabled={!api.enabled}
              onChange={(checked) => setAPI({ ...api, mcpEnabled: checked })}
            />
            <Field label="키별 분당 요청 한도">
              <input
                type="number"
                min="1"
                max="10000"
                value={api.rateLimitPerMinute}
                onChange={(event) =>
                  setAPI({
                    ...api,
                    rateLimitPerMinute: Number(event.target.value),
                  })
                }
              />
            </Field>
          </Card>
          <Card>
            <SectionHeader
              title="이미지·영상 업로드"
              description="JPEG, PNG, GIF, WebP 이미지와 MP4, WebM 영상의 용량·개수·미사용 정리를 관리합니다."
              action={
                <Button
                  variant="primary"
                  onClick={() => void saveSetting("media.config", media)}
                  disabled={working === "media.config"}
                >
                  <Save />
                  저장
                </Button>
              }
            />
            <div className="form-grid">
              <Field label="파일당 최대 용량(MiB)" help="1~50MiB">
                <input
                  type="number"
                  min="1"
                  max="50"
                  value={Math.round(media.maxUploadBytes / (1024 * 1024))}
                  onChange={(event) =>
                    setMedia({
                      ...media,
                      maxUploadBytes: Number(event.target.value) * 1024 * 1024,
                    })
                  }
                />
              </Field>
              <Field label="모인당 최대 미디어" help="1~12개">
                <input
                  type="number"
                  min="1"
                  max="12"
                  value={media.maxPerPost}
                  onChange={(event) =>
                    setMedia({
                      ...media,
                      maxPerPost: Number(event.target.value),
                    })
                  }
                />
              </Field>
              <Field
                label="미사용 업로드 정리 시간"
                help="게시물에 연결되지 않은 파일을 정리할 때까지 1~720시간"
              >
                <input
                  type="number"
                  min="1"
                  max="720"
                  value={media.orphanTtlHours}
                  onChange={(event) =>
                    setMedia({
                      ...media,
                      orphanTtlHours: Number(event.target.value),
                    })
                  }
                />
              </Field>
            </div>
          </Card>
          <Card>
            <SectionHeader
              title="Reverse Proxy 신뢰 정책"
              description="직접 연결한 Peer가 목록에 있을 때만 Forwarded·X-Forwarded 헤더를 신뢰합니다. 현재 인스턴스에는 즉시, 다른 인스턴스에는 최대 30초 안에 반영됩니다."
              action={
                <Button
                  variant="primary"
                  onClick={() => void saveSetting("network.proxy", network)}
                  disabled={working === "network.proxy"}
                >
                  <Save />
                  저장
                </Button>
              }
            />
            <Field
              label="신뢰 Proxy IP 또는 CIDR"
              help="한 줄에 하나씩 입력하세요. 호스트 이름과 와일드카드는 허용하지 않습니다. 비워 두면 전달 헤더를 모두 무시합니다."
            >
              <textarea
                rows={5}
                spellCheck={false}
                placeholder={"예: 10.20.0.10\n10.30.0.0/24"}
                value={network.trustedProxies.join("\n")}
                onChange={(event) =>
                  setNetwork({
                    trustedProxies: event.target.value
                      .split(/[\n,]/)
                      .map((item) => item.trim())
                      .filter(Boolean),
                  })
                }
              />
            </Field>
            <p className="settings-note">
              감사 로그에는 소켓 IP, 계산된 실제 Client IP와 Proxy Chain을 각각 기록합니다.
            </p>
          </Card>
          <Card>
            <SectionHeader
              title="검토·승인 프로세스"
              description="설정이 없거나 꺼져 있으면 승인·반려 단계를 완전히 제외합니다."
              action={
                <Button
                  variant="primary"
                  onClick={() => void saveWorkflow()}
                  disabled={working === "workflow"}
                >
                  <Save />
                  정책 저장
                </Button>
              }
            />
            <SwitchField
              label="팀장 검토·승인 사용"
              description="지정 작업을 승인 대기로 보내고 승인 메뉴를 표시합니다."
              checked={workflow.enabled === true}
              onChange={(checked) =>
                setWorkflow({ ...workflow, enabled: checked })
              }
            />
            {workflow.enabled && (
              <div className="nested-settings">
                <fieldset className="permission-picker">
                  <legend>등록된 승인 작업</legend>
                  {registeredWorkflowActions.map((action) => (
                    <label key={action.value}>
                      <input
                        type="checkbox"
                        checked={workflowActionGroups.selected.includes(action.value)}
                        onChange={(event) => {
                          const selected = event.target.checked
                            ? [...workflowActionGroups.selected, action.value]
                            : workflowActionGroups.selected.filter((item) => item !== action.value);
                          setWorkflow({
                            ...workflow,
                            actions: mergeWorkflowActions(selected, workflowActionGroups.advanced),
                          });
                        }}
                      />
                      <span><strong>{action.label}</strong><small>{action.description} · <code>{action.value}</code></small></span>
                    </label>
                  ))}
                </fieldset>
                <details className="advanced-settings">
                  <summary>고급 패턴 직접 입력</summary>
                  <Field
                    label="Action 패턴"
                    help="한 줄에 하나씩 입력합니다. 전체는 *, 영역 wildcard는 post.*처럼 마지막 segment에만 사용할 수 있습니다."
                  >
                    <textarea
                      rows={4}
                      spellCheck={false}
                      value={workflowActionGroups.advanced.join("\n")}
                      placeholder={"예: post.*\n*"}
                      onChange={(event) =>
                        setWorkflow({
                          ...workflow,
                          actions: mergeWorkflowActions(workflowActionGroups.selected, event.target.value),
                        })
                      }
                    />
                  </Field>
                </details>
                <fieldset className="permission-picker">
                  <legend>승인 가능한 역할</legend>
                  {roleNames.map((role) => (
                    <label key={role}>
                      <input
                        type="checkbox"
                        checked={(workflow.approverRoles || []).includes(role)}
                        onChange={(event) =>
                          setWorkflow({
                            ...workflow,
                            approverRoles: event.target.checked
                              ? [...(workflow.approverRoles || []), role]
                              : (workflow.approverRoles || []).filter(
                                  (item) => item !== role,
                                ),
                          })
                        }
                      />
                      <span>{role}</span>
                    </label>
                  ))}
                </fieldset>
                {roleNames.length === 0 && (
                  <p className="field-error">
                    approvals:review 권한이 있는 역할을 먼저 만드세요.
                  </p>
                )}
              </div>
            )}
          </Card>
        </div>
      )}
    </div>
  );
}
