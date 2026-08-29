import { Save } from "lucide-react";
import { useEffect, useState } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useAuth } from "../../auth/AuthContext";
import { useToast } from "../../components/ToastProvider";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingState,
  SectionHeader,
} from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { hasPermission } from "../../navigation";
import { AdminTitle } from "./components";
import { roleRows } from "./helpers";

const permissionCatalog = [
  ["admin:access", "서비스 관리자 진입"],
  ["users:manage", "사용자 관리"],
  ["posts:manage", "모인 관리"],
  ["moderation:manage", "신고·제재 관리"],
  ["approvals:review", "검토·승인"],
  ["roles:manage", "역할·권한 관리"],
  ["settings:manage", "서비스 설정"],
  ["audit:read", "감사 로그 조회"],
  ["outbox:manage", "실패 이벤트 재처리"],
  ["posts:read", "모인 조회"],
  ["posts:write", "모인 작성"],
  ["social:write", "Link·Signal 활동"],
  ["ai:use", "AI 사용"],
  ["mcp:use", "MCP 사용"],
  ["keys:manage", "개인 키 관리"],
] as const;
export function AdminRolesPage() {
  const { user } = useAuth();
  const { notify } = useToast();
  const query = useApiQuery<unknown>("/admin/roles");
  const roles = roleRows(query.data);
  const [selected, setSelected] = useState("");
  const [draft, setDraft] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const role = roles.find((item) => item.name === selected) || roles[0];
  const locked =
    role?.name === "super_admin" ||
    !hasPermission(user?.permissions, "roles:manage");
  useEffect(() => {
    if (roles.length && !selected) setSelected(roles[0].name);
  }, [roles, selected]);
  useEffect(() => {
    if (role) setDraft(role.permissions || []);
  }, [role?.name]);
  const save = async () => {
    if (!role) return;
    setSaving(true);
    try {
      await apiRequest("/admin/roles", {
        method: "PUT",
        body: {
          roles: roles.map((item) =>
            item.name === role.name ? { ...item, permissions: draft } : item,
          ),
        },
      });
      notify("역할 권한 정책을 저장했습니다.", "success");
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="page-stack">
      <AdminTitle
        title="역할·권한"
        description="역할별 기능 권한을 변경하고 개인 키의 최대 권한 범위를 제어합니다."
      />
      {query.loading ? (
        <LoadingState />
      ) : query.error ? (
        <ErrorState message={query.error} onRetry={query.reload} />
      ) : roles.length ? (
        <div className="role-layout">
          <Card className="role-sidebar">
            <SectionHeader title="역할" />
            {roles.map((item) => (
              <button
                type="button"
                className={role?.name === item.name ? "active" : ""}
                onClick={() => setSelected(item.name)}
                key={item.name}
              >
                <span>
                  <strong>{item.name}</strong>
                  <small>{item.permissions?.length || 0}개 권한</small>
                </span>
                {item.name === "super_admin" && <Badge>보호</Badge>}
              </button>
            ))}
          </Card>
          <Card>
            <SectionHeader
              title={role?.name || "역할"}
              description={
                locked
                  ? "최고 관리자 잠금을 방지하기 위해 이 역할은 읽기 전용입니다."
                  : "허용할 기능을 선택하세요."
              }
              action={
                <Button
                  variant="primary"
                  onClick={() => void save()}
                  disabled={locked || saving}
                >
                  <Save />
                  {saving ? "저장 중…" : "권한 저장"}
                </Button>
              }
            />
            <div className="permission-grid">
              {permissionCatalog.map(([permission, label]) => (
                <label key={permission}>
                  <input
                    type="checkbox"
                    checked={draft.includes(permission) || draft.includes("*")}
                    disabled={locked || draft.includes("*")}
                    onChange={(event) =>
                      setDraft(
                        event.target.checked
                          ? [...draft, permission]
                          : draft.filter((item) => item !== permission),
                      )
                    }
                  />
                  <span>
                    <strong>{label}</strong>
                    <code>{permission}</code>
                  </span>
                </label>
              ))}
            </div>
          </Card>
        </div>
      ) : (
        <EmptyState
          title="역할이 없습니다"
          description="서비스에는 최소 하나의 super_admin 역할이 필요합니다."
        />
      )}
    </div>
  );
}
