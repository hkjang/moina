import { AtSign, Bell, Heart, MessageCircle, UserPlus } from 'lucide-react';
import { Link } from 'react-router-dom';
import { apiRequest, readableError } from '../api/client';
import { useToast } from '../components/ToastProvider';
import { Avatar, Button, EmptyState, ErrorState, LoadingState, PageHeader, Tabs } from '../components/ui';
import { useApiQuery } from '../hooks/useApiQuery';
import type { CursorPage, NotificationItem } from '../types';
import { formatDate, formatRelativeTime } from '../utils/format';
import { useState } from 'react';

const iconFor = (type: string) => type === 'follow' ? UserPlus : type === 'signal' ? Heart : type === 'echo' ? MessageCircle : type === 'mention' ? AtSign : Bell;

export default function NotificationsPage() {
  const [filter, setFilter] = useState('all');
  const { notify } = useToast();
  const query = useApiQuery<CursorPage<NotificationItem>>(`/notifications?filter=${filter}&limit=50`);
  const items = query.data?.items || [];
  const markRead = async () => { try { await apiRequest('/notifications/read', { method: 'POST', body: { all: true } }); notify('모든 알림을 읽음으로 표시했습니다.', 'success'); query.reload(); } catch (error) { notify(readableError(error), 'error'); } };
  return <div className="page-stack"><PageHeader title="알림" description="Link, 에코, Signal과 운영 알림을 실시간으로 확인합니다." actions={items.some((item) => !item.readAt) && <Button onClick={() => void markRead()}>모두 읽음</Button>}/><Tabs value={filter} onChange={setFilter} label="알림 필터" items={[{ value: 'all', label: '전체' }, { value: 'mention', label: '멘션' }, { value: 'signal', label: 'Signal' }]}/>{query.loading ? <LoadingState/> : query.error ? <ErrorState message={query.error} onRetry={query.reload}/> : items.length ? <div className="notification-list">{items.map((item) => { const Icon = iconFor(item.type); return <Link to={item.targetPath || '/notifications'} key={item.id} className={!item.readAt ? 'unread' : ''}><span className="notification-icon"><Icon/></span>{item.actor && <Avatar name={item.actor.displayName} src={item.actor.avatarUrl}/>}<span><strong>{item.title}</strong>{item.body && <p>{item.body}</p>}<small><time dateTime={item.createdAt} title={formatDate(item.createdAt)}>{formatRelativeTime(item.createdAt)}</time></small></span>{!item.readAt && <i aria-label="읽지 않음"/>}</Link>; })}</div> : <EmptyState title="새 알림이 없습니다" description="새로운 반응과 연결이 생기면 바로 알려드릴게요."/>}</div>;
}
