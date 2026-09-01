import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ToastProvider } from '../components/ToastProvider';
import { ProfileSettingsPage } from './SettingsPages';

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(),
  refresh: vi.fn(),
  reload: vi.fn(),
  invalidate: vi.fn(),
}));

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  return { ...actual, apiRequest: mocks.apiRequest };
});

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    user: {
      id: 'user-1',
      username: 'moina-user',
      displayName: '모이나 사용자',
      email: 'user@example.com',
      avatarUrl: '/api/v1/media/avatar-old',
      roles: ['member'],
      permissions: ['posts:read', 'posts:write'],
    },
    refresh: mocks.refresh,
  }),
}));

vi.mock('../hooks/useApiQuery', () => ({
  useApiQuery: (path: string) => path === '/media/config'
    ? {
      data: { maxUploadBytes: 5 * 1024 * 1024, acceptedTypes: ['image/jpeg', 'image/png', 'image/gif', 'image/webp'] },
      loading: false,
      backgroundLoading: false,
      error: null,
      reload: mocks.reload,
    }
    : {
      data: {
        id: 'user-1',
        username: 'moina-user',
        displayName: '모이나 사용자',
        email: 'user@example.com',
        bio: '소개',
        avatarId: 'avatar-old',
        avatarUrl: '/api/v1/media/avatar-old',
      },
      loading: false,
      backgroundLoading: false,
      error: null,
      reload: mocks.reload,
    },
}));

vi.mock('../hooks/apiQueryClient', () => ({ invalidateApiQueries: mocks.invalidate }));

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/settings/profile']}>
      <ToastProvider><ProfileSettingsPage /></ToastProvider>
    </MemoryRouter>,
  );
}

function clipboardData(file: File) {
  return {
    items: [{ kind: 'file', type: file.type, getAsFile: () => file }],
    files: [file],
  };
}

describe('개인 프로필 이미지 설정', () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.refresh.mockReset().mockResolvedValue(undefined);
    mocks.reload.mockReset();
    mocks.invalidate.mockReset();
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:profile-preview'),
    });
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn(),
    });
    mocks.apiRequest.mockImplementation((path: string, options?: { method?: string; body?: unknown }) => {
      if (path === '/media' && options?.method === 'POST') return Promise.resolve({ id: 'avatar-new', url: '/api/v1/media/avatar-new' });
      if (path === '/profile' && options?.method === 'PATCH') {
        const body = options.body as Record<string, unknown>;
        return Promise.resolve({ ...body, id: 'user-1', username: 'moina-user', avatarUrl: body.avatarId ? '/api/v1/media/avatar-new' : '' });
      }
      return Promise.resolve(undefined);
    });
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('선택한 이미지를 미리 보고 업로드한 avatarId를 프로필에 저장한다', async () => {
    const page = renderPage();
    const image = new File(['profile image'], 'profile.png', { type: 'image/png' });

    fireEvent.change(screen.getByLabelText('프로필 이미지 파일'), { target: { files: [image] } });

    expect(await screen.findByText(/업로드 완료/)).toBeInTheDocument();
    const uploadCall = mocks.apiRequest.mock.calls.find(([path]) => path === '/media');
    expect(uploadCall?.[1]?.body).toBeInstanceOf(FormData);
    expect((uploadCall?.[1]?.body as FormData).get('file')).toBe(image);
    expect(page.container.querySelector('.profile-avatar-target img')).toHaveAttribute('src', '/api/v1/media/avatar-new');

    fireEvent.click(await screen.findByRole('button', { name: '프로필 저장' }));

    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith('/profile', expect.objectContaining({
      method: 'PATCH',
      body: expect.objectContaining({ avatarId: 'avatar-new' }),
    })));
    await waitFor(() => expect(mocks.refresh).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith('/media/avatar-old', { method: 'DELETE' }));
    expect(mocks.invalidate).toHaveBeenCalledWith(expect.arrayContaining(['/feed', '/profiles', '/notifications']));
  });

  it('캡처한 클립보드 이미지를 Ctrl+V로 업로드한다', async () => {
    renderPage();
    const image = new File(['clipboard image'], 'capture.png', { type: 'image/png' });

    fireEvent.paste(screen.getByRole('button', { name: '프로필 이미지 선택' }), { clipboardData: clipboardData(image) });

    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith('/media', expect.objectContaining({ method: 'POST' })));
    const uploadCall = mocks.apiRequest.mock.calls.find(([path]) => path === '/media');
    expect((uploadCall?.[1]?.body as FormData).get('file')).toBe(image);
  });

  it('기존 이미지를 제거하면 빈 avatarId를 저장한다', async () => {
    renderPage();

    fireEvent.click(screen.getByRole('button', { name: '이미지 제거' }));
    fireEvent.click(screen.getByRole('button', { name: '프로필 저장' }));

    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith('/profile', expect.objectContaining({
      method: 'PATCH',
      body: expect.objectContaining({ avatarId: '' }),
    })));
  });

  it('저장하지 않고 이동하면 새 임시 업로드를 정리한다', async () => {
    const page = renderPage();
    const image = new File(['profile image'], 'profile.png', { type: 'image/png' });
    fireEvent.change(screen.getByLabelText('프로필 이미지 파일'), { target: { files: [image] } });
    expect(await screen.findByText(/업로드 완료/)).toBeInTheDocument();

    page.unmount();

    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith('/media/avatar-new', { method: 'DELETE' }));
  });
});
