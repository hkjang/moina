import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { apiRequest } from '../api/client';
import { ToastProvider } from '../components/ToastProvider';
import FlowPage from './FlowPage';

vi.mock('../api/client', async (load) => {
  const actual = await load<typeof import('../api/client')>();
  return { ...actual, apiRequest: vi.fn() };
});

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'u1', username: 'tester', displayName: '테스터' },
  }),
}));

vi.mock('../hooks/useFeedPages', () => ({
  useFeedPages: () => ({
    items: [],
    loading: false,
    error: null,
    nextCursor: undefined,
    loadMore: vi.fn(),
    reload: vi.fn(),
    reloadFirstPage: vi.fn(),
    updateMoin: vi.fn(),
  }),
}));

vi.mock('../hooks/useApiQuery', () => ({
  useApiQuery: (path: string | null) => {
    if (path === '/posts/quote-1') {
      return {
        data: {
          id: 'quote-1',
          content: '보존해야 할 인용 원문',
          author: { id: 'u2', username: 'quoted', displayName: '인용 작성자' },
          createdAt: '2026-08-31T00:00:00Z',
        },
        loading: false,
        error: null,
        reload: vi.fn(),
      };
    }
    if (path === '/media/config') {
      return {
        data: { maxUploadBytes: 10 * 1024 * 1024, maxPerPost: 4 },
        loading: false,
        error: null,
        reload: vi.fn(),
      };
    }
    return { data: undefined, loading: false, error: null, reload: vi.fn() };
  },
}));

const mockedRequest = vi.mocked(apiRequest);

function renderFlow() {
  const router = createMemoryRouter([
    {
      path: '/flow',
      element: <ToastProvider><FlowPage /></ToastProvider>,
    },
    {
      path: '/other',
      element: <h1>다른 화면</h1>,
    },
  ], { initialEntries: ['/flow?compose=1&quote=quote-1'] });
  render(<RouterProvider router={router} />);
  return router;
}

function pasteImage(target: Element) {
  const file = new File(['png'], '붙여넣기.png', { type: 'image/png' });
  const event = new Event('paste', { bubbles: true, cancelable: true });
  Object.defineProperty(event, 'clipboardData', {
    configurable: true,
    value: {
      files: [file],
      items: [{ kind: 'file', type: file.type, getAsFile: () => file }],
      types: ['Files'],
    },
  });
  fireEvent(target, event);
}

describe('FlowPage composer navigation guard', () => {
  beforeEach(() => {
    mockedRequest.mockReset();
    mockedRequest.mockResolvedValue({});
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:flow-navigation-test'),
    });
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn(),
    });
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('확인을 거부하면 URL과 작성 내용 및 인용 원문을 보존한다', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const router = renderFlow();
    const textarea = await screen.findByRole('textbox', { name: '모인 내용' });
    fireEvent.change(textarea, { target: { value: '아직 저장하지 않은 작성 내용' } });

    await act(async () => {
      await router.navigate('/other');
    });

    await waitFor(() => expect(confirm).toHaveBeenCalledWith('작성 중인 내용과 첨부를 버리고 이동할까요?'));
    expect(router.state.location.pathname).toBe('/flow');
    expect(router.state.location.search).toBe('?compose=1&quote=quote-1');
    expect(textarea).toHaveValue('아직 저장하지 않은 작성 내용');
    expect(screen.getByText('보존해야 할 인용 원문')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '다른 화면' })).not.toBeInTheDocument();
  });

  it('인용 대상 이동을 승인하면 이전 작성 초안을 버리고 새 작성기로 전환한다', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const router = renderFlow();
    fireEvent.change(await screen.findByRole('textbox', { name: '모인 내용' }), {
      target: { value: '다른 인용에 넘기지 않을 초안' },
    });

    await act(async () => {
      await router.navigate('/flow?compose=1&quote=quote-2');
    });

    await waitFor(() =>
      expect(confirm).toHaveBeenCalledWith(
        '작성 중인 내용과 첨부를 버리고 이동할까요?',
      ),
    );
    expect(router.state.location.search).toBe('?compose=1&quote=quote-2');
    expect(await screen.findByRole('textbox', { name: '모인 내용' })).toHaveValue('');
    expect(screen.queryByText('보존해야 할 인용 원문')).not.toBeInTheDocument();
  });

  it('미디어 업로드 중에는 확인 없이 다른 경로로 이동하지 않는다', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    mockedRequest.mockImplementation((_path, options) => new Promise((_resolve, reject) => {
      options?.signal?.addEventListener('abort', () => reject(new DOMException('취소됨', 'AbortError')));
    }));
    const router = renderFlow();
    const textarea = await screen.findByRole('textbox', { name: '모인 내용' });
    pasteImage(textarea);
    await screen.findByText('업로드 중');

    await act(async () => {
      await router.navigate('/other');
    });

    await screen.findByText('업로드 또는 게시가 끝난 뒤 작성 창을 닫아 주세요.');
    expect(confirm).not.toHaveBeenCalled();
    expect(router.state.location.pathname).toBe('/flow');
    expect(router.state.location.search).toBe('?compose=1&quote=quote-1');
    expect(screen.getByText('보존해야 할 인용 원문')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '다른 화면' })).not.toBeInTheDocument();
  });
});
