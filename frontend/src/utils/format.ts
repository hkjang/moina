export function formatRelativeTime(value: string | undefined) {
  if (!value) return '방금 전';
  const time = new Date(value).getTime();
  if (!Number.isFinite(time)) return value;
  const now = Date.now();
  const seconds = Math.max(0, Math.floor((now - time) / 1000));
  if (seconds < 60) return '방금 전';
  if (seconds < 3600) return `${Math.floor(seconds / 60)}분 전`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}시간 전`;
  if (seconds < 604800) return `${Math.floor(seconds / 86400)}일 전`;
  // 해가 바뀐 Moin을 월·일만으로 표시하면 올해 글과 구분할 수 없다. 연도가
  // 다를 때만 연도를 덧붙여 평소 목록의 밀도는 그대로 유지한다.
  const date = new Date(time);
  const sameYear = date.getFullYear() === new Date(now).getFullYear();
  return new Intl.DateTimeFormat('ko-KR', { ...(sameYear ? {} : { year: 'numeric' }), month: 'short', day: 'numeric' }).format(date);
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
