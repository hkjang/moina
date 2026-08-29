import * as Dialog from '@radix-ui/react-dialog';
import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from 'react';
import { AlertTriangle, Check, LoaderCircle, X } from 'lucide-react';
import { cn } from '../lib/cn';

export function Button({ variant = 'secondary', size = 'default', className, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'ghost' | 'danger'; size?: 'default' | 'small' | 'icon' }) {
  return <button className={cn('ui-button', `ui-button-${variant}`, `ui-button-${size}`, className)} {...props}>{children}</button>;
}

export function IconButton({ label, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; children: ReactNode }) {
  return <Button size="icon" variant="ghost" aria-label={label} title={label} {...props}>{children}</Button>;
}

export function Card({ className, children, ...props }: HTMLAttributes<HTMLElement>) {
  return <section className={cn('surface-card', className)} {...props}>{children}</section>;
}

export function PageHeader({ title, description, actions, eyebrow }: { title: string; description?: string; actions?: ReactNode; eyebrow?: string }) {
  return <header className="page-header">
    <div>{eyebrow && <p className="page-eyebrow">{eyebrow}</p>}<h1>{title}</h1>{description && <p>{description}</p>}</div>
    {actions && <div className="page-actions">{actions}</div>}
  </header>;
}

export function SectionHeader({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return <div className="section-header"><div><h2>{title}</h2>{description && <p>{description}</p>}</div>{action}</div>;
}

export function Avatar({ name, src, size = 'default' }: { name: string; src?: string; size?: 'small' | 'default' | 'large' }) {
  const label = name.trim() || '사용자';
  return <span className={cn('avatar', `avatar-${size}`)} aria-hidden="true">{src ? <img src={src} alt="" /> : label.slice(0, 2).toUpperCase()}</span>;
}

export function LoadingState({ label = '불러오는 중입니다.' }: { label?: string }) {
  return <div className="state-panel" role="status"><LoaderCircle className="spin" aria-hidden="true"/><span>{label}</span></div>;
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: ReactNode }) {
  return <div className="state-panel empty-state"><span className="state-icon"><Check aria-hidden="true"/></span><strong>{title}</strong><p>{description}</p>{action}</div>;
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return <div className="state-panel error-state" role="alert"><span className="state-icon"><AlertTriangle aria-hidden="true"/></span><strong>화면을 불러오지 못했습니다.</strong><p>{message}</p>{onRetry && <Button onClick={onRetry}>다시 시도</Button>}</div>;
}

export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'brand' | 'positive' | 'warning' | 'danger' }) {
  return <span className={cn('badge', `badge-${tone}`)}>{children}</span>;
}

export function Field({ label, help, error, children }: { label: string; help?: string; error?: string; children: ReactNode }) {
  return <label className="field"><span className="field-label">{label}</span>{children}{help && <small>{help}</small>}{error && <small className="field-error" role="alert">{error}</small>}</label>;
}

export function SwitchField({ label, description, checked, onChange, disabled }: { label: string; description: string; checked: boolean; onChange: (checked: boolean) => void; disabled?: boolean }) {
  return <label className="switch-field"><span><strong>{label}</strong><small>{description}</small></span><input type="checkbox" role="switch" checked={checked} onChange={(event) => onChange(event.target.checked)} disabled={disabled}/></label>;
}

export function Modal({ open, onOpenChange, title, description, children }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; description?: string; children: ReactNode }) {
  return <Dialog.Root open={open} onOpenChange={onOpenChange}>
    <Dialog.Portal>
      <Dialog.Overlay className="dialog-overlay"/>
      <Dialog.Content className="dialog-content custom-scrollbar">
        <div className="dialog-heading"><div><Dialog.Title>{title}</Dialog.Title>{description && <Dialog.Description>{description}</Dialog.Description>}</div><Dialog.Close asChild><IconButton label="창 닫기"><X/></IconButton></Dialog.Close></div>
        {children}
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>;
}

export function Tabs({ value, items, onChange, label }: { value: string; items: Array<{ value: string; label: string }>; onChange: (value: string) => void; label: string }) {
  return <div className="tabs custom-scrollbar" role="tablist" aria-label={label}>{items.map((item) => <button type="button" role="tab" aria-selected={value === item.value} className={value === item.value ? 'active' : ''} key={item.value} onClick={() => onChange(item.value)}>{item.label}</button>)}</div>;
}

export function SkeletonFeed() {
  return <div className="skeleton-stack" aria-label="피드를 불러오는 중" role="status">{[0, 1, 2].map((item) => <div className="skeleton-card" key={item}><span/><div><i/><i/><i/></div></div>)}</div>;
}
