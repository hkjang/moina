import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { apiRequest } from '../api/client';
import type { Moin } from '../types';
import { MoinCard } from './MoinCard';
import { ToastProvider } from './ToastProvider';

vi.mock('../api/client', async (load) => {
  const actual = await load<typeof import('../api/client')>();
  return { ...actual, apiRequest: vi.fn() };
});

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'u1', username: 'user', displayName: '사용자' },
  }),
}));

const mockedRequest = vi.mocked(apiRequest);
const moin = (): Moin => ({ id: 'm1', content: '테스트 모인', author: { id: 'u1', username: 'user', displayName: '사용자' }, createdAt: '2026-01-01T00:00:00Z', counts: { signals: { like: 0 }, bookmarks: 0, remoins: 0 }, viewer: { signals: [], bookmarked: false, remoined: false } });
const renderCardWithRouter = (onMoinChange = vi.fn(), value = moin()) => {
  const router = createMemoryRouter([
    {
      path: '*',
      element: <ToastProvider><MoinCard moin={value} onMoinChange={onMoinChange}/></ToastProvider>,
    },
  ], { initialEntries: ['/flow'] });
  render(<RouterProvider router={router}/>);
  return { onMoinChange, router };
};
const renderCard = (onMoinChange = vi.fn()) =>
  renderCardWithRouter(onMoinChange).onMoinChange;

