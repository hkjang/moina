import { RefreshCw } from "lucide-react";
import { useState, type FormEvent } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import { Badge, Button, Card, EmptyState, ErrorState, Field, LoadingState } from "../../components/ui";
import { Modal } from "../../components/Modal";
import { useApiQuery } from "../../hooks/useApiQuery";
import { formatDate, listFrom } from "../../utils/format";
import { AdminTitle, Table } from "./components";
import { statusTone } from "./helpers";

interface ReportRow {
  id: string;
  targetType?: string;
  targetId?: string;
  reason?: string;
  reporterUsername?: string;
  status?: string;
  createdAt?: string;
}
export function AdminReportsPage() {
  const { notify } = useToast();
  const query = useApiQuery<unknown>("/admin/reports?limit=100");
  const reports = listFrom<ReportRow>(
    query.data as ReportRow[] | { items?: ReportRow[] } | undefined,
  );
  const [decision, setDecision] = useState<{
    report: ReportRow;
    action: "resolve" | "reject";
    note: string;
  } | null>(null);
  const [saving, setSaving] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!decision) return;
    setSaving(true);
    try {
      await apiRequest(
        `/admin/reports/${encodeURIComponent(decision.report.id)}`,
        {
          method: "PATCH",
          body: {
            status: decision.action === "resolve" ? "resolved" : "dismissed",
            resolution: decision.note,
          },
        },
      );
      notify(
        decision.action === "resolve"
          ? "신고를 처리했습니다."
          : "신고를 기각했습니다.",
        "success",
      );
      setDecision(null);
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
        title="신고·제재"
        description="신고 내용을 검토하고 일관된 운영 정책에 따라 처리합니다."
        actions={
          <Button onClick={query.reload}>
            <RefreshCw />
            새로고침
          </Button>
        }
      />
      <Card>
        {query.loading ? (
          <LoadingState />
        ) : query.error ? (
          <ErrorState message={query.error} onRetry={query.reload} />
        ) : reports.length ? (
          <Table
            caption="신고 목록"
            headers={["대상", "신고 사유", "신고자", "상태", "접수일", "검토"]}
          >
            {reports.map((report) => (
              <tr key={report.id}>
                <td>
                  <strong>
                    {report.targetType || "대상"} · {report.targetId}
                  </strong>
                  <small>{report.id}</small>
                </td>
                <td>{report.reason || "사유 없음"}</td>
                <td>@{report.reporterUsername || "알 수 없음"}</td>
                <td>
                  <Badge tone={statusTone(report.status || "pending")}>
                    {report.status || "검토 대기"}
                  </Badge>
                </td>
                <td>{formatDate(report.createdAt)}</td>
                <td>
                  <div className="row-actions">
                    <Button
                      size="small"
                      onClick={() =>
                        setDecision({ report, action: "reject", note: "" })
                      }
                    >
                      기각
                    </Button>
                    <Button
                      size="small"
                      variant="danger"
                      onClick={() =>
                        setDecision({ report, action: "resolve", note: "" })
                      }
                    >
                      제재 처리
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        ) : (
          <EmptyState
            title="대기 중인 신고가 없습니다"
            description="새 신고가 접수되면 이곳에서 검토할 수 있습니다."
          />
        )}
      </Card>
      <Modal
        open={Boolean(decision)}
        onOpenChange={(open) => !open && setDecision(null)}
        title={decision?.action === "resolve" ? "신고 제재 처리" : "신고 기각"}
        description="결정과 메모는 감사 로그에 기록됩니다."
      >
        {decision && (
          <form className="settings-form" onSubmit={submit}>
            <Field label="검토 메모">
              <textarea
                required
                rows={4}
                value={decision.note}
                onChange={(event) =>
                  setDecision({ ...decision, note: event.target.value })
                }
              />
            </Field>
            <div className="form-actions">
              <Button type="button" onClick={() => setDecision(null)}>
                취소
              </Button>
              <Button
                type="submit"
                variant={decision.action === "resolve" ? "danger" : "primary"}
                disabled={saving}
              >
                {saving ? "처리 중…" : "결정 확정"}
              </Button>
            </div>
          </form>
        )}
      </Modal>
    </div>
  );
}
