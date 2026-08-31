import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { apiRequest } from '../api/client';
import type { Moin } from '../types';
import { MoinCard } from './MoinCard';
import { ToastProvider } from './ToastProvider';

vi.mock('../api/client', async (load) => {
  const actual = await load<typeof import('../api/client')>();
  return { ...actual, apiRequest: vi.fn() };
});

const mockedRequest = vi.mocked(apiRequest);
const moin = (): Moin => ({ id: 'm1', content: '테스트 모인', author: { id: 'u1', username: 'user', displayName: '사용자' }, createdAt: '2026-01-01T00:00:00Z', counts: { signals: { like: 0 }, bookmarks: 0, remoins: 0 }, viewer: { signals: [], bookmarked: false, remoined: false } });
const renderCard = (onMoinChange = vi.fn()) => {
  render(<MemoryRouter><ToastProvider><MoinCard moin={moin()} onMoinChange={onMoinChange}/></ToastProvider></MemoryRouter>);
  return onMoinChange;
};

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
