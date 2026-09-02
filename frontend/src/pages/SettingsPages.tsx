import { Bell, KeyRound, Mail, Monitor, RefreshCw, Save, ShieldCheck, UserRoundCog } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { NavLink } from 'react-router-dom';
import { apiRequest, readableError } from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { ProfileAvatarEditor } from '../components/ProfileAvatarEditor';
import { useToast } from '../components/ToastProvider';
import { Badge, Button, Card, EmptyState, ErrorState, Field, LoadingState, PageHeader, SectionHeader, SwitchField } from '../components/ui';
import { Modal } from '../components/Modal';
import { useApiQuery } from '../hooks/useApiQuery';
import { invalidateApiQueries } from '../hooks/apiQueryClient';
import { hasPermission, personalNavigation } from '../navigation';
import type { PersonalKey, UserPreferences } from '../types';
import { formatDate, listFrom } from '../utils/format';
import { applyAppearance, DEFAULT_PREFERENCES, mergePreferences } from '../utils/preferences';

const defaultPreferences = DEFAULT_PREFERENCES;

function SettingsLayout({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return <div className="settings-page"><PageHeader eyebrow="PERSONAL" title={title} description={description}/><div className="settings-grid"><nav className="settings-nav custom-scrollbar" aria-label="개인 설정">{personalNavigation.map((item) => <NavLink key={item.path} to={item.path} className={({ isActive }) => isActive ? 'active' : ''}><item.icon/><span><strong>{item.label}</strong><small>{item.description}</small></span></NavLink>)}</nav><div className="settings-content">{children}</div></div></div>;
}

export function ProfileSettingsPage() {
  const { user, refresh } = useAuth();
  const { notify } = useToast();
  const query = useApiQuery<Record<string, unknown>>('/profile');
  const initializedProfile = useRef('');
  const [form, setForm] = useState({ displayName: '', bio: '', email: '', avatarId: '' });
  const [savedAvatar, setSavedAvatar] = useState({ id: '', url: '' });
  const [avatarBusy, setAvatarBusy] = useState(false);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    const data = query.data;
    if (!data) return;
    const profileID = String(data.id || user?.id || user?.username || 'profile');
    if (initializedProfile.current === profileID) return;
    initializedProfile.current = profileID;
    const avatar = {
      id: String(data.avatarId || ''),
      url: String(data.avatarUrl || user?.avatarUrl || ''),
    };
    setSavedAvatar(avatar);
    setForm({
      displayName: String(data.displayName || data.name || user?.displayName || ''),
      bio: String(data.bio || ''),
      email: String(data.email || user?.email || ''),
      avatarId: avatar.id,
    });
  }, [query.data, user]);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (avatarBusy) {
      notify('프로필 이미지 업로드가 끝난 뒤 저장해 주세요.', 'info');
      return;
    }
    setSaving(true);
    try {
      const updated = await apiRequest<Record<string, unknown>>('/profile', { method: 'PATCH', body: form });
      const nextAvatar = {
        id: String(updated.avatarId || ''),
        url: String(updated.avatarUrl || ''),
      };
      const previousAvatarID = savedAvatar.id;
      setSavedAvatar(nextAvatar);
      setForm((current) => ({ ...current, avatarId: nextAvatar.id }));
      await refresh();
      invalidateApiQueries(['/feed', '/posts', '/users', '/profiles', '/search', '/notifications']);
      if (previousAvatarID && previousAvatarID !== nextAvatar.id) {
        void apiRequest(`/media/${encodeURIComponent(previousAvatarID)}`, { method: 'DELETE' }).catch(() => undefined);
      }
      notify('프로필을 저장했습니다.', 'success');
    } catch (error) {
      notify(readableError(error), 'error');
    } finally {
      setSaving(false);
    }
  };
  return <SettingsLayout title="프로필 설정" description="다른 사람에게 보이는 프로필 이미지, 이름과 소개를 관리합니다.">{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : <Card><SectionHeader title="기본 프로필" description={`고유 사용자 이름 @${user?.username}`}/><form className="settings-form" onSubmit={save}><ProfileAvatarEditor name={form.displayName || user?.displayName || '사용자'} initialAvatarId={savedAvatar.id} initialAvatarUrl={savedAvatar.url} value={form.avatarId} disabled={saving} onBusyChange={setAvatarBusy} onChange={(avatarId) => setForm((current) => ({ ...current, avatarId }))}/><Field label="표시 이름"><input required maxLength={80} value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })}/></Field><Field label="이메일"><input type="email" value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })}/></Field><Field label="소개" help="관심사와 전문성을 500자 이내로 알려주세요."><textarea rows={5} maxLength={500} value={form.bio} onChange={(event) => setForm({ ...form, bio: event.target.value })}/></Field><div className="form-actions"><Button type="submit" variant="primary" disabled={saving || avatarBusy}><Save/>{saving ? '저장 중…' : avatarBusy ? '이미지 업로드 중…' : '프로필 저장'}</Button></div></form></Card>}</SettingsLayout>;
}

