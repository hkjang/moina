import { describe, expect, it } from 'vitest';
import { activeMention } from './useMentionAutocomplete';

describe('activeMention', () => {
  it('단어 시작 위치의 @만 멘션으로 인식한다', () => {
    expect(activeMention('@user', 5)).toEqual({ start: 0, end: 5, query: 'user' });
    expect(activeMention('안녕 @user', 8)).toEqual({ start: 3, end: 8, query: 'user' });
  });

  it('이메일 주소나 단어 중간의 @는 목록을 열지 않는다', () => {
    // Typing an address must not turn the domain into a mention search.
    expect(activeMention('me@example.com', 14)).toBeNull();
    expect(activeMention('abc@def', 7)).toBeNull();
  });

  it('@만 입력한 상태는 빈 검색어로 추천 목록을 연다', () => {
    expect(activeMention('@', 1)).toEqual({ start: 0, end: 1, query: '' });
  });

  it('커서 앞부분만 본다', () => {
    // The caret is inside "@ab"; the text after it is not part of the query.
    expect(activeMention('@abcdef', 3)).toEqual({ start: 0, end: 3, query: 'ab' });
  });

  it('공백이 끼면 멘션이 끊긴다', () => {
    expect(activeMention('@user 다음 문장', 11)).toBeNull();
  });

  it('한글과 허용 기호를 포함한 아이디를 인식한다', () => {
    expect(activeMention('@사용자.이름-1', 9)).toEqual({ start: 0, end: 9, query: '사용자.이름-1' });
  });

  it('39자를 넘는 후보는 멘션으로 보지 않는다', () => {
    const long = 'a'.repeat(40);
    expect(activeMention(`@${long}`, long.length + 1)).toBeNull();
  });
});
