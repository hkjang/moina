import { MailCheck, Send } from "lucide-react";
import { useEffect, useState } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import { Badge, Button, Card, ErrorState, Field, LoadingState, SectionHeader, SwitchField } from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { AdminTitle } from "./components";

interface SMTPUpdateSettings {
  enabled: boolean;
  host: string;
  port: number;
  security: "starttls" | "tls" | "none";
  username: string;
  password?: string;
  clearPassword?: boolean;
  fromAddress: string;
  fromName: string;
  timeoutSeconds: number;
  allowPrivateNetwork: boolean;
}

interface SMTPSettingsView extends SMTPUpdateSettings {
  passwordConfigured?: boolean;
}

const defaults: SMTPUpdateSettings = {
  enabled: false,
  host: "",
  port: 587,
  security: "starttls",
  username: "",
  password: "",
  fromAddress: "",
  fromName: "MOINA",
  timeoutSeconds: 15,
  allowPrivateNetwork: false,
};

export function AdminSMTPPage() {
  const { notify } = useToast();
  const query = useApiQuery<SMTPSettingsView>("/admin/smtp");
  const [form, setForm] = useState<SMTPUpdateSettings>(defaults);
  const [passwordConfigured, setPasswordConfigured] = useState(false);
  const [working, setWorking] = useState<"save" | "test" | null>(null);

  useEffect(() => {
    if (!query.data) return;
    const { passwordConfigured: configured = false, ...editable } = query.data;
    setPasswordConfigured(configured);
    setForm({ ...defaults, ...editable, password: "", clearPassword: false });
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
          username: form.username,
          password: form.password,
          clearPassword: form.clearPassword,
          fromAddress: form.fromAddress,
          fromName: form.fromName,
          timeoutSeconds: form.timeoutSeconds,
          allowPrivateNetwork: form.allowPrivateNetwork,
        },
      });
      if (test) {
        const result = await apiRequest<{ recipient?: string }>("/admin/smtp/test", { method: "POST" });
        notify(`SMTP 설정을 저장하고 ${result.recipient || "관리자 이메일"}로 테스트 메일을 보냈습니다.`, "success");
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
            <Field label="연결 보안"><select value={form.security} onChange={(event) => changeSecurity(event.target.value as SMTPUpdateSettings["security"])}><option value="starttls">STARTTLS (권장)</option><option value="tls">TLS / SMTPS</option><option value="none">암호화 없음 (폐쇄망·무인증)</option></select></Field>
            <Field label="연결 제한 시간(초)"><input type="number" min="3" max="60" value={form.timeoutSeconds} onChange={(event) => setForm({ ...form, timeoutSeconds: Number(event.target.value) })}/></Field>
            <Field label="사용자 이름" help={form.security === "none" ? "암호화 없는 연결에서는 인증 정보를 보내지 않습니다." : "인증이 필요하지 않으면 비워둘 수 있습니다."}><input autoComplete="off" value={form.username} disabled={form.security === "none"} onChange={(event) => setForm({ ...form, username: event.target.value })}/></Field>
            <Field label="비밀번호" help={passwordConfigured ? "비워두면 저장된 비밀번호를 유지합니다." : "SMTP 인증에 필요한 경우 입력하세요."}><input type="password" autoComplete="new-password" value={form.password || ""} disabled={form.security === "none" || form.clearPassword} onChange={(event) => setForm({ ...form, password: event.target.value, clearPassword: false })}/></Field>
            <Field label="보내는 이메일"><input required type="email" value={form.fromAddress} onChange={(event) => setForm({ ...form, fromAddress: event.target.value })} placeholder="no-reply@example.com"/></Field>
            <Field label="보내는 이름"><input maxLength={80} value={form.fromName} onChange={(event) => setForm({ ...form, fromName: event.target.value })}/></Field>
          </div>
          {passwordConfigured && form.security !== "none" && <SwitchField label="저장된 SMTP 비밀번호 삭제" description="저장하면 기존 비밀번호 암호문을 제거합니다." checked={form.clearPassword === true} onChange={(clearPassword) => setForm({ ...form, clearPassword, password: clearPassword ? "" : form.password })}/>}
          <SwitchField label="사설망 SMTP 서버 허용" description={`정확히 '${form.host || "입력한 SMTP DNS 이름"}'만 RFC1918·ULA 주소로 연결할 수 있게 합니다. IP 직접 입력과 loopback은 허용하지 않습니다.`} checked={form.allowPrivateNetwork} onChange={(allowPrivateNetwork) => setForm({ ...form, allowPrivateNetwork })}/>
        </div>}
        <div className="form-actions">
          <Button variant="secondary" onClick={() => void save(false)} disabled={working !== null}><MailCheck/>{working === "save" ? "저장 중…" : "SMTP 설정 저장"}</Button>
          <Button variant="primary" onClick={() => void save(true)} disabled={!form.enabled || working !== null}><Send/>{working === "test" ? "테스트 전송 중…" : "저장 후 테스트 메일"}</Button>
        </div>
      </Card>
      <Card><SectionHeader title="알림 연동" description="멘션·Signal·Link·Echo와 승인 알림이 outbox를 통해 전달됩니다."/><p className="settings-note">게시와 메일 전송은 분리되어 있습니다. 메일 서버가 잠시 중단되어도 모인 작성은 완료되며, 실패한 메일 이벤트는 자동 재시도된 뒤 관리자 outbox 화면에서 확인할 수 있습니다.</p></Card>
    </>}
  </div>;
}
