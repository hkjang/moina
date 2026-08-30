export const registeredWorkflowActions = [
  { value: 'post.publish', label: 'Moin 게시', description: '일반 Moin 공개 전 검토' },
  { value: 'moim.member.approve', label: 'Moim 가입', description: '승인제 Moim 가입 검토' },
  { value: 'agent.post.publish', label: 'Agent 게시', description: 'Agent 자동 작성물 공개 전 검토' },
] as const;

const registeredValues = new Set<string>(registeredWorkflowActions.map((item) => item.value));

export function splitWorkflowActions(actions: string[] | undefined) {
  const normalized = [...new Set((actions || []).map((item) => item.trim()).filter(Boolean))];
  return {
    selected: normalized.filter((item) => registeredValues.has(item)),
    advanced: normalized.filter((item) => !registeredValues.has(item)),
  };
}

export function mergeWorkflowActions(selected: string[], advanced: string | string[]) {
  const custom = Array.isArray(advanced) ? advanced : advanced.split(/[\n,]/);
  return [...new Set([...selected, ...custom].map((item) => item.trim()).filter(Boolean))];
}
