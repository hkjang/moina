import {
  Activity,
  FileCheck2,
  Flag,
  RefreshCw,
  ShieldCheck,
  Users,
} from "lucide-react";
import {
  Badge,
  Button,
  Card,
  ErrorState,
  LoadingState,
  SectionHeader,
} from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { AdminTitle } from "./components";
import { statusTone } from "./helpers";

export function AdminOverviewPage() {
  const query = useApiQuery<Record<string, unknown>>("/admin/stats");
  const data = query.data || {};
  const metrics = [
    {
      label: "전체 사용자",
      value: Number(data.userCount || data.users || 0),
      icon: Users,
      tone: "brand",
    },
    {
      label: "오늘의 모인",
      value: Number(data.todayPostCount || data.todayPosts || 0),
      icon: Activity,
      tone: "positive",
    },
    {
      label: "처리 대기 신고",
      value: Number(data.pendingReportCount || data.pendingReports || 0),
      icon: Flag,
      tone: "warning",
    },
    {
      label: "승인 대기",
      value: Number(data.pendingApprovalCount || data.pendingApprovals || 0),
      icon: FileCheck2,
      tone: "neutral",
    },
  ];
  return (
    <div className="page-stack">
      <AdminTitle
        title="관리 대시보드"
        description="개인 활동과 분리된 서비스 운영·보안 현황을 확인합니다."
        actions={
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
      ) : (
        <>
          <div className="metric-grid">
            {metrics.map((metric) => (
              <Card className="metric-card" key={metric.label}>
                <span className={`metric-icon ${metric.tone}`}>
                  <metric.icon />
                </span>
                <span>
                  <small>{metric.label}</small>
                  <strong>{metric.value.toLocaleString("ko-KR")}</strong>
                </span>
              </Card>
            ))}
          </div>
          <div className="admin-dashboard-grid">
            <Card>
              <SectionHeader title="서비스 상태" />
              <div className="health-list">
                <p>
                  <span>API</span>
                  <Badge tone="positive">정상</Badge>
                </p>
                <p>
                  <span>PostgreSQL</span>
                  <Badge tone={statusTone(data.databaseStatus || "active")}>
                    {String(data.databaseStatus || "연결됨")}
                  </Badge>
                </p>
                <p>
                  <span>실시간 알림</span>
                  <Badge tone={statusTone(data.websocketStatus || "active")}>
                    {String(data.websocketStatus || "운영 중")}
                  </Badge>
                </p>
                <p>
                  <span>AI 공급자</span>
                  <Badge tone={data.aiEnabled ? "positive" : "neutral"}>
                    {data.aiEnabled ? "사용 중" : "꺼짐"}
                  </Badge>
                </p>
              </div>
            </Card>
            <Card>
              <SectionHeader title="운영 안내" />
              <div className="admin-callout">
                <ShieldCheck />
                <span>
                  <strong>관리 활동은 모두 감사 로그에 남습니다.</strong>
                  <p>
                    콘텐츠 삭제, 사용자 제재, 역할·설정 변경은 최소 권한으로
                    수행하세요.
                  </p>
                </span>
              </div>
            </Card>
          </div>
        </>
      )}
    </div>
  );
}
