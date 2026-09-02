import { act, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import type { Moin } from '../types';
import { MoinCard } from './MoinCard';
import { ToastProvider } from './ToastProvider';

vi.mock('../api/client', async (load) => {
  const actual = await load<typeof import('../api/client')>();
  return { ...actual, apiRequest: vi.fn() };
});

// MoinCard calls useAuth exactly once per render, which makes it a faithful
// render counter without instrumenting the component under test.
const renderProbe = vi.fn();
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => {
    renderProbe();
    return { user: { id: 'u1', username: 'user', displayName: '사용자' } };
  },
}));

const moin = (id: string): Moin => ({
  id,
  content: `모인 ${id}`,
  author: { id: 'u1', username: 'user', displayName: '사용자' },
  createdAt: '2026-01-01T00:00:00Z',
  counts: { signals: { like: 0 }, bookmarks: 0, remoins: 0 },
  viewer: { signals: [], bookmarked: false, remoined: false },
});

describe('MoinCard 리렌더 비용', () => {
  it('memo로 감싸져 있어 변하지 않은 카드는 다시 그리지 않는다', () => {
    // A Flow keeps every loaded page mounted, so one reaction must not cost a
    // render of the whole list.
    const component = MoinCard as unknown as { $$typeof: symbol };
    expect(component.$$typeof).toBe(Symbol.for('react.memo'));
  });

  it('부모가 다시 렌더링되어도 같은 moin은 카드를 다시 렌더링하지 않는다', () => {
    const stable = moin('m1');
    const onMoinChange = vi.fn();
    const onMoinDelete = vi.fn();
    let bump = () => {};

    function Parent() {
      const [count, setCount] = useState(0);
      bump = () => setCount((value) => value + 1);
      return (
        <ToastProvider>
          <span data-testid="counter">{count}</span>
          <MoinCard moin={stable} onMoinChange={onMoinChange} onMoinDelete={onMoinDelete} />
        </ToastProvider>
      );
    }

    renderProbe.mockClear();
    const router = createMemoryRouter([{ path: '*', element: <Parent /> }], { initialEntries: ['/'] });
    render(<RouterProvider router={router} />);
    const initialRenders = renderProbe.mock.calls.length;
    expect(initialRenders).toBeGreaterThan(0);

    act(() => bump());

    expect(screen.getByTestId('counter')).toHaveTextContent('1');
    expect(screen.getByText('모인 m1')).toBeInTheDocument();
    expect(renderProbe.mock.calls.length).toBe(initialRenders);
  });

  it('moin이 실제로 바뀌면 카드를 다시 렌더링한다', () => {
    let replace = (_next: Moin) => {};

    function Parent() {
      const [value, setValue] = useState(moin('m1'));
      replace = setValue;
      return <ToastProvider><MoinCard moin={value} /></ToastProvider>;
    }

    renderProbe.mockClear();
    const router = createMemoryRouter([{ path: '*', element: <Parent /> }], { initialEntries: ['/'] });
    render(<RouterProvider router={router} />);
    const initialRenders = renderProbe.mock.calls.length;

    act(() => replace(moin('m2')));

    expect(screen.getByText('모인 m2')).toBeInTheDocument();
    expect(renderProbe.mock.calls.length).toBeGreaterThan(initialRenders);
  });
});
