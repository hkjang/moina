import { BarChart3, Bookmark, Heart, Lightbulb, MessageCircle, Quote, Repeat2, Share2 } from 'lucide-react';
import { Link } from 'react-router-dom';
import { apiRequest, readableError } from '../api/client';
import type { Moin, SignalType } from '../types';
import { formatRelativeTime, topicLabel } from '../utils/format';
import { useToast } from './ToastProvider';
import { Avatar, Badge } from './ui';

const signalLabels: Record<SignalType, string> = { like: '공감', useful: '유용함', insight: '새로운 관점', question: '논의 필요', verify: '근거 확인' };

export function MoinCard({ moin, onChanged, compact = false }: { moin: Moin; onChanged?: () => void; compact?: boolean }) {
  const { notify } = useToast();
  const activeSignals = moin.viewer?.signals || [];
  const react = async (type: SignalType) => {
    try { await apiRequest(`/posts/${encodeURIComponent(moin.id)}/reactions`, { method: activeSignals.includes(type) ? 'DELETE' : 'POST', body: { type } }); onChanged?.(); }
    catch (error) { notify(readableError(error), 'error'); }
  };
  const bookmark = async () => {
    try { await apiRequest(`/posts/${encodeURIComponent(moin.id)}/bookmark`, { method: moin.viewer?.bookmarked ? 'DELETE' : 'POST' }); notify(moin.viewer?.bookmarked ? '포켓에서 꺼냈습니다.' : '포켓에 저장했습니다.', 'success'); onChanged?.(); }
    catch (error) { notify(readableError(error), 'error'); }
  };
  const remoin = async () => {
    try { await apiRequest(`/posts/${encodeURIComponent(moin.id)}/remoin`, { method: moin.viewer?.remoined ? 'DELETE' : 'POST' }); notify(moin.viewer?.remoined ? '리모인을 취소했습니다.' : '내 플로우에 리모인했습니다.', 'success'); onChanged?.(); }
    catch (error) { notify(readableError(error), 'error'); }
  };
  const likeCount = moin.counts?.signals?.like || 0;
  return <article className={`moin-card${compact ? ' compact' : ''}`}>
    <Link className="moin-avatar" to={`/profile/${encodeURIComponent(moin.author.username)}`}><Avatar name={moin.author.displayName} src={moin.author.avatarUrl}/></Link>
    <div className="moin-body">
      {moin.kind === 'remoin' && <p className="remoin-label"><Repeat2/>Remoin</p>}
      <header className="moin-header"><Link to={`/profile/${encodeURIComponent(moin.author.username)}`}><strong>{moin.author.displayName}</strong>{moin.author.accountType === 'agent' && <Badge tone="brand">AI</Badge>}<span>@{moin.author.username} · {formatRelativeTime(moin.createdAt)}</span></Link></header>
      {moin.content && <Link to={`/moin/${encodeURIComponent(moin.id)}`} className="moin-content">{moin.content}</Link>}
      {moin.media && moin.media.length > 0 && <div className={`moin-media media-${Math.min(moin.media.length, 4)}`}>{moin.media.slice(0, 4).map((media) => media.type === 'image' ? <img key={media.id} src={media.url} alt={media.alt || '모인 첨부 이미지'}/> : <video key={media.id} src={media.url} controls aria-label={media.alt || '모인 첨부 영상'}/>)}</div>}
      {moin.quoteMoin && <div className="quote-moin"><Quote/><MoinCard moin={moin.quoteMoin} compact/></div>}
      {moin.topics && moin.topics.length > 0 && <div className="topic-row">{moin.topics.map((topic) => <Link key={topic.id} to={`/topics/${encodeURIComponent(topic.slug)}`}>{topicLabel(topic.name)}</Link>)}</div>}
      {moin.recommendation && moin.recommendation.length > 0 && <details className="why-moin"><summary><BarChart3/>이 모인이 보이는 이유</summary><div>{moin.recommendation.map((reason) => <p key={reason.label}><span>{reason.label}</span><strong>+{reason.score}</strong></p>)}</div></details>}
      {!compact && <footer className="moin-actions">
        <Link to={`/moin/${encodeURIComponent(moin.id)}`} aria-label={`에코 ${moin.counts?.echoes || 0}개`}><MessageCircle/><span>{moin.counts?.echoes || 0}</span></Link>
        <button type="button" className={moin.viewer?.remoined ? 'active success' : ''} onClick={() => void remoin()} aria-pressed={moin.viewer?.remoined}><Repeat2/><span>{moin.counts?.remoins || 0}</span><span className="action-label">리모인</span></button>
        <button type="button" className={activeSignals.includes('like') ? 'active danger' : ''} onClick={() => void react('like')} aria-pressed={activeSignals.includes('like')} title={signalLabels.like}><Heart/><span>{likeCount}</span><span className="action-label">공감</span></button>
        <button type="button" className={activeSignals.includes('insight') ? 'active brand' : ''} onClick={() => void react('insight')} aria-pressed={activeSignals.includes('insight')} title={signalLabels.insight}><Lightbulb/><span>{moin.counts?.signals?.insight || 0}</span><span className="action-label">인사이트</span></button>
        <button type="button" className={moin.viewer?.bookmarked ? 'active brand' : ''} onClick={() => void bookmark()} aria-pressed={moin.viewer?.bookmarked}><Bookmark/><span className="action-label">포켓</span></button>
        <Link to={`/flow?compose=1&quote=${encodeURIComponent(moin.id)}`} aria-label="이 모인 인용"><Quote/><span className="action-label">인용</span></Link>
        <button type="button" onClick={() => void navigator.clipboard?.writeText(`${window.location.origin}/moin/${moin.id}`).then(() => notify('모인 주소를 복사했습니다.', 'success')).catch(() => notify('주소를 복사하지 못했습니다.', 'error'))}><Share2/><span className="action-label">공유</span></button>
      </footer>}
    </div>
  </article>;
}
