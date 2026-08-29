import { RefreshCw, Search } from "lucide-react";
import { useState } from "react";
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
import { formatDate, listFrom } from "../../utils/format";
import { AdminTitle, Table } from "./components";

interface AuditRow {
  id: string;
  actorUsername?: string;
  action?: string;
  target?: string;
  success?: boolean;
  ip?: string;
  createdAt?: string;
  detail?: unknown;
}
interface OutboxRow {
  id: string;
  type?: string;
  aggregateId?: string;
  attempts?: number;
  maxAttempts?: number;
  lastError?: string;
  deadLetteredAt?: string;
  createdAt?: string;
}
export function AdminAuditPage() {
  const { user } = useAuth();
  const { notify } = useToast();
  const [search, setSearch] = useState("");
  const [retrying, setRetrying] = useState<string | null>(null);
  const query = useApiQuery<unknown>(
    `/admin/audit?limit=100${search ? `&q=${encodeURIComponent(search)}` : ""}`,
  );
  const outbox = useApiQuery<unknown>(
    "/admin/outbox?status=dead_letter&limit=100",
  );
  const rows = listFrom<AuditRow>(
    query.data as AuditRow[] | { items?: AuditRow[] } | undefined,
  );
  const failed = listFrom<OutboxRow>(
    outbox.data as OutboxRow[] | { items?: OutboxRow[] } | undefined,
  );
  const canRetry = hasPermission(user?.permissions, "outbox:manage");
  const retry = async (event: OutboxRow) => {
    if (!canRetry) return;
    setRetrying(event.id);
    try {
      await apiRequest(`/admin/outbox/${encodeURIComponent(event.id)}/retry`, {
        method: "POST",
      });
      notify("실패 이벤트를 재처리 대기열로 보냈습니다.", "success");
      outbox.reload();
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setRetrying(null);
    }
  };
  return (
    <div className="page-stack">
      <AdminTitle
        title="감사 로그"
        description="로그인, 관리 설정, 권한과 콘텐츠 조치 이력을 변경 불가능한 기록으로 추적합니다."
      />
      <Card>
        <SectionHeader
          title="실패 이벤트 복구"
          description={
            canRetry
              ? "재시도 한도를 넘긴 알림·비동기 이벤트를 확인하고 안전하게 다시 처리합니다."
              : "실패 이벤트를 조회할 수 있습니다. 재처리는 outbox:manage 권한이 필요합니다."
          }
          action={
            <Button onClick={outbox.reload}>
              <RefreshCw />
              새로고침
            </Button>
          }
        />
        {outbox.loading ? (
          <LoadingState />
        ) : outbox.error ? (
          <ErrorState message={outbox.error} onRetry={outbox.reload} />
        ) : failed.length ? (
          <Table
            caption="실패 이벤트 목록"
            headers={[
              "이벤트",
              "대상",
              "시도",
              "마지막 오류",
              "실패 시각",
              "복구",
            ]}
          >
            {failed.map((event) => (
              <tr key={event.id}>
                <td>
                  <strong>{event.type || "알 수 없는 이벤트"}</strong>
                  <small>{event.id}</small>
                </td>
                <td>{event.aggregateId || "—"}</td>
                <td>
                  {event.attempts || 0}/{event.maxAttempts || 0}
                </td>
                <td className="table-content" title={event.lastError}>
                  {event.lastError || "오류 정보 없음"}
                </td>
                <td>{formatDate(event.deadLetteredAt || event.createdAt)}</td>
                <td>
                  {canRetry ? (
                    <Button
                      size="small"
                      variant="primary"
                      onClick={() => void retry(event)}
                      disabled={retrying === event.id}
                    >
                      <RefreshCw />
                      {retrying === event.id ? "재처리 중…" : "재처리"}
                    </Button>
                  ) : (
                    <small>조회 전용</small>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <EmptyState
            title="복구할 실패 이벤트가 없습니다"
            description="현재 모든 비동기 이벤트가 정상 처리되고 있습니다."
          />
        )}
      </Card>
      <Card>
        <div className="table-toolbar">
          <label className="table-search">
            <Search />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              aria-label="감사 로그 검색"
              placeholder="수행자, 작업, 대상 검색"
            />
          </label>
          <Button onClick={query.reload}>
            <RefreshCw />
            새로고침
          </Button>
        </div>
        {query.loading ? (
          <LoadingState />
        ) : query.error ? (
          <ErrorState message={query.error} onRetry={query.reload} />
        ) : rows.length ? (
          <Table
            caption="감사 로그"
            headers={["시각", "수행자", "작업", "대상", "결과", "IP", "상세"]}
          >
            {rows.map((row) => (
              <tr key={row.id}>
                <td>{formatDate(row.createdAt)}</td>
                <td>@{row.actorUsername || "system"}</td>
                <td>
                  <strong>{row.action || "활동"}</strong>
                </td>
                <td>{row.target || "—"}</td>
                <td>
                  <Badge tone={row.success === false ? "danger" : "positive"}>
                    {row.success === false ? "실패" : "성공"}
                  </Badge>
                </td>
                <td>
                  <code>{row.ip || "—"}</code>
                </td>
                <td>
                  {row.detail !== undefined && (
                    <details>
                      <summary>보기</summary>
                      <pre>{JSON.stringify(row.detail, null, 2)}</pre>
                    </details>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <EmptyState
            title="기록된 감사 이벤트가 없습니다"
            description="관리 활동이 시작되면 여기에 기록됩니다."
          />
        )}
      </Card>
    </div>
  );
}
