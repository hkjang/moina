import { useEffect, useState, type FormEvent } from 'react';
import { Navigate, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { ArrowRight, Bot, Network, ShieldCheck, Sparkles } from 'lucide-react';
import { apiRequest, readableError } from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { API_BASE, APP_NAME, APP_VERSION, normalizeVersion, safeAppPath } from '../config';
import type { PublicConfig } from '../types';

export default function LoginPage() {
  const { user, loading, login, refresh } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [params] = useSearchParams();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [email, setEmail] = useState('');
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [config, setConfig] = useState<PublicConfig>({});
  const requestedPath = safeAppPath(params.get('returnTo') || (location.state as { from?: string } | null)?.from);

  useEffect(() => {
    const controller = new AbortController();
    Promise.allSettled([
      apiRequest<{ enabled?: boolean; label?: string; providerName?: string; allowRegistration?: boolean; registrationEnabled?: boolean }>('/auth/oidc/status', { signal: controller.signal, suppressUnauthorized: true }),
      apiRequest<{ version?: string } | string>('/version', { signal: controller.signal, suppressUnauthorized: true }),
    ]).then(([oidc, version]) => setConfig({
      oidc: oidc.status === 'fulfilled' ? oidc.value : undefined,
      version: version.status === 'fulfilled' ? typeof version.value === 'string' ? version.value : version.value.version : APP_VERSION,
    }));
    return () => controller.abort();
  }, []);

  if (!loading && user) return <Navigate to={requestedPath} replace/>;
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setSubmitting(true); setError(null);
    try {
      if (mode === 'register') {
        await apiRequest('/auth/register', { method: 'POST', suppressUnauthorized: true, body: { username: username.trim(), displayName: displayName.trim(), email: email.trim(), password } });
        await refresh();
      } else await login(username.trim(), password);
      navigate(requestedPath, { replace: true });
    }
    catch (cause) { setError(readableError(cause)); }
    finally { setSubmitting(false); }
  };
  const oidc = () => window.location.assign(`${API_BASE}/auth/oidc/login?returnTo=${encodeURIComponent(requestedPath)}`);
  const version = normalizeVersion(config.version || APP_VERSION);
  const registrationAllowed = config.oidc?.allowRegistration === true || config.oidc?.registrationEnabled === true;
  const changeMode = (next: 'login' | 'register') => { setMode(next); setError(null); setPassword(''); };
  return <main className="login-page">
    <section className="login-story" aria-label="MOINA 서비스 소개">
      <div className="login-brand"><img className="brand-symbol brand-symbol-large" src="/moina-mark.svg" alt=""/><strong>MOINA</strong></div>
      <div className="login-copy"><p className="eyebrow">AI SOCIAL KNOWLEDGE NETWORK</p><h1>사람의 생각이<br/>지식으로 이어지는 곳.</h1><p>사람, 관심사, 대화와 스트리밍 AI가 연결되는 투명한 소셜 네트워크를 만나보세요.</p></div>
      <div className="login-features"><span><Network/>관심사 그래프</span><span><Sparkles/>투명한 For Me</span><span><Bot/>스트리밍 AI</span></div>
      <p className="offline-note"><ShieldCheck/>외부 CDN 없이 오프라인망에서 운영할 수 있습니다.</p>
    </section>
    <section className="login-panel">
      <div className="mobile-login-brand"><img className="brand-symbol" src="/moina-mark.svg" alt=""/><strong>MOINA</strong></div>
      <div className="login-card">
        <div className="login-heading"><p className="eyebrow">{mode === 'login' ? '다시 만나서 반가워요' : '새로운 연결을 시작해요'}</p><h2>{mode === 'login' ? '로그인' : '계정 만들기'}</h2><p>{mode === 'login' ? 'MOINA 계정으로 안전하게 시작하세요.' : '관리자가 허용한 로컬 계정을 만듭니다.'}</p></div>
        {params.get('expired') === '1' && <div className="login-notice" role="status">세션이 만료되었습니다. 다시 로그인해 주세요.</div>}
        {error && <div className="login-error" role="alert">{error}</div>}
        <form className="login-form" onSubmit={submit}>
          {mode === 'register' && <><label><span>표시 이름</span><input required maxLength={80} autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} disabled={submitting} placeholder="대화에 표시할 이름"/></label><label><span>이메일</span><input required type="email" autoComplete="email" value={email} onChange={(event) => setEmail(event.target.value)} disabled={submitting} placeholder="name@example.com"/></label></>}
          <label><span>사용자 이름</span><input required autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} disabled={submitting} placeholder="사용자 이름 입력"/></label>
          <label><span>비밀번호</span><input required type="password" minLength={mode === 'register' ? 12 : undefined} autoComplete={mode === 'register' ? 'new-password' : 'current-password'} value={password} onChange={(event) => setPassword(event.target.value)} disabled={submitting} placeholder={mode === 'register' ? '12자 이상 입력' : '비밀번호 입력'}/></label>
          <button type="submit" className="login-submit" disabled={submitting || loading}>{submitting ? (mode === 'register' ? '가입 중…' : '로그인 중…') : <><span>{mode === 'register' ? '가입하고 시작' : '로그인'}</span><ArrowRight/></>}</button>
        </form>
        {mode === 'login' && (config.oidc?.enabled ? <><div className="login-divider"><span>또는</span></div><button type="button" className="oidc-button" onClick={oidc}><ShieldCheck/><span>{config.oidc.label || config.oidc.providerName || 'Keycloak SSO'}로 로그인</span><ArrowRight/></button></> : <p className="login-help">관리자가 SSO를 설정하면 Keycloak 로그인 버튼이 자동으로 표시됩니다.</p>)}
        {registrationAllowed && <button type="button" className="login-mode-switch" onClick={() => changeMode(mode === 'login' ? 'register' : 'login')}>{mode === 'login' ? '처음이신가요? 계정 만들기' : '이미 계정이 있나요? 로그인'}</button>}
      </div>
      <footer className="login-footer"><span>{APP_NAME} {version}</span><span aria-hidden="true">·</span><span>오프라인 운영 지원</span></footer>
    </section>
  </main>;
}
