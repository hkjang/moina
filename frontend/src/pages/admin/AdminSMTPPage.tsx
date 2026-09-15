import { MailCheck, RefreshCw, Send } from "lucide-react";
import { useEffect, useState } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import { Badge, Button, Card, ErrorState, Field, LoadingState, SectionHeader, SwitchField } from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { formatDate } from "../../utils/format";
import { AdminTitle, Table } from "./components";

// 메일로 보내는 이벤트는 사람이 실제로 기다리는 일만 고릅니다 — 오지 않으면 누군가
// 손해를 보거나 화면을 계속 새로고침하는 것. Signal·Link·Remoin은 알림 센터와 요약에만 남습니다.
export const mailEvents: { key: string; label: string; description: string }[] = [
  { key: "approval_requested", label: "승인 요청", description: "승인자가 움직여야 요청자의 Moin이 게시됩니다." },
  { key: "approval_decided", label: "승인 결과", description: "요청자가 기다리는 승인·반려 결과입니다." },
  { key: "mention", label: "멘션", description: "누군가 나를 직접 불러 답을 기다립니다." },
  { key: "echo", label: "내 Moin의 Echo", description: "내 Moin에 달린 답글 — 대화가 나를 기다립니다." },
  { key: "digest", label: "요약", description: "사용자가 켠 주기로 활동을 묶어 한 통으로 보냅니다." },
  { key: "security", label: "계정 보안", description: "계정 보안 알림은 사용자가 끌 수 없는 운영 기록입니다." },
];

type MailNotify = Record<string, boolean>;

interface SMTPUpdateSettings {
  enabled: boolean;
  host: string;
  port: number;
  security: "auto" | "starttls" | "tls" | "none";
  skipTlsVerify: boolean;
  username: string;
  password?: string;
  clearPassword?: boolean;
  fromAddress: string;
  fromName: string;
  timeoutSeconds: number;
  allowPrivateNetwork: boolean;
  notify: MailNotify;
}

interface SMTPSettingsView extends SMTPUpdateSettings {
  passwordConfigured?: boolean;
}

interface MailDelivery {
  id: string;
  event: string;
  recipient: string;
  subject: string;
  status: "queued" | "sent" | "failed";
  attempts: number;
  errorMessage?: string;
  createdAt: string;
  updatedAt: string;
}

interface MailDeliveryPage {
  items: MailDelivery[];
  summary: { total: number; status: Record<string, number> };
}

const defaultNotify = (): MailNotify => Object.fromEntries(mailEvents.map((event) => [event.key, true]));

// 사내 릴레이는 포트 25·인증 없음·TLS 없음이 흔하므로 그것이 기본값입니다.
const defaults: SMTPUpdateSettings = {
  enabled: false,
  host: "",
  port: 25,
  security: "auto",
  skipTlsVerify: false,
  username: "",
  password: "",
  fromAddress: "",
  fromName: "MOINA",
  timeoutSeconds: 10,
  allowPrivateNetwork: false,
  notify: defaultNotify(),
};

const eventLabel = (key: string) => mailEvents.find((event) => event.key === key)?.label ?? (key === "test" ? "시험 발송" : key);
const statusBadge = (status: MailDelivery["status"]) => status === "sent"
  ? <Badge tone="positive">보냄</Badge>
  : status === "failed" ? <Badge tone="danger">실패</Badge> : <Badge tone="neutral">대기</Badge>;