function usePreferences() {
  const query = useApiQuery<UserPreferences>('/profile/preferences');
  const [value, setValue] = useState(() => mergePreferences());
  useEffect(() => { if (query.data) { const merged = mergePreferences(query.data); setValue(merged); applyAppearance(merged); } }, [query.data]);
  return { query, value, setValue };
}

export function FeedSettingsPage() {
  const { notify } = useToast(); const { query, value, setValue } = usePreferences(); const [saving, setSaving] = useState(false);
  const feed = { ...defaultPreferences.feed, ...value.feed };
  const update = (key: keyof typeof feed, next: number | boolean | string[]) => setValue({ ...value, feed: { ...feed, [key]: next } });
  const save = async () => { setSaving(true); try { await apiRequest('/profile/preferences', { method: 'PUT', body: value }); notify('For Me 설정을 저장했습니다.', 'success'); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setSaving(false); } };
  return <SettingsLayout title="피드 개인화" description="내가 소유하는 For Me 알고리즘의 비율과 추천 이유 표시를 정합니다.">{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : <Card><SectionHeader title="For Me 구성" description="각 신호는 추천 순위에 적용되며 합계가 100일 필요는 없습니다." action={<Button variant="primary" onClick={() => void save()} disabled={saving}><Save/>{saving ? '저장 중…' : '저장'}</Button>}/><div className="range-stack">{([['topicWeight', '관심 토픽'], ['linkWeight', '내가 Link한 사람'], ['discoveryWeight', '새로운 발견'], ['recencyWeight', '최신성']] as const).map(([key, label]) => <label key={key}><span><strong>{label}</strong><output>{Number(feed[key])}%</output></span><input type="range" min="0" max="100" step="5" value={Number(feed[key])} onChange={(event) => update(key, Number(event.target.value))}/></label>)}</div><SwitchField label="추천 이유 표시" description="각 모인에 Why this Moin? 점수 설명을 표시합니다." checked={feed.showReasons !== false} onChange={(checked) => update('showReasons', checked)}/><Field label="제외 토픽" help="쉼표로 구분하세요. 제외한 토픽은 For Me에 추천하지 않습니다."><input value={(feed.excludedTopics || []).join(', ')} onChange={(event) => update('excludedTopics', event.target.value.split(',').map((item) => item.trim()).filter(Boolean))} placeholder="예: 광고, 스포일러"/></Field></Card>}</SettingsLayout>;
}

type DesktopPermission = NotificationPermission | 'unsupported';

function currentDesktopPermission(): DesktopPermission {
  return typeof window === 'undefined' || !('Notification' in window) ? 'unsupported' : window.Notification.permission;
}

