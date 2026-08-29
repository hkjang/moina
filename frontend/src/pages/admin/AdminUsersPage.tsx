import { Plus, RefreshCw, Search } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  LoadingState,
  Modal,
} from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { formatDate, listFrom } from "../../utils/format";
import { AdminTitle, Table } from "./components";
import { roleRows } from "./helpers";

interface AdminUser {
  id: string;
  username: string;
  displayName?: string;
  email?: string;
  provider?: string;
  roles?: string[];
  active?: boolean;
  createdAt?: string;
}

export function AdminUsersPage() {
  const { notify } = useToast();
  const query = useApiQuery<unknown>("/admin/users?limit=100");
  const rolesQuery = useApiQuery<unknown>("/admin/roles");
  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [working, setWorking] = useState<string | null>(null);
  const [draft, setDraft] = useState({
    username: "",
    displayName: "",
    email: "",
    password: "",
    role: "member",
  });
  const users = listFrom<AdminUser>(
    query.data as AdminUser[] | { items?: AdminUser[] } | undefined,
  ).filter((user) =>
    `${user.username} ${user.displayName} ${user.email}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const roles = roleRows(rolesQuery.data);
  useEffect(() => {
    if (roles.length && !roles.some((role) => role.name === draft.role))
      setDraft((current) => ({ ...current, role: roles[0].name }));
  }, [roles]);
  const create = async (event: FormEvent) => {
    event.preventDefault();
    setWorking("create");
    try {
      await apiRequest("/admin/users", {
        method: "POST",
        body: { ...draft, roles: [draft.role] },
      });
      notify("로컬 사용자를 추가했습니다.", "success");
      setCreateOpen(false);
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };
  const toggle = async (user: AdminUser) => {
    setWorking(user.id);
    try {
      await apiRequest(`/admin/users/${encodeURIComponent(user.id)}`, {
        method: "PATCH",
        body: { active: user.active === false },
      });
      notify(
        user.active === false
          ? "사용자를 활성화했습니다."
          : "사용자를 비활성화했습니다.",
        "success",
      );
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };
  const updateRole = async (user: AdminUser, role: string) => {
    setWorking(user.id);
    try {
      await apiRequest(`/admin/users/${encodeURIComponent(user.id)}`, {
        method: "PATCH",
        body: { roles: [role] },
      });
      notify("사용자 역할을 변경했습니다.", "success");
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
        title="사용자 관리"
        description="로컬·OIDC 사용자 상태와 역할을 관리합니다."
        actions={
          <Button variant="primary" onClick={() => setCreateOpen(true)}>
            <Plus />
            사용자 추가
          </Button>
        }
      />
      <Card>
        <div className="table-toolbar">
          <label className="table-search">
            <Search />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              aria-label="사용자 검색"
              placeholder="이름, 사용자 이름, 이메일 검색"
            />
          </label>
          <Button onClick={query.reload}>
            <RefreshCw />
            새로고침
          </Button>
        </div>
        {query.loading || rolesQuery.loading ? (
          <LoadingState />
        ) : query.error || rolesQuery.error ? (
          <ErrorState
            message={
              query.error ||
              rolesQuery.error ||
              "사용자 정보를 불러오지 못했습니다."
            }
            onRetry={() => {
              query.reload();
              rolesQuery.reload();
            }}
          />
        ) : users.length ? (
          <Table
            caption="사용자 목록"
            headers={["사용자", "인증", "역할", "상태", "등록일", "관리"]}
          >
            {users.map((user) => (
              <tr key={user.id}>
                <td>
                  <strong>{user.displayName || user.username}</strong>
                  <small>{user.email || `@${user.username}`}</small>
                </td>
                <td>{user.provider === "oidc" ? "Keycloak OIDC" : "로컬"}</td>
                <td>
                  <select
                    aria-label={`${user.username} 역할`}
                    value={user.roles?.[0] || ""}
                    onChange={(event) =>
                      void updateRole(user, event.target.value)
                    }
                    disabled={working === user.id}
                  >
                    {roles.map((role) => (
                      <option key={role.name} value={role.name}>
                        {role.name}
                      </option>
                    ))}
                  </select>
                </td>
                <td>
                  <Badge tone={user.active === false ? "danger" : "positive"}>
                    {user.active === false ? "비활성" : "활성"}
                  </Badge>
                </td>
                <td>{formatDate(user.createdAt, false)}</td>
                <td>
                  <Button
                    size="small"
                    variant="ghost"
                    onClick={() => void toggle(user)}
                    disabled={working === user.id}
                  >
                    {user.active === false ? "활성화" : "비활성화"}
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <EmptyState
            title="사용자가 없습니다"
            description="첫 사용자를 추가하거나 OIDC 자동 등록을 설정하세요."
          />
        )}
      </Card>
      <Modal
        open={createOpen}
        onOpenChange={setCreateOpen}
        title="로컬 사용자 추가"
        description="임시 비밀번호는 안전한 채널로 전달하세요."
      >
        <form className="settings-form" onSubmit={create}>
          <div className="form-grid">
            <Field label="사용자 이름">
              <input
                required
                value={draft.username}
                onChange={(event) =>
                  setDraft({ ...draft, username: event.target.value })
                }
              />
            </Field>
            <Field label="표시 이름">
              <input
                required
                value={draft.displayName}
                onChange={(event) =>
                  setDraft({ ...draft, displayName: event.target.value })
                }
              />
            </Field>
            <Field label="이메일">
              <input
                type="email"
                value={draft.email}
                onChange={(event) =>
                  setDraft({ ...draft, email: event.target.value })
                }
              />
            </Field>
            <Field label="초기 비밀번호" help="12자 이상">
              <input
                type="password"
                required
                minLength={12}
                value={draft.password}
                onChange={(event) =>
                  setDraft({ ...draft, password: event.target.value })
                }
              />
            </Field>
            <Field label="역할">
              <select
                value={draft.role}
                onChange={(event) =>
                  setDraft({ ...draft, role: event.target.value })
                }
              >
                {roles.map((role) => (
                  <option key={role.name}>{role.name}</option>
                ))}
              </select>
            </Field>
          </div>
          <div className="form-actions">
            <Button type="button" onClick={() => setCreateOpen(false)}>
              취소
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={working === "create"}
            >
              사용자 추가
            </Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