export function AdminSMTPPage() {
  const { notify } = useToast();
  const query = useApiQuery<SMTPSettingsView>("/admin/smtp");
  const deliveries = useApiQuery<MailDeliveryPage>("/admin/smtp/deliveries?limit=50");
  const [form, setForm] = useState<SMTPUpdateSettings>(defaults);
  const [passwordConfigured, setPasswordConfigured] = useState(false);
  const [working, setWorking] = useState<"save" | "test" | null>(null);

  useEffect(() => {
    if (!query.data) return;
    const { passwordConfigured: configured = false, ...editable } = query.data;
    setPasswordConfigured(configured);
    setForm({ ...defaults, ...editable, notify: { ...defaultNotify(), ...(editable.notify ?? {}) }, password: "", clearPassword: false });
  }, [query.data]);

  const save = async (test = false) => {
    setWorking(test ? "test" : "save");
    try {
      await apiRequest("/admin/smtp", {
        method: "PUT",
        body: {
          enabled: form.enabled,
          host: form.host,
          port: form.port,
          security: form.security,
          skipTlsVerify: form.skipTlsVerify,
          username: form.username,
          password: form.password,
          clearPassword: form.clearPassword,
          fromAddress: form.fromAddress,
          fromName: form.fromName,
          timeoutSeconds: form.timeoutSeconds,
          allowPrivateNetwork: form.allowPrivateNetwork,
          notify: form.notify,
        },
      });
      if (test) {
        try {
          const result = await apiRequest<{ recipient?: string }>("/admin/smtp/test", { method: "POST" });
          notify(`SMTP 설정을 저장하고 ${result.recipient || "관리자 이메일"}로 테스트 메일을 보냈습니다.`, "success");
        } finally {
          // 시험 발송은 성공이든 실패든 기록에 남으므로 결과를 그 자리에서 보여 줍니다.
          deliveries.reload();
        }
      } else {
        notify("SMTP 메일 설정을 저장했습니다.", "success");
      }
      query.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };

  const changeSecurity = (security: SMTPUpdateSettings["security"]) => {
    if (security === "none") {
      setForm({ ...form, security, username: "", password: "", clearPassword: passwordConfigured });
      return;
    }
    setForm({ ...form, security });
  };

  return <div className="page-stack">
    <AdminTitle title="SMTP 메일 설정" description="MOINA 알림을 사용자 프로필 이메일로 안전하게 전달합니다."/>
    {query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : <>
      <Card>
        <SectionHeader title="메일 전달 서버" description="비밀번호는 ENCRYPTION_KEY로 암호화되며 화면에 다시 노출되지 않습니다." action={<Badge tone={form.enabled ? "positive" : "neutral"}>{form.enabled ? "활성" : "비활성"}</Badge>}/>
        <SwitchField label="이메일 알림 사용" description="사용자가 개인 알림 설정에서 이메일 채널을 선택할 수 있게 합니다." checked={form.enabled} onChange={(enabled) => setForm({ ...form, enabled })}/>
        {form.enabled && <div className="settings-form">
          <div className="form-grid">
            <Field label="SMTP 서버" help="포트 없이 정확한 DNS 이름을 입력하세요."><input required value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} placeholder="smtp.example.com" spellCheck={false}/></Field>
            <Field label="포트"><input required type="number" min="1" max="65535" value={form.port} onChange={(event) => setForm({ ...form, port: Number(event.target.value) })}/></Field>
            <Field label="연결 보안"><select value={form.security} onChange={(event) => changeSecurity(event.target.value as SMTPUpdateSettings["security"])}><option value="auto">자동 (서버가 알리는 대로)</option><option value="starttls">STARTTLS</option><option value="tls">TLS / SMTPS</option><option value="none">암호화 없음 (폐쇄망·무인증)</option></select></Field>
            <Field label="연결 제한 시간(초)"><input type="number" min="3" max="60" value={form.timeoutSeconds} onChange={(event) => setForm({ ...form, timeoutSeconds: Number(event.target.value) })}/></Field>
            <Field label="사용자 이름" help={form.security === "none" ? "암호화 없는 연결에서는 인증 정보를 보내지 않습니다." : "사내 릴레이는 대개 인증이 없습니다. 필요할 때만 입력하세요."}><input autoComplete="off" value={form.username} disabled={form.security === "none"} onChange={(event) => setForm({ ...form, username: event.target.value })}/></Field>
            <Field label="비밀번호" help={passwordConfigured ? "비워두면 저장된 비밀번호를 유지합니다." : "SMTP 인증에 필요한 경우 입력하세요."}><input type="password" autoComplete="new-password" value={form.password || ""} disabled={form.security === "none" || form.clearPassword} onChange={(event) => setForm({ ...form, password: event.target.value, clearPassword: false })}/></Field>
            <Field label="보내는 이메일"><input required type="email" value={form.fromAddress} onChange={(event) => setForm({ ...form, fromAddress: event.target.value })} placeholder="no-reply@example.com"/></Field>
            <Field label="보내는 이름"><input maxLength={80} value={form.fromName} onChange={(event) => setForm({ ...form, fromName: event.target.value })}/></Field>
          </div>
          {passwordConfigured && form.security !== "none" && <SwitchField label="저장된 SMTP 비밀번호 삭제" description="저장하면 기존 비밀번호 암호문을 제거합니다." checked={form.clearPassword === true} onChange={(clearPassword) => setForm({ ...form, clearPassword, password: clearPassword ? "" : form.password })}/>}
          <SwitchField label="사설망 SMTP 서버 허용" description={`정확히 '${form.host || "입력한 SMTP DNS 이름"}'만 RFC1918·ULA 주소로 연결할 수 있게 합니다. IP 직접 입력과 loopback은 허용하지 않습니다.`} checked={form.allowPrivateNetwork} onChange={(allowPrivateNetwork) => setForm({ ...form, allowPrivateNetwork })}/>
          {form.security !== "none" && <SwitchField label="TLS 인증서 검증 건너뛰기" description="사내 인증서가 사설 CA일 때만 켭니다. 암호화는 그대로 두고 인증서 체인 검사만 생략합니다." checked={form.skipTlsVerify} onChange={(skipTlsVerify) => setForm({ ...form, skipTlsVerify })}/>}
          <SectionHeader title="메일로 보내는 이벤트" description="오지 않으면 누군가 기다리게 되는 일만 메일로 보냅니다. 끄면 그 종류만 멎고 알림 센터에는 그대로 남습니다."/>
          {mailEvents.map((event) => <SwitchField key={event.key} label={event.label} description={event.description} checked={form.notify[event.key] !== false} onChange={(checked) => setForm({ ...form, notify: { ...form.notify, [event.key]: checked } })}/>)}
        </div>}
        <div className="form-actions">
          <Button variant="secondary" onClick={() => void save(false)} disabled={working !== null}><MailCheck/>{working === "save" ? "저장 중…" : "SMTP 설정 저장"}</Button>
          <Button variant="primary" onClick={() => void save(true)} disabled={!form.enabled || working !== null}><Send/>{working === "test" ? "테스트 전송 중…" : "저장 후 테스트 메일"}</Button>
        </div>
      </Card>
      <Card><SectionHeader title="알림 연동" description="멘션·Signal·Link·Echo와 승인 알림이 outbox를 통해 전달됩니다."/><p className="settings-note">게시와 메일 전송은 분리되어 있습니다. 메일 서버가 잠시 중단되어도 모인 작성은 완료되며, 실패한 메일 이벤트는 자동 재시도된 뒤 관리자 outbox 화면에서 확인할 수 있습니다.</p></Card>
      {(form.enabled || (deliveries.data?.items?.length ?? 0) > 0) && <MailDeliveryLog page={deliveries.data} loading={deliveries.loading} error={deliveries.error} onReload={deliveries.reload}/>}
    </>}
  </div>;
}

