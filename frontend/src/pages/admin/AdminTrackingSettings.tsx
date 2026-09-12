import { Eraser, Save, ShieldPlus } from "lucide-react";
import { useState } from "react";
import { apiRequest, readableError } from "../../api/client";
import { useToast } from "../../components/ToastProvider";
import { Badge, Button, Card, ErrorState, Field, LoadingState, SectionHeader, SwitchField } from "../../components/ui";
import { useApiQuery } from "../../hooks/useApiQuery";
import { MAX_SNIPPET_BYTES, parseAllowedHosts, snippetBytes } from "../../utils/tracking";

export type TrackingProvider = "none" | "momento" | "ga4" | "gtm" | "matomo" | "custom";

export interface TrackingSettings {
  enabled: boolean;
  provider: TrackingProvider;
  momentoUrl: string;
  momentoSiteId: string;
  momentoProxy: boolean;
  momentoAllowPrivateNetwork: boolean;
  measurementId: string;
  matomoUrl: string;
  matomoSiteId: string;
  customSnippet: string;
  allowedHosts: string[];
  includeAdmin: boolean;
  placement: "head" | "body";
}

interface TrackingSettingsView extends TrackingSettings {
  providers?: string[];
  maxSnippetBytes?: number;
  snippetPreview?: string;
}

export interface TrackingViolation {
  origin: string;
  directive: string;
  page: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
  allowed: boolean;
}

const trackingDefaults: TrackingSettings = {
  enabled: false,
  provider: "none",
  momentoUrl: "",
  momentoSiteId: "",
  momentoProxy: true,
  momentoAllowPrivateNetwork: false,
  measurementId: "",
  matomoUrl: "",
  matomoSiteId: "",
  customSnippet: "",
  allowedHosts: [],
  includeAdmin: false,
  placement: "head",
};

// Momento comes first: it is the self-hosted collector and the only choice
// whose data never leaves the network.
const providerOptions: Array<{ value: TrackingProvider; label: string }> = [
  { value: "momento", label: "Momento (사내 수집기)" },
  { value: "ga4", label: "Google Analytics 4" },
  { value: "gtm", label: "Google Tag Manager" },
  { value: "matomo", label: "Matomo" },
  { value: "custom", label: "직접 붙여 넣기" },
];


