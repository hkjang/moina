import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { NotificationSettingsPage } from './SettingsPages';
import { ToastProvider } from '../components/ToastProvider';

const mocks = vi.hoisted(() => ({
  apiRequest: vi.fn(),
  reload: vi.fn(),
  preferences: {
    notifications: {
      inApp: { mentions: true, signals: true, follows: true, echoes: true, approvals: true },
      toast: { enabled: true },
      desktop: { enabled: false },
      digest: { mode: 'off' as const, time: '08:00' },
      quietHours: { enabled: false, start: '22:00', end: '07:00' },
    },
  },
}));

vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client');
  return { ...actual, apiRequest: mocks.apiRequest };
});

vi.mock('../hooks/useApiQuery', () => ({
  useApiQuery: () => ({
    data: mocks.preferences,
    loading: false,
    error: null,
    reload: mocks.reload,
  }),
}));

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/settings/notifications']}>
      <ToastProvider><NotificationSettingsPage /></ToastProvider>
    </MemoryRouter>,
  );
}

describe('알림 개인화 설정', () => {
  beforeEach(() => {
    mocks.apiRequest.mockReset();
    mocks.reload.mockReset();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('승인·보안 알림은 항상 켜진 비활성 control로 표시한다', () => {
    renderPage();
    const approval = screen.getByRole('switch', { name: /승인·보안 알림/ });
    expect(approval).toBeChecked();
    expect(approval).toBeDisabled();
  });

  it('사용자 동작으로 브라우저 권한을 받은 뒤 desktop 채널을 저장한다', async () => {
    const requestPermission = vi.fn().mockResolvedValue('granted');
    class MockNotification {
      static permission: NotificationPermission = 'default';
      static requestPermission = requestPermission;
    }
    vi.stubGlobal('Notification', MockNotification as unknown as typeof Notification);
    mocks.apiRequest.mockResolvedValue({});
    renderPage();

    fireEvent.click(screen.getByRole('switch', { name: /^데스크톱 알림/ }));
    await waitFor(() => expect(requestPermission).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole('switch', { name: /^데스크톱 알림/ })).toBeChecked());
    fireEvent.click(screen.getByRole('button', { name: /^저장$/ }));

    await waitFor(() => expect(mocks.apiRequest).toHaveBeenCalledWith('/profile/preferences', expect.objectContaining({
      method: 'PUT',
      body: expect.objectContaining({ notifications: expect.objectContaining({ desktop: { enabled: true } }) }),
    })));
  });
});