export function NotificationSettingsPage() {
  const { notify } = useToast();
  const { query, value, setValue } = usePreferences();
  const [saving, setSaving] = useState(false);
  const [desktopPermission, setDesktopPermission] = useState<DesktopPermission>(currentDesktopPermission);
  const emailStatus = useApiQuery<{ available?: boolean; smtpConfigured?: boolean; recipientConfigured?: boolean }>('/notifications/email/status');
  const notifications = value.notifications;
  const updateInApp = (key: keyof typeof notifications.inApp, enabled: boolean) => setValue({
    ...value,
    notifications: { ...notifications, inApp: { ...notifications.inApp, [key]: key === 'approvals' ? true : enabled } },
  });
  const updateChannel = (channel: 'toast' | 'desktop' | 'email', enabled: boolean) => setValue({
    ...value,
    notifications: { ...notifications, [channel]: { enabled } },
  });
  const toggleDesktop = async (enabled: boolean) => {
    if (!enabled) {
      updateChannel('desktop', false);
      return;
    }
    if (typeof window === 'undefined' || !('Notification' in window)) {
      setDesktopPermission('unsupported');
      notify('이 브라우저는 데스크톱 알림을 지원하지 않습니다.', 'error');
      return;
    }
    try {
      const permission = await window.Notification.requestPermission();
      setDesktopPermission(permission);
      if (permission === 'granted') {
        updateChannel('desktop', true);
      } else {
        updateChannel('desktop', false);
        notify(permission === 'denied' ? '브라우저에서 알림 권한이 차단되었습니다.' : '알림 권한을 허용해야 데스크톱 알림을 켤 수 있습니다.', 'error');
      }
    } catch {
      updateChannel('desktop', false);
      notify('브라우저 알림 권한을 요청할 수 없습니다.', 'error');
    }
  };
  const save = async () => {
    setSaving(true);
    try {
      await apiRequest('/profile/preferences', { method: 'PUT', body: { notifications } });
      notify('알림 개인화 설정을 저장했습니다.', 'success');
      query.reload();
    } catch (error) {
      notify(readableError(error), 'error');
    } finally {
      setSaving(false);
    }
  };
  const permissionCopy = desktopPermission === 'granted'
    ? { label: '허용됨', tone: 'positive' as const, description: '이 브라우저에서 MOINA 데스크톱 알림을 표시할 수 있습니다.' }
    : desktopPermission === 'denied'
      ? { label: '차단됨', tone: 'danger' as const, description: '브라우저 사이트 설정에서 알림 권한을 다시 허용해야 합니다.' }
      : desktopPermission === 'unsupported'
        ? { label: '지원 안 함', tone: 'warning' as const, description: '현재 브라우저에서는 앱 내 알림과 토스트를 이용해 주세요.' }
        : { label: '확인 전', tone: 'neutral' as const, description: '데스크톱 알림을 켤 때 브라우저 권한을 요청합니다.' };
  const smtpConfigured = emailStatus.data?.smtpConfigured ?? emailStatus.data?.available === true;
  const recipientConfigured = emailStatus.data?.recipientConfigured ?? emailStatus.data?.available === true;
  const emailStatusCopy = !smtpConfigured
    ? 'SMTP 설정이 아직 준비되지 않았습니다.'
    : !recipientConfigured
      ? '프로필 설정에 메일을 받을 이메일 주소를 먼저 저장해 주세요.'
      : '관리자 SMTP 설정이 준비되어 있습니다.';

  return <SettingsLayout title="알림 개인화" description="알림 종류와 표시 방식, 요약 주기와 방해 금지 시간을 관리합니다.">{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : <>
    <Card><SectionHeader title="알림 받을 활동" description="알림 센터와 이메일에 공통으로 적용할 활동을 선택합니다." action={<Button variant="primary" onClick={() => void save()} disabled={saving}><Save/>{saving ? '저장 중…' : '저장'}</Button>}/>
      <SwitchField label="멘션" description="다른 사용자가 나를 언급하면 알립니다." checked={notifications.inApp.mentions} onChange={(checked) => updateInApp('mentions', checked)}/>
      <SwitchField label="Signal" description="내 모인에 새로운 Signal이 생기면 알립니다." checked={notifications.inApp.signals} onChange={(checked) => updateInApp('signals', checked)}/>
      <SwitchField label="새로운 Link" description="다른 사용자가 나와 Link하면 알립니다." checked={notifications.inApp.follows} onChange={(checked) => updateInApp('follows', checked)}/>
      <SwitchField label="Echo·인용·Remoin" description="내 모인에서 대화가 이어지면 알립니다." checked={notifications.inApp.echoes} onChange={(checked) => updateInApp('echoes', checked)}/>
      <SwitchField label="승인·보안 알림" description="필수 운영 기록이므로 끌 수 없으며 항상 알림 센터에 보관됩니다." checked disabled onChange={() => updateInApp('approvals', true)}/>
    </Card>
    <Card><SectionHeader title="실시간 표시 방식" description="서버가 허용한 새 알림을 현재 브라우저에 표시하는 방법입니다."/>
      <SwitchField label="앱 내 토스트" description="MOINA를 사용하는 동안 화면에 짧은 알림을 표시합니다." checked={notifications.toast.enabled} onChange={(checked) => updateChannel('toast', checked)}/>
      <SwitchField label="데스크톱 알림" description="브라우저가 백그라운드에 있어도 운영체제 알림을 표시합니다." checked={notifications.desktop.enabled} onChange={(checked) => void toggleDesktop(checked)}/>
      <div className="account-auth"><Bell/><span><strong>브라우저 알림 권한</strong><small>{permissionCopy.description}</small></span><Badge tone={permissionCopy.tone}>{permissionCopy.label}</Badge></div>
      <SwitchField label="이메일 알림" description={emailStatus.data?.available ? '프로필 이메일로 알림을 보냅니다. 요약을 켜면 반복 활동은 묶어서 보냅니다.' : '관리자가 SMTP 메일 설정을 활성화하면 사용할 수 있습니다.'} checked={notifications.email.enabled} disabled={!emailStatus.data?.available && !notifications.email.enabled} onChange={(checked) => updateChannel('email', checked)}/>
      <div className="account-auth"><Mail/><span><strong>메일 전달 서버</strong><small>{emailStatusCopy}</small></span><Badge tone={emailStatus.data?.available ? 'positive' : 'neutral'}>{emailStatus.data?.available ? '사용 가능' : '대기'}</Badge></div>
    </Card>
    <Card><SectionHeader title="요약과 조용한 시간" description="반복 알림은 모아서 보고, 집중 시간에는 실시간 표시를 잠시 멈춥니다."/>
      <div className="form-grid"><Field label="알림 요약"><select value={notifications.digest.mode} onChange={(event) => setValue({ ...value, notifications: { ...notifications, digest: { ...notifications.digest, mode: event.target.value as typeof notifications.digest.mode } } })}><option value="off">사용 안 함</option><option value="hourly">매시간</option><option value="daily">매일</option></select></Field><Field label="요약 시각" help="일별 요약의 기준 시각입니다."><input type="time" value={notifications.digest.time} disabled={notifications.digest.mode === 'off'} onChange={(event) => setValue({ ...value, notifications: { ...notifications, digest: { ...notifications.digest, time: event.target.value } } })}/></Field></div>
      <SwitchField label="조용한 시간" description="이 시간에는 토스트와 데스크톱 알림을 표시하지 않습니다." checked={notifications.quietHours.enabled} onChange={(enabled) => setValue({ ...value, notifications: { ...notifications, quietHours: { ...notifications.quietHours, enabled } } })}/>
      <div className="form-grid"><Field label="시작 시각"><input type="time" value={notifications.quietHours.start} disabled={!notifications.quietHours.enabled} onChange={(event) => setValue({ ...value, notifications: { ...notifications, quietHours: { ...notifications.quietHours, start: event.target.value } } })}/></Field><Field label="종료 시각"><input type="time" value={notifications.quietHours.end} disabled={!notifications.quietHours.enabled} onChange={(event) => setValue({ ...value, notifications: { ...notifications, quietHours: { ...notifications.quietHours, end: event.target.value } } })}/></Field></div>
    </Card>
  </>}</SettingsLayout>;
}