describe('MoinCard optimistic mutation', () => {
  beforeEach(() => mockedRequest.mockReset());
  afterEach(() => cleanup());

  it('반응·저장·공유 아이콘 버튼에 항상 접근 가능한 이름을 제공한다', () => {
    renderCard();
    expect(screen.getByRole('button', { name: '리모인 0개' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '공감 0개' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '새로운 관점 0개' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '포켓' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '모인 주소 복사' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '모인 수정' })).toBeInTheDocument();
  });

  it('승인 대기 중인 모인에는 실패할 수정 동작을 노출하지 않는다', () => {
    renderCardWithRouter(vi.fn(), { ...moin(), status: 'pending_approval' });
    expect(screen.queryByRole('button', { name: '모인 수정' })).not.toBeInTheDocument();
  });

  it('본인 모인을 수정하고 PATCH 응답을 카드와 cache callback에 반영한다', async () => {
    mockedRequest.mockImplementation((path) => {
      if (path === '/media/config') return Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 });
      if (path === '/posts/m1') return Promise.resolve({
        ...moin(),
        content: '수정된 모인',
        updatedAt: '2026-01-02T00:00:00Z',
      });
      return Promise.resolve({});
    });
    const onChange = renderCard();

    fireEvent.click(screen.getByRole('button', { name: '모인 수정' }));
    const editor = await screen.findByLabelText('수정할 모인 내용');
    await waitFor(() => expect(editor).toHaveFocus());
    fireEvent.change(editor, {
      target: { value: '수정된 모인' },
    });
    fireEvent.click(screen.getByRole('button', { name: '변경사항 저장' }));

    await waitFor(() => expect(mockedRequest).toHaveBeenCalledWith(
      '/posts/m1',
      expect.objectContaining({
        method: 'PATCH',
        body: expect.objectContaining({ content: '수정된 모인', mediaIds: [] }),
      }),
    ));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ content: '수정된 모인' }),
    ));
    expect(screen.getByText('수정된 모인')).toBeInTheDocument();
  });

  it('수정 중인 초안은 확인 없이 닫아 잃지 않는다', async () => {
    mockedRequest.mockImplementation((path) =>
      path === '/media/config'
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 })
        : Promise.resolve({}),
    );
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    renderCard();
    fireEvent.click(screen.getByRole('button', { name: '모인 수정' }));
    fireEvent.change(await screen.findByLabelText('수정할 모인 내용'), {
      target: { value: '저장하지 않은 내용' },
    });

    fireEvent.click(screen.getByRole('button', { name: '창 닫기' }));

    expect(confirm).toHaveBeenCalledWith('수정 중인 내용과 첨부 변경을 버리고 닫을까요?');
    expect(screen.getByLabelText('수정할 모인 내용')).toHaveValue('저장하지 않은 내용');
    confirm.mockRestore();
  });

  it('초안을 버리고 이동하면 같은 Flow에 남아도 수정 모달을 닫는다', async () => {
    mockedRequest.mockImplementation((path) =>
      path === '/media/config'
        ? Promise.resolve({ maxUploadBytes: 1024, maxPerPost: 4 })
        : Promise.resolve({}),
    );
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { router } = renderCardWithRouter();
    fireEvent.click(screen.getByRole('button', { name: '모인 수정' }));
    fireEvent.change(await screen.findByLabelText('수정할 모인 내용'), {
      target: { value: '이동하며 버릴 내용' },
    });

    await act(async () => {
      await router.navigate('/flow?compose=1&quote=m1');
    });

    await waitFor(() =>
      expect(confirm).toHaveBeenCalledWith(
        '수정 중인 내용과 첨부 변경을 버리고 이동할까요?',
      ),
    );
    await waitFor(() =>
      expect(screen.queryByLabelText('수정할 모인 내용')).not.toBeInTheDocument(),
    );
    expect(router.state.location.search).toBe('?compose=1&quote=m1');
  });

  it('Signal 요청 완료 전에 UI와 cache callback을 먼저 갱신한다', async () => {
    let resolve!: (value: unknown) => void;
    mockedRequest.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
    const onChange = renderCard();
    const button = screen.getByTitle('공감');
    fireEvent.click(button);
    expect(button).toHaveAttribute('aria-pressed', 'true');
    expect(button).toHaveTextContent('1');
    expect(onChange).toHaveBeenCalledTimes(1);
    act(() => resolve({}));
    await waitFor(() => expect(button).not.toBeDisabled());
  });

  it('처리 중 같은 작업을 빠르게 반복해도 요청을 중복 전송하지 않는다', () => {
    mockedRequest.mockReturnValueOnce(new Promise(() => {}));
    renderCard();
    const button = screen.getByTitle('공감');
    fireEvent.click(button);
    fireEvent.click(button);
    expect(mockedRequest).toHaveBeenCalledTimes(1);
  });

  it('Signal 실패 시 이전 상태로 rollback한다', async () => {
    mockedRequest.mockRejectedValueOnce(new Error('네트워크 실패'));
    const onChange = renderCard();
    const button = screen.getByTitle('공감');
    fireEvent.click(button);
    await waitFor(() => expect(button).toHaveAttribute('aria-pressed', 'false'));
    expect(button).toHaveTextContent('0');
    expect(onChange).toHaveBeenCalledTimes(2);
    expect(screen.getByText('네트워크 실패')).toBeInTheDocument();
  });

  it('Pocket은 refetch 없이 optimistic 상태를 유지한다', async () => {
    mockedRequest.mockResolvedValueOnce({ bookmarked: true });
    renderCard();
    const button = screen.getByRole('button', { name: '포켓' });
    fireEvent.click(button);
    expect(button).toHaveAttribute('aria-pressed', 'true');
    expect(mockedRequest).toHaveBeenCalledWith('/posts/m1/bookmark', expect.objectContaining({ method: 'POST' }));
  });

  it('Remoin 실패 시 count와 pressed 상태를 함께 rollback한다', async () => {
    mockedRequest.mockRejectedValueOnce(new Error('처리 실패'));
    renderCard();
    const button = screen.getByRole('button', { name: /리모인/ });
    fireEvent.click(button);
    await waitFor(() => expect(button).toHaveAttribute('aria-pressed', 'false'));
    expect(button).toHaveTextContent('0');
  });

	it('승인 대기 Remoin은 활성 상태를 유지하되 공개 count는 늘리지 않는다', async () => {
		mockedRequest.mockResolvedValueOnce({ id: 'pending-remoin', status: 'pending_approval' });
		renderCard();
		const button = screen.getByRole('button', { name: /리모인/ });
		fireEvent.click(button);
		await waitFor(() => expect(button).not.toBeDisabled());
		expect(button).toHaveAttribute('aria-pressed', 'true');
		expect(button).toHaveTextContent('0');
		expect(screen.getByText('리모인이 승인 대기 상태로 접수되었습니다.')).toBeInTheDocument();
	});
});
