import { describe, expect, it } from 'vitest';
import { mergeWorkflowActions, splitWorkflowActions } from './workflowActions';

describe('승인 작업 편집기', () => {
  it('등록된 exact action과 고급 wildcard를 분리한다', () => {
    expect(splitWorkflowActions(['post.publish', 'post.*', 'agent.post.publish'])).toEqual({
      selected: ['post.publish'],
      advanced: ['post.*', 'agent.post.publish'],
    });
  });

  it('checkbox 선택과 고급 입력을 중복 없이 다시 결합한다', () => {
    expect(mergeWorkflowActions(['post.publish'], 'post.*\npost.publish, *')).toEqual([
      'post.publish', 'post.*', '*',
    ]);
  });
});