export function AccessibilitySettingsPage() {
  const { notify } = useToast(); const { query, value, setValue } = usePreferences(); const [saving, setSaving] = useState(false);
  const appearance = { ...defaultPreferences.appearance, ...value.appearance };
  const update = (next: typeof appearance) => { const changed = { ...value, appearance: next }; setValue(changed); applyAppearance(changed); };
  const save = async () => { setSaving(true); try { await apiRequest('/profile/preferences', { method: 'PUT', body: value }); notify('화면 및 접근성 설정을 저장했습니다.', 'success'); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setSaving(false); } };
  return <SettingsLayout title="화면 및 접근성" description="읽기 편한 글자 크기, 화면 테마와 움직임을 내 계정에 저장합니다.">{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : <Card><SectionHeader title="보기 환경" action={<Button variant="primary" onClick={() => void save()} disabled={saving}><Save/>{saving ? '저장 중…' : '저장'}</Button>}/><div className="form-grid"><Field label="글자 크기" help="본문 기본 크기는 16px이며 확대 시 여백도 함께 조정됩니다."><select value={appearance.fontScale} onChange={(event) => update({ ...appearance, fontScale: Number(event.target.value) as 100 | 112 | 125 })}><option value="100">기본 100%</option><option value="112">크게 112%</option><option value="125">매우 크게 125%</option></select></Field><Field label="화면 테마"><select value={appearance.theme} onChange={(event) => update({ ...appearance, theme: event.target.value as 'light' | 'dark' | 'system' })}><option value="system">기기 설정</option><option value="light">밝게</option><option value="dark">어둡게</option></select></Field><Field label="정보 밀도"><select value={appearance.density} onChange={(event) => update({ ...appearance, density: event.target.value as 'comfortable' | 'compact' })}><option value="comfortable">여유롭게</option><option value="compact">간결하게</option></select></Field></div><SwitchField label="화면 움직임 줄이기" description="전환, 커서와 반복 애니메이션을 최소화합니다." checked={appearance.reduceMotion === true} onChange={(checked) => update({ ...appearance, reduceMotion: checked })}/><div className="accessibility-preview"><Monitor/><span><strong>읽기 미리보기</strong><p>생각은 또렷하게, 인터페이스는 편안하게 보여야 합니다.</p></span></div></Card>}</SettingsLayout>;
}

