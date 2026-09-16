import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import NotificationsPage from './NotificationsPage';
import { ToastProvider } from '../components/ToastProvider';

const mocks = vi.hoisted(() => ({
  items: [] as Array<Record<string, unknown>>,
}));

vi.mock('../hooks/useApiQuery', () => ({
  useApiQuery: () => ({ data: { items: mocks.items }, loading: false, error: null, reload: vi.fn() }),
}));

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/notifications']}>
      <ToastProvider><NotificationsPage /></ToastProvider>
    </MemoryRouter>,
  );
}

describe('NotificationsPage', () => {
  afterEach(() => { cleanup(); mocks.items = []; });

  it('shows an account-security notice with a shield and its settings link', () => {
    mocks.items = [{
      id: 'ntf_security', type: 'security', title: '계정 보안',
      body: "새 API·MCP 키 'ci'을(를) 만들었습니다. (요청 IP 203.0.113.5)",
      targetPath: '/settings/keys', createdAt: new Date().toISOString(),
    }];
    renderPage();
    const link = screen.getByRole('link', { name: /계정 보안/ });
    expect(link).toHaveAttribute('href', '/settings/keys');
    expect(link.querySelector('.notification-icon svg.lucide-shield-alert')).not.toBeNull();
    expect(screen.getByText(/요청 IP 203\.0\.113\.5/)).toBeInTheDocument();
  });

  it('keeps the bell for notification types it does not know', () => {
    mocks.items = [{ id: 'ntf_other', type: 'future_operational_event', title: '새 알림', createdAt: new Date().toISOString() }];
    renderPage();
    const link = screen.getByRole('link', { name: /새 알림/ });
    expect(link).toHaveAttribute('href', '/notifications');
    expect(link.querySelector('.notification-icon svg.lucide-bell')).not.toBeNull();
  });
});
