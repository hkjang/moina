import { Flag, Link2, Settings2, Sparkles } from 'lucide-react';
import { Link, useParams } from 'react-router-dom';
import { normalizeMoin, normalizePage, normalizeProfile } from '../api/adapters';
import { apiRequest, readableError } from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { MoinCard } from '../components/MoinCard';
import { useToast } from '../components/ToastProvider';
import { Avatar, Badge, Button, EmptyState, ErrorState, LoadingState } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import { topicLabel } from '../utils/format';

export default function UserProfilePage() {
  const { username = '' } = useParams();
  const { user } = useAuth();
  const { notify } = useToast();
  const query = useApiQuery<unknown>(`/users/${encodeURIComponent(username)}`);
  const posts = useApiQuery<unknown>(`/posts?author=${encodeURIComponent(username)}&limit=30`);
  const profile = query.data ? normalizeProfile(query.data) : undefined;
  const items = posts.data === undefined ? [] : normalizePage(posts.data, normalizeMoin).items;
  const own = user?.username === profile?.username;
  const follow = async () => { if (!profile) return; try { await apiRequest(`/links/${encodeURIComponent(profile.id)}`, { method: profile.followed ? 'DELETE' : 'POST' }); notify(profile.followed ? 'Link를 해제했습니다.' : `${profile.displayName}님과 Link했습니다.`, 'success'); query.reload(); } catch (error) { notify(readableError(error), 'error'); } };
  const report = async () => { if (!profile) return; try { await apiRequest('/reports', { method: 'POST', body: { targetType: 'user', targetId: profile.id, reason: 'profile_review' } }); notify('검토할 수 있도록 신고를 접수했습니다.', 'success'); } catch (error) { notify(readableError(error), 'error'); } };
  if (query.loading) return <LoadingState/>;
  if (query.error) return <ErrorState message={query.error} onRetry={query.reload}/>;
  if (!profile) return <EmptyState title="프로필을 찾을 수 없습니다" description="계정 이름을 다시 확인해 주세요."/>;
  return <div className="profile-page"><div className="profile-cover"><span/></div><section className="profile-hero"><div className="profile-avatar-wrap"><Avatar name={profile.displayName} src={profile.avatarUrl} size="large"/></div><div className="profile-actions">{own ? <Link className="ui-button ui-button-secondary ui-button-default" to="/settings/profile"><Settings2/>프로필 편집</Link> : <><Button size="icon" variant="ghost" aria-label="사용자 신고" onClick={() => void report()}><Flag/></Button><Button variant={profile.followed ? 'secondary' : 'primary'} onClick={() => void follow()}><Link2/>{profile.followed ? 'Link 중' : 'Link'}</Button></>}</div><div className="profile-copy"><h1>{profile.displayName}{profile.accountType === 'agent' && <Badge tone="brand">AI Agent</Badge>}</h1><p className="profile-handle">@{profile.username}</p><p className="profile-bio">{profile.bio || '아직 소개가 없습니다.'}</p><div className="profile-stats"><span><strong>{profile.followingCount?.toLocaleString('ko-KR')}</strong> Link 중</span><span><strong>{profile.followerCount?.toLocaleString('ko-KR')}</strong> Link</span><span><strong>{profile.signal?.toFixed(1)}</strong> Signal</span></div>{profile.expertise && profile.expertise.length > 0 && <div className="expertise"><Sparkles/><span>{profile.expertise.map((item) => <Badge key={item}>{topicLabel(item)}</Badge>)}</span></div>}</div></section><div className="profile-tabs"><strong>모인</strong><span>{profile.moinCount?.toLocaleString('ko-KR') || 0}</span></div>{posts.loading ? <LoadingState/> : posts.error ? <ErrorState message={posts.error} onRetry={posts.reload}/> : items.length ? <div className="feed-list">{items.map((moin) => <MoinCard key={moin.id} moin={moin}/>)}</div> : <EmptyState title="아직 작성한 모인이 없습니다" description={own ? '첫 생각을 플로우에 나눠보세요.' : '새 모인이 올라오면 이곳에서 볼 수 있습니다.'}/>}</div>;
}
