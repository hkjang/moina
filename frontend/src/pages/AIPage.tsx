import { Bot, Eraser, Send, Settings2, Sparkles, Square } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { streamRequest, readableError } from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Avatar, Button, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import { hasPermission } from '../navigation';

interface AIStatus { enabled?: boolean; model?: string; apiStyle?: string; defaultMaxTokens?: number; maxTokens?: number }
interface ChatMessage { id: string; role: 'user' | 'assistant'; content: string; error?: boolean; streaming?: boolean }

const suggestions = ['오늘 관심 토픽의 핵심 대화를 요약해줘', '이 글을 더 명확한 모인으로 다듬어줘', '긴 Chain의 찬반 관점을 구조화해줘', '외국어 모인을 자연스러운 한국어로 번역해줘'];

export default function AIPage() {
  const { user } = useAuth();
  const status = useApiQuery<AIStatus>('/ai/status');
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const [announcement, setAnnouncement] = useState('');
  const [maxTokens, setMaxTokens] = useState(4096);
  const abort = useRef<AbortController | null>(null);
  const end = useRef<HTMLDivElement>(null);
  const limit = Math.min(262144, Math.max(1, status.data?.maxTokens || status.data?.defaultMaxTokens || 4096));
  const options = useMemo(() => [...new Set([4096, 8192, 32768, 65536, 131072, 262144, limit].filter((value) => value <= limit))].sort((a, b) => a - b), [limit]);
  useEffect(() => { setMaxTokens(status.data?.defaultMaxTokens && status.data.defaultMaxTokens <= limit ? status.data.defaultMaxTokens : limit); }, [limit, status.data?.defaultMaxTokens]);
  useEffect(() => end.current?.scrollIntoView({ behavior: 'smooth' }), [messages]);
  useEffect(() => () => abort.current?.abort(), []);
  const ask = async (question: string) => {
    const text = question.trim(); if (!text || streaming) return;
    const userMessage: ChatMessage = { id: crypto.randomUUID(), role: 'user', content: text };
    const assistantID = crypto.randomUUID();
    setMessages((current) => [...current, userMessage, { id: assistantID, role: 'assistant', content: '', streaming: true }]);
    setInput(''); setStreaming(true); setAnnouncement('AI가 답변을 만들고 있습니다.');
    const controller = new AbortController(); abort.current = controller;
    try {
      const history = [...messages.filter((item) => !item.error && !item.streaming), userMessage].map(({ role, content }) => ({ role, content }));
      await streamRequest('/ai/chat', { messages: history, maxTokens }, (event) => { if (event.delta) setMessages((current) => current.map((item) => item.id === assistantID ? { ...item, content: item.content + event.delta } : item)); }, controller.signal);
      setMessages((current) => current.map((item) => item.id === assistantID ? { ...item, streaming: false, content: item.content || '응답 내용이 없습니다.' } : item));
      setAnnouncement('AI 답변이 완료되었습니다.');
    } catch (error) {
      const stopped = error instanceof DOMException && error.name === 'AbortError';
      setMessages((current) => current.map((item) => item.id === assistantID ? { ...item, streaming: false, error: !stopped, content: item.content || (stopped ? '답변 생성을 중지했습니다.' : readableError(error)) } : item));
      setAnnouncement(stopped ? 'AI 답변 생성을 중지했습니다.' : 'AI 답변 중 오류가 발생했습니다.');
    } finally { setStreaming(false); abort.current = null; }
  };
  const submit = (event: FormEvent) => { event.preventDefault(); void ask(input); };
  if (status.loading) return <div className="page-stack"><PageHeader title="AI 어시스턴트"/><LoadingState/></div>;
  if (status.error) return <div className="page-stack"><PageHeader title="AI 어시스턴트"/><ErrorState message={status.error} onRetry={status.reload}/></div>;
  if (!status.data?.enabled) return <div className="page-stack"><PageHeader title="AI 어시스턴트" description="MOINA의 대화와 지식을 더 잘 이해하도록 돕습니다."/><EmptyState title="AI 기능이 꺼져 있습니다" description="서비스 관리자가 OpenAI 호환 AI 공급자를 설정하면 스트리밍 대화를 사용할 수 있습니다." action={hasPermission(user?.permissions, 'settings:manage') && <Link className="ui-button ui-button-primary ui-button-default" to="/admin/ai"><Settings2/>AI 설정 열기</Link>}/></div>;
  return <div className="ai-page"><PageHeader eyebrow="STREAMING" title="AI 어시스턴트" description="글 다듬기, Chain 요약, 번역과 관심사 탐색을 대화로 요청하세요." actions={<><select aria-label="최대 응답 토큰" value={maxTokens} onChange={(event) => setMaxTokens(Number(event.target.value))} disabled={streaming}>{options.map((value) => <option key={value} value={value}>최대 {value >= 1024 ? `${value / 1024}K` : value} 토큰</option>)}</select>{messages.length > 0 && <Button onClick={() => { abort.current?.abort(); setMessages([]); }}><Eraser/>새 대화</Button>}</>}/>
    <section className="chat-shell"><header><span className="ai-avatar"><Bot/></span><span><strong>MOINA AI</strong><small>{status.data.model || '설정된 모델'} · SSE 스트리밍</small></span><i>연결됨</i></header><div className="chat-messages custom-scrollbar">{messages.length === 0 ? <div className="ai-welcome"><span className="ai-hero"><Sparkles/></span><h2>어떤 생각을 함께 다듬을까요?</h2><p>AI의 답변은 초안이며, 중요한 사실과 출처는 직접 확인해 주세요.</p><div>{suggestions.map((suggestion) => <button type="button" onClick={() => void ask(suggestion)} key={suggestion}><Sparkles/><span>{suggestion}</span></button>)}</div></div> : messages.map((message) => <article className={`chat-message ${message.role} ${message.error ? 'error' : ''}`} key={message.id}><Avatar name={message.role === 'user' ? user?.displayName || '나' : 'AI'} src={message.role === 'user' ? user?.avatarUrl : undefined}/><div><strong>{message.role === 'user' ? user?.displayName || '나' : 'MOINA AI'}</strong><p>{message.content}{message.streaming && <span className="stream-cursor"/>}</p></div></article>)}<div ref={end}/></div>
      <span className="sr-only" aria-live="polite">{announcement}</span>
      <form className="chat-composer" onSubmit={submit}><textarea rows={2} value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); void ask(input); } }} placeholder="MOINA AI에게 질문하세요. Shift+Enter로 줄바꿈" aria-label="AI 질문" disabled={streaming}/><footer><span>최대 {Math.round(maxTokens / 1024)}K 토큰</span>{streaming ? <Button type="button" variant="danger" onClick={() => abort.current?.abort()}><Square/>생성 중지</Button> : <Button type="submit" variant="primary" disabled={!input.trim()}><Send/>보내기</Button>}</footer></form>
    </section>
  </div>;
}
