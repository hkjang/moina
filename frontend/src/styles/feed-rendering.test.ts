import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const feedCSS = readFileSync(resolve(process.cwd(), 'src/styles/feed.css'), 'utf8');

describe('Flow 카드 렌더링 계약', () => {
  it('피드 카드는 화면 밖 렌더링을 건너뛴다', () => {
    // A Flow keeps every loaded page mounted, so this is what stops style and
    // layout cost from growing with every "더 보기" click.
    expect(feedCSS).toMatch(/\.feed-list\s*>\s*\.moin-card\s*\{[^}]*content-visibility:\s*auto/);
  });

  it('건너뛴 카드도 높이를 기억해 스크롤이 튀지 않는다', () => {
    // The auto keyword is what makes a card remember its rendered height; a
    // fixed value would make the scrollbar jump as cards are measured.
    expect(feedCSS).toMatch(/\.feed-list\s*>\s*\.moin-card\s*\{[^}]*contain-intrinsic-size:\s*auto\s/);
  });

  it('인용 카드에는 적용하지 않는다', () => {
    // A quoted card is small and always inside a parent card, so containing it
    // separately would only add boundaries without skipping meaningful work.
    expect(feedCSS).not.toMatch(/\.moin-card\.compact\s*\{[^}]*content-visibility/);
  });
});