// MailDeliveryLog는 건물 밖으로 나간 메일을 보여 줍니다 — 언제, 어떤 이벤트로, 누구에게,
// 어떤 제목이, 되었는지. "안 왔다"는 문의에 답하려면 성공도 실패도 모두 있어야 합니다. 본문은 담지 않습니다.
function MailDeliveryLog({ page, loading, error, onReload }: { page: MailDeliveryPage | undefined; loading: boolean; error: string | null; onReload: () => void }) {
  const items = page?.items ?? [];
  const summary = page?.summary?.status ?? {};
  return <Card>
    <SectionHeader
      title="발송 기록"
      description={page ? `보냄 ${summary.sent ?? 0} · 실패 ${summary.failed ?? 0} · 대기 ${summary.queued ?? 0} — 최근 ${items.length}건을 보여 줍니다. 본문은 기록하지 않습니다.` : "최근 발송 시도와 결과입니다."}
      action={<Button variant="ghost" size="small" onClick={onReload} disabled={loading}><RefreshCw/>새로 고침</Button>}
    />
    {loading && !page ? <LoadingState/> : error ? <ErrorState message={error} onRetry={onReload}/> : items.length === 0
      ? <p className="settings-note">아직 보낸 메일이 없습니다. 저장 후 테스트 메일을 보내면 첫 기록이 남습니다.</p>
      : <Table caption="메일 발송 기록" headers={["시각", "이벤트", "받는 사람", "제목", "결과"]}>
        {items.map((item) => <tr key={item.id}>
          <td title={item.updatedAt}>{formatDate(item.createdAt)}</td>
          <td>{eventLabel(item.event)}</td>
          <td className="table-content">{item.recipient}</td>
          <td className="table-content" title={item.subject}>{item.subject}</td>
          <td>{statusBadge(item.status)}{item.attempts > 1 && <span className="settings-note"> {item.attempts}회 시도</span>}{item.status === "failed" && item.errorMessage && <div className="settings-note" title={item.errorMessage}>{item.errorMessage}</div>}</td>
        </tr>)}
      </Table>}
  </Card>;
}
