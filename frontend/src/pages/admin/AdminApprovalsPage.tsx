import { Check, RefreshCw, X } from "lucide-react";
import { useState, type FormEvent } from "react";
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
  SectionHeader,
} from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { formatDate, listFrom } from "../../utils/format";
import { AdminTitle, Table } from "./components";

interface ApprovalRow {
  id: string;
  action?: string;
  targetType?: string;
  targetId?: string;
  requesterUsername?: string;
  status?: string;
  summary?: string;
  createdAt?: string;
}
export function AdminApprovalsPage() {
  const { notify } = useToast();
  const status = useApiQuery<{
    enabled?: boolean;
    approvalEnabled?: boolean;
    pending?: number;
  }>("/workflow/status");
  const query = useApiQuery<unknown>("/approvals?limit=100");
  const approvals = listFrom<ApprovalRow>(
    query.data as ApprovalRow[] | { items?: ApprovalRow[] } | undefined,
  );
  const [decision, setDecision] = useState<{
    item: ApprovalRow;
    type: "approve" | "reject";
    note: string;
  } | null>(null);
  const [saving, setSaving] = useState(false);
  const enabled = status.data?.enabled ?? status.data?.approvalEnabled;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!decision) return;
    setSaving(true);
    try {
      await apiRequest(
        `/approvals/${encodeURIComponent(decision.item.id)}/${decision.type}`,
        { method: "POST", body: { comment: decision.note } },
      );
      notify(
        decision.type === "approve"
          ? "요청을 승인했습니다."
          : "요청을 반려했습니다.",
        "success",
      );
      setDecision(null);
      query.reload();
      status.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="page-stack">
      <AdminTitle
        title="검토·승인"
        description="관리자가 활성화한 팀장 검토 프로세스의 요청을 승인하거나 반려합니다."
      />
      {status.loading ? (
        <LoadingState />
      ) : status.error ? (
        <ErrorState message={status.error} onRetry={status.reload} />
      ) : !enabled && approvals.length === 0 ? (
        <Card>
          <EmptyState
            title="승인 프로세스가 꺼져 있습니다"
            description="현재 새 요청에는 검토·승인·반려 단계가 적용되지 않습니다. 일반 설정에서 필요할 때 켤 수 있습니다."
          />
        </Card>
      ) : (
        <Card>
          <SectionHeader
            title="승인 대기 요청"
            description={
              !enabled
                ? "프로세스를 끄기 전에 생성된 요청만 표시합니다."
                : "승인만으로 외부 작업이 자동 실행되지는 않습니다."
            }
            action={
              <Button onClick={query.reload}>
                <RefreshCw />
                새로고침
              </Button>
            }
          />
          {query.loading ? (
            <LoadingState />
          ) : query.error ? (
            <ErrorState message={query.error} onRetry={query.reload} />
          ) : approvals.length ? (
            <Table
              caption="승인 요청 목록"
              headers={[
                "요청",
                "대상",
                "요청자",
                "요약",
                "상태",
                "요청일",
                "검토",
              ]}
            >
              {approvals.map((item) => (
                <tr key={item.id}>
                  <td>
                    <strong>{item.action || "설정 변경"}</strong>
                    <small>{item.id}</small>
                  </td>
                  <td>
                    {item.targetType} · {item.targetId}
                  </td>
                  <td>@{item.requesterUsername || "시스템"}</td>
                  <td>{item.summary || "상세 내용을 확인하세요."}</td>
                  <td>
                    <Badge tone="warning">{item.status || "승인 대기"}</Badge>
                  </td>
                  <td>{formatDate(item.createdAt)}</td>
                  <td>
                    <div className="row-actions">
                      <Button
                        size="small"
                        onClick={() =>
                          setDecision({ item, type: "reject", note: "" })
                        }
                      >
                        <X />
                        반려
                      </Button>
                      <Button
                        size="small"
                        variant="primary"
                        onClick={() =>
                          setDecision({ item, type: "approve", note: "" })
                        }
                      >
                        <Check />
                        승인
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </Table>
          ) : (
            <EmptyState
              title="처리할 승인 요청이 없습니다"
              description="정책에 맞는 요청이 생성되면 여기에 표시됩니다."
            />
          )}
        </Card>
      )}
      <Modal
        open={Boolean(decision)}
        onOpenChange={(open) => !open && setDecision(null)}
        title={decision?.type === "approve" ? "요청 승인" : "요청 반려"}
        description={
          decision?.item.summary || "결정 전 요청 내용을 다시 확인하세요."
        }
      >
        {decision && (
          <form className="settings-form" onSubmit={submit}>
            <Field
              label={decision.type === "reject" ? "반려 사유" : "검토 의견"}
            >
              <textarea
                required={decision.type === "reject"}
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
                variant={decision.type === "approve" ? "primary" : "danger"}
                disabled={
                  saving ||
                  (decision.type === "reject" && !decision.note.trim())
                }
              >
                결정 확정
              </Button>
            </div>
          </form>
        )}
      </Modal>
    </div>
  );
}