export function SecuritySettingsPage() {
  const { user, logout } = useAuth(); const { notify } = useToast();
  const [form, setForm] = useState({ currentPassword: '', newPassword: '', confirm: '' }); const [saving, setSaving] = useState(false);
  const local = !user?.provider || user.provider === 'local';
  const submit = async (event: FormEvent) => { event.preventDefault(); if (form.newPassword !== form.confirm) return notify('새 비밀번호 확인 값이 일치하지 않습니다.', 'error'); if ([...form.newPassword].length < 12) return notify('새 비밀번호는 12자 이상이어야 합니다.', 'error'); setSaving(true); try { await apiRequest('/profile/password', { method: 'POST', body: { currentPassword: form.currentPassword, newPassword: form.newPassword } }); await logout(); window.location.assign('/login?passwordChanged=1'); } catch (error) { notify(readableError(error), 'error'); setSaving(false); } };
  return <SettingsLayout title="로그인 보안" description="인증 방식과 비밀번호를 확인하고 내 계정을 안전하게 보호합니다."><Card><SectionHeader title="인증 방식"/><div className="account-auth"><ShieldCheck/><span><strong>{local ? 'MOINA 로컬 계정' : 'Keycloak OIDC'}</strong><small>{local ? '비밀번호는 복원할 수 없는 단방향 해시로 저장됩니다.' : '비밀번호와 다중 인증은 조직의 Keycloak에서 관리합니다.'}</small></span><Badge tone="positive">보호됨</Badge></div></Card>{local && <Card><SectionHeader title="비밀번호 변경" description="변경하면 기존 로그인 세션이 모두 종료됩니다."/><form className="settings-form" onSubmit={submit}><Field label="현재 비밀번호"><input type="password" required autoComplete="current-password" value={form.currentPassword} onChange={(event) => setForm({ ...form, currentPassword: event.target.value })}/></Field><div className="form-grid"><Field label="새 비밀번호" help="12자 이상, UTF-8 기준 72바이트 이하"><input type="password" required minLength={12} maxLength={72} autoComplete="new-password" value={form.newPassword} onChange={(event) => setForm({ ...form, newPassword: event.target.value })}/></Field><Field label="새 비밀번호 확인"><input type="password" required minLength={12} maxLength={72} autoComplete="new-password" value={form.confirm} onChange={(event) => setForm({ ...form, confirm: event.target.value })}/></Field></div><div className="form-actions"><Button type="submit" variant="primary" disabled={saving}>{saving ? '변경 중…' : '비밀번호 변경'}</Button></div></form></Card>}</SettingsLayout>;
}