export function AdminTrackingSettings() {
  const { notify } = useToast();
  const query = useApiQuery<TrackingSettingsView>("/admin/analytics");
  const violations = useApiQuery<{ items?: TrackingViolation[] }>("/admin/analytics/violations");
  const [form, setForm] = useState<TrackingSettings>(trackingDefaults);
  const [hostsText, setHostsText] = useState("");
  const [preview, setPreview] = useState("");
  const [working, setWorking] = useState<string | null>(null);
  // The form is re-seeded from each fresh server response during render, the
  // React pattern for state derived from a prop, so an administrator's edits
  // survive re-renders but not a reload after saving.
  const [seededFrom, setSeededFrom] = useState<TrackingSettingsView | undefined>(undefined);
  if (query.data && query.data !== seededFrom) {
    const { providers: _providers, maxSnippetBytes: _max, snippetPreview = "", ...editable } = query.data;
    setSeededFrom(query.data);
    setForm({ ...trackingDefaults, ...editable, allowedHosts: editable.allowedHosts || [] });
    setHostsText((editable.allowedHosts || []).join("\n"));
    setPreview(snippetPreview);
  }

  const body = () => ({ ...form, allowedHosts: parseAllowedHosts(hostsText) });
  const reloadAll = () => { query.reload(); violations.reload(); };

  const save = async () => {
    setWorking("save");
    try {
      await apiRequest("/admin/analytics", { method: "PUT", body: body() });
      notify(form.enabled ? "방문 추적을 저장했습니다. 화면을 새로 고치면 스니펫이 붙습니다." : "방문 추적을 저장했습니다. 정책은 원래대로 좁아집니다.", "success");
      reloadAll();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };

  const allow = async (origin: string) => {
    setWorking(`allow:${origin}`);
    try {
      await apiRequest("/admin/analytics/violations/allow", { method: "POST", body: { origin } });
      notify(`${origin}을(를) 허용 출처에 추가했습니다.`, "success");
      reloadAll();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };

  const clear = async () => {
    setWorking("clear");
    try {
      await apiRequest("/admin/analytics/violations", { method: "DELETE" });
      notify("차단 기록을 비웠습니다. 화면을 다시 열어 남는 차단이 있는지 확인하세요.", "success");
      violations.reload();
    } catch (error) {
      notify(readableError(error), "error");
    } finally {
      setWorking(null);
    }
  };

  const bytes = snippetBytes(form.customSnippet);
  const items = violations.data?.items || [];
  const blocked = items.filter((item) => !item.allowed);

  if (query.loading) return <Card><SectionHeader title="방문 추적"/><LoadingState/></Card>;
  if (query.error) return <Card><SectionHeader title="방문 추적"/><ErrorState message={query.error} onRetry={query.reload}/></Card>;

  return <Card>
    <SectionHeader
      title="방문 추적"
      description="관리자가 고른 추적 스니펫을 화면 문서에 붙입니다. 기본값은 꺼짐이며, 켜도 script-src를 'unsafe-inline'으로 풀지 않고 요청마다 nonce를 씁니다."
      action={<Badge tone={form.enabled ? "positive" : "neutral"}>{form.enabled ? "활성" : "비활성"}</Badge>}
    />
    <SwitchField
      label="방문 추적 사용"
      description="켜면 사용자 화면 문서마다 선택한 provider의 스니펫이 들어가고, 그 스니펫이 가리키는 출처만 Content-Security-Policy에 더해집니다."
      checked={form.enabled}
      onChange={(enabled) => setForm({ ...form, enabled, provider: enabled && form.provider === "none" ? "momento" : form.provider })}
    />
    {form.enabled && <div className="nested-settings settings-form">
      <div className="form-grid">
        <Field label="Provider" help="Momento는 사내 자체 호스팅 수집기라 데이터가 밖으로 나가지 않습니다.">
          <select value={form.provider} onChange={(event) => setForm({ ...form, provider: event.target.value as TrackingProvider })}>
            {providerOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
          </select>
        </Field>
        <Field label="삽입 위치" help="대부분의 추적 도구는 head를 권합니다.">
          <select value={form.placement} onChange={(event) => setForm({ ...form, placement: event.target.value as "head" | "body" })}>
            <option value="head">head 끝</option>
            <option value="body">body 끝</option>
          </select>
        </Field>
      </div>
      {form.provider === "momento" && <div className="form-grid">
        <Field label="Momento 수집기 주소" help="query·fragment 없는 HTTP(S) URL. 예: https://momento.corp.example">
          <input type="url" required value={form.momentoUrl} spellCheck={false} placeholder="https://momento.corp.example" onChange={(event) => setForm({ ...form, momentoUrl: event.target.value })}/>
        </Field>
        <Field label="Momento 사이트 ID">
          <input required value={form.momentoSiteId} spellCheck={false} onChange={(event) => setForm({ ...form, momentoSiteId: event.target.value })}/>
        </Field>
      </div>}
      {form.provider === "momento" && <>
        <SwitchField
          label="같은 오리진 프록시 사용 (권장)"
          description="서버가 /momento/* 를 수집기로 넘기고 스니펫에 data-endpoint=&quot;/momento&quot;를 줍니다. 정책에 외부 출처가 등장하지 않아 CSP를 건드리지 않습니다."
          checked={form.momentoProxy}
          onChange={(momentoProxy) => setForm({ ...form, momentoProxy })}
        />
        {form.momentoProxy && <SwitchField
          label="사설망 수집기 허용"
          description="수집기 DNS 이름이 RFC1918·ULA 주소로 해석될 때만 켭니다. IP 직접 입력과 loopback은 허용하지 않습니다."
          checked={form.momentoAllowPrivateNetwork}
          onChange={(momentoAllowPrivateNetwork) => setForm({ ...form, momentoAllowPrivateNetwork })}
        />}
      </>}
      {(form.provider === "ga4" || form.provider === "gtm") && <div className="form-grid">
        <Field label={form.provider === "ga4" ? "측정 ID" : "컨테이너 ID"} help={form.provider === "ga4" ? "예: G-XXXXXXX" : "예: GTM-XXXXXXX"}>
          <input required value={form.measurementId} spellCheck={false} onChange={(event) => setForm({ ...form, measurementId: event.target.value })}/>
        </Field>
      </div>}
      {form.provider === "matomo" && <div className="form-grid">
        <Field label="Matomo 주소" help="예: https://matomo.corp.example">
          <input type="url" required value={form.matomoUrl} spellCheck={false} onChange={(event) => setForm({ ...form, matomoUrl: event.target.value })}/>
        </Field>
        <Field label="Matomo 사이트 ID">
          <input required value={form.matomoSiteId} spellCheck={false} onChange={(event) => setForm({ ...form, matomoSiteId: event.target.value })}/>
        </Field>
      </div>}
      {form.provider === "custom" && <Field
        label="추적 코드"
        help={`추적 도구가 준 <script> 스니펫을 그대로 붙여 넣습니다. 모든 <script>에 요청별 nonce가 붙고, 본문에 적힌 http(s) 출처가 정책에 더해집니다. ${bytes.toLocaleString()} / ${MAX_SNIPPET_BYTES.toLocaleString()} bytes`}
        error={bytes > MAX_SNIPPET_BYTES ? "추적 코드는 8KB를 넘을 수 없습니다." : undefined}
      >
        <textarea rows={8} spellCheck={false} value={form.customSnippet} onChange={(event) => setForm({ ...form, customSnippet: event.target.value })}/>
      </Field>}
      <Field label="추가 허용 출처" help="스니펫에서 자동으로 읽지 못한 출처를 한 줄에 하나씩 https://host[:port] 형태로 적습니다. 아래 차단 기록에서 허용을 누르면 여기에 추가됩니다.">
        <textarea rows={3} spellCheck={false} value={hostsText} placeholder={"https://cdn.example\nhttps://collect.example:8443"} onChange={(event) => setHostsText(event.target.value)}/>
      </Field>
      <SwitchField
        label="관리 화면에도 붙이기"
        description="기본은 아니오입니다. /admin 으로 시작하는 화면을 새로 열 때는 스니펫을 넣지 않습니다."
        checked={form.includeAdmin}
        onChange={(includeAdmin) => setForm({ ...form, includeAdmin })}
      />
      {preview && <details className="advanced-settings">
        <summary>페이지에 실리는 스니펫 미리보기</summary>
        <pre className="tracking-preview"><code>{preview}</code></pre>
      </details>}
    </div>}
    <div className="form-actions">
      <Button variant="primary" onClick={() => void save()} disabled={working !== null || bytes > MAX_SNIPPET_BYTES}><Save/>{working === "save" ? "저장 중…" : "방문 추적 저장"}</Button>
    </div>
    {form.enabled && <div className="nested-settings">
      <SectionHeader
        title="정책이 차단한 출처"
        description={blocked.length ? "브라우저가 신고한 출처입니다. 허용을 누르면 추가 허용 출처에 들어가고 저장까지 끝납니다." : "차단 신고가 없습니다. 스니펫이 실린 화면을 새로 열어도 비어 있으면 정책이 스니펫을 막지 않는 것입니다."}
        action={<Button variant="ghost" size="small" onClick={() => void clear()} disabled={working !== null || items.length === 0}><Eraser/>{working === "clear" ? "비우는 중…" : "기록 비우기"}</Button>}
      />
      {items.length > 0 && <div className="table-scroll"><table className="data-table">
        <thead><tr><th>출처</th><th>지시어</th><th>횟수</th><th>마지막 화면</th><th>상태</th></tr></thead>
        <tbody>
          {items.map((item) => <tr key={`${item.directive} ${item.origin}`}>
            <td><code>{item.origin}</code></td>
            <td>{item.directive}</td>
            <td>{item.count}</td>
            <td className="table-content" title={item.page}>{item.page || "—"}</td>
            <td>{item.allowed
              ? <Badge tone="positive">허용됨</Badge>
              : <Button size="small" variant="secondary" onClick={() => void allow(item.origin)} disabled={working !== null}><ShieldPlus/>{working === `allow:${item.origin}` ? "추가 중…" : "허용"}</Button>}
            </td>
          </tr>)}
        </tbody>
      </table></div>}
    </div>}
  </Card>;
}
