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

// 서버는 본문이 바뀔 때만 updatedAt을 옮기고, 생성 시점에는 createdAt과 같은
// 값으로 채운다. 두 값이 뜻있게 벌어졌을 때만 수정된 Moin으로 보고 그 시각을
// 돌려주므로, 호출하는 쪽은 반환값 유무로 수정 여부를 판단할 수 있다.
export function moinEditedAt(createdAt: string | undefined, updatedAt: string | undefined) {
  if (!createdAt || !updatedAt) return '';
  const created = new Date(createdAt).getTime();
  const updated = new Date(updatedAt).getTime();
  if (!Number.isFinite(created) || !Number.isFinite(updated)) return '';
  return updated - created >= 1000 ? updatedAt : '';
}

export function listFrom<T>(value: T[] | { items?: T[] } | undefined): T[] {
  return Array.isArray(value) ? value : value?.items || [];
}

export function topicLabel(value: string) {
  return `#${value.replace(/^#+/, '')}`;
}