const personalKeyScopes = ['posts:read', 'posts:write', 'social:write', 'ai:use', 'mcp:use'] as const;
export function KeySettingsPage() {
  const { user } = useAuth(); const { notify } = useToast(); const query = useApiQuery<unknown>('/profile/keys');
  const keys = listFrom<PersonalKey>(query.data as PersonalKey[] | { items?: PersonalKey[] } | undefined);
  const [createOpen, setCreateOpen] = useState(false); const [edit, setEdit] = useState<{ key: PersonalKey; name: string; permissions: string[] } | null>(null); const [secret, setSecret] = useState<string | null>(null); const [working, setWorking] = useState<string | null>(null);
  const allowed = useMemo<string[]>(() => personalKeyScopes.filter((scope) => hasPermission(user?.permissions, scope)), [user?.permissions]);
  const [draft, setDraft] = useState({ name: '', permissions: ['posts:read'], expiresAt: '' });
  useEffect(() => { if (allowed.length && !draft.permissions.some((item) => allowed.includes(item))) setDraft((current) => ({ ...current, permissions: [allowed[0]] })); }, [allowed]);
  const create = async (event: FormEvent) => { event.preventDefault(); setWorking('create'); const expiresAt = draft.expiresAt ? new Date(`${draft.expiresAt}T23:59:59`).toISOString() : undefined; try { const result = await apiRequest<{ secret?: string; token?: string }>('/profile/keys', { method: 'POST', body: { name: draft.name, permissions: draft.permissions, ...(expiresAt ? { expiresAt } : {}) } }); setSecret(result.secret || result.token || null); setCreateOpen(false); setDraft({ name: '', permissions: allowed.slice(0, 1), expiresAt: '' }); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setWorking(null); } };
  const update = async (event: FormEvent) => { event.preventDefault(); if (!edit) return; setWorking(edit.key.id); try { await apiRequest(`/profile/keys/${encodeURIComponent(edit.key.id)}`, { method: 'PATCH', body: { name: edit.name, permissions: edit.permissions } }); notify('키 이름과 권한을 변경했습니다.', 'success'); setEdit(null); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setWorking(null); } };
  const rotate = async (key: PersonalKey) => { if (!window.confirm(`'${key.name}' 키를 회전하면 기존 값은 즉시 폐기됩니다. 계속할까요?`)) return; setWorking(key.id); try { const result = await apiRequest<{ secret?: string; token?: string }>(`/profile/keys/${encodeURIComponent(key.id)}/rotate`, { method: 'POST' }); setSecret(result.secret || result.token || null); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setWorking(null); } };
  const revoke = async (key: PersonalKey) => { if (!window.confirm(`'${key.name}' 키를 폐기할까요? 이 작업은 되돌릴 수 없습니다.`)) return; setWorking(key.id); try { await apiRequest(`/profile/keys/${encodeURIComponent(key.id)}`, { method: 'DELETE' }); notify('키를 폐기했습니다.', 'success'); query.reload(); } catch (error) { notify(readableError(error), 'error'); } finally { setWorking(null); } };
  return <SettingsLayout title="내 API·MCP 키" description="개인 키마다 최소 권한과 만료일을 지정하고 필요할 때 안전하게 회전합니다."><Card><SectionHeader title="발급된 키" description="비밀 값은 생성 또는 회전 직후 한 번만 표시됩니다." action={<Button variant="primary" onClick={() => setCreateOpen(true)} disabled={allowed.length === 0}><KeyRound/>새 키 만들기</Button>}/>{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : keys.length ? <div className="key-list">{keys.map((key) => <article key={key.id}><div><span className="key-icon"><KeyRound/></span><span><strong>{key.name}</strong><small><code>{key.prefix || key.id}</code> · 최근 사용 {formatDate(key.lastUsedAt)}</small></span></div><div className="key-permissions">{key.permissions.slice(0, 3).map((item) => <Badge key={item}>{item}</Badge>)}{key.permissions.length > 3 && <Badge>+{key.permissions.length - 3}</Badge>}</div><div><Badge tone={key.revokedAt ? 'danger' : 'positive'}>{key.revokedAt ? '폐기됨' : '활성'}</Badge><small>회전 {formatDate(key.rotatedAt, false)} · 만료 {formatDate(key.expiresAt, false)}</small></div><div><Button size="small" onClick={() => setEdit({ key, name: key.name, permissions: [...key.permissions] })} disabled={Boolean(key.revokedAt) || working === key.id}>권한 편집</Button><Button size="small" onClick={() => void rotate(key)} disabled={Boolean(key.revokedAt) || working === key.id}><RefreshCw/>회전</Button><Button size="small" variant="ghost" className="text-danger" onClick={() => void revoke(key)} disabled={Boolean(key.revokedAt) || working === key.id}>폐기</Button></div></article>)}</div> : <EmptyState title="발급된 키가 없습니다" description="외부 API 또는 MCP 도구가 필요할 때 최소 권한 키를 만드세요."/>}</Card>
    <Modal open={createOpen} onOpenChange={setCreateOpen} title="새 개인 키" description="현재 내 권한 안에서 필요한 범위만 선택하세요."><form className="settings-form" onSubmit={create}><Field label="키 이름"><input required maxLength={80} value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="예: 리서치 MCP"/></Field><Field label="만료일" help="비워두면 만료되지 않습니다."><input type="date" value={draft.expiresAt} onChange={(event) => setDraft({ ...draft, expiresAt: event.target.value })}/></Field><fieldset className="permission-picker"><legend>키 권한</legend>{allowed.map((permission) => <label key={permission}><input type="checkbox" checked={draft.permissions.includes(permission)} onChange={(event) => setDraft({ ...draft, permissions: event.target.checked ? [...draft.permissions, permission] : draft.permissions.filter((item) => item !== permission) })}/><span>{permission}</span></label>)}</fieldset><div className="form-actions"><Button type="button" onClick={() => setCreateOpen(false)}>취소</Button><Button type="submit" variant="primary" disabled={working === 'create' || !draft.name.trim() || draft.permissions.length === 0}>키 생성</Button></div></form></Modal>
    <Modal open={Boolean(edit)} onOpenChange={(open) => !open && setEdit(null)} title="개인 키 권한 편집" description="저장 즉시 이후 API와 MCP 요청에 새 권한이 적용됩니다.">{edit && <form className="settings-form" onSubmit={update}><Field label="키 이름"><input required maxLength={120} value={edit.name} onChange={(event) => setEdit({ ...edit, name: event.target.value })}/></Field><fieldset className="permission-picker"><legend>키 권한</legend>{allowed.map((permission) => <label key={permission}><input type="checkbox" checked={edit.permissions.includes(permission)} onChange={(event) => setEdit({ ...edit, permissions: event.target.checked ? [...edit.permissions, permission] : edit.permissions.filter((item) => item !== permission) })}/><span>{permission}</span></label>)}</fieldset><div className="form-actions"><Button type="button" onClick={() => setEdit(null)}>취소</Button><Button type="submit" variant="primary" disabled={working === edit.key.id || !edit.name.trim() || edit.permissions.length === 0}>권한 저장</Button></div></form>}</Modal>
    <Modal open={Boolean(secret)} onOpenChange={(open) => !open && setSecret(null)} title="새 비밀 키 — 지금 복사하세요" description="이 창을 닫으면 전체 값을 다시 볼 수 없습니다."><div className="one-time-secret"><code>{secret}</code><Button variant="primary" onClick={() => void navigator.clipboard.writeText(secret || '').then(() => notify('비밀 키를 복사했습니다.', 'success'))}>복사</Button></div></Modal>
  </SettingsLayout>;
}
