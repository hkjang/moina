import { cleanup, render } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, it } from 'vitest';
import type { Moin } from '../types';
import { AuthProvider } from '../auth/AuthContext';
import { MoinCard } from '../components/MoinCard';
import { QuickNavigation } from '../components/QuickNavigation';
import { ToastProvider } from '../components/ToastProvider';
import { Button, Card, Field, PageHeader, SwitchField } from '../components/ui';
import { NotFoundPage } from '../pages/StatePages';
import { expectNoA11yViolations } from './a11y';

afterEach(() => cleanup());

describe('대표 DOM 접근성', () => {
  it('공통 form/card 구성요소가 axe-core 규칙을 만족한다', async () => {
    const { container } = render(
      <main>
        <PageHeader title="개인화 설정" description="내 화면과 알림을 설정합니다." />
        <Card aria-labelledby="profile-form-title">
          <h2 id="profile-form-title">프로필</h2>
          <Field label="표시 이름" help="대화에 표시되는 이름입니다."><input name="displayName" /></Field>
          <SwitchField label="알림 받기" description="새 Echo를 알려드립니다." checked onChange={() => undefined} />
          <Button variant="primary">저장</Button>
        </Card>
      </main>,
    );
    await expectNoA11yViolations(container);
  });

  it('MoinCard의 링크와 반응 control이 axe-core 규칙을 만족한다', async () => {
    const moin: Moin = {
      id: 'm1',
      content: '접근성 있는 지식 모인',
      author: { id: 'u1', username: 'tester', displayName: '테스터' },
      createdAt: '2026-08-30T00:00:00Z',
      counts: { echoes: 1, remoins: 0, bookmarks: 0, signals: { like: 0, insight: 0 } },
      viewer: { signals: [], bookmarked: false, remoined: false },
    };
    const { container } = render(
      <MemoryRouter>
        <AuthProvider><ToastProvider><main><MoinCard moin={moin} /></main></ToastProvider></AuthProvider>
      </MemoryRouter>,
    );
    await expectNoA11yViolations(container);
  });

  it('대표 fallback route가 axe-core 규칙을 만족한다', async () => {
    const { container } = render(<MemoryRouter><main><NotFoundPage /></main></MemoryRouter>);
    await expectNoA11yViolations(container);
  });

  it('빠른 이동 combobox와 결과 목록이 axe-core 규칙을 만족한다', async () => {
    const { container } = render(
      <MemoryRouter>
        <QuickNavigation
          open
          onOpenChange={() => undefined}
          userId="a11y-quick-navigation"
          username="tester"
          permissions={['*']}
          approvalVisible
        />
      </MemoryRouter>,
    );
    await expectNoA11yViolations(container.ownerDocument.body);
  });
});
