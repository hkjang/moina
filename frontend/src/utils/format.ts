export function formatRelativeTime(value: string | undefined) {
  if (!value) return '방금 전';
  const time = new Date(value).getTime();
  if (!Number.isFinite(time)) return value;
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
  if (seconds < 60) return '방금 전';
  if (seconds < 3600) return `${Math.floor(seconds / 60)}분 전`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}시간 전`;
  if (seconds < 604800) return `${Math.floor(seconds / 86400)}일 전`;
  return new Intl.DateTimeFormat('ko-KR', { month: 'short', day: 'numeric' }).format(new Date(time));
}

export function formatDate(value: string | undefined, includeTime = true) {
  if (!value) return '—';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat('ko-KR', { year: 'numeric', month: 'short', day: 'numeric', ...(includeTime ? { hour: '2-digit', minute: '2-digit' } : {}) }).format(date);
}

export function listFrom<T>(value: T[] | { items?: T[] } | undefined): T[] {
  return Array.isArray(value) ? value : value?.items || [];
}

export function topicLabel(value: string) {
  return `#${value.replace(/^#+/, '')}`;
}
