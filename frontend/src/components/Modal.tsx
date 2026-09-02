import * as Dialog from '@radix-ui/react-dialog';
import type { ReactNode, RefObject } from 'react';
import { X } from 'lucide-react';
import { IconButton } from './ui';

// Modal lives apart from the other primitives because it is the only one
// that needs Radix. ToastProvider mounts eagerly and imports IconButton, so
// keeping the dialog here is what stops the Radix runtime from being pulled
// into the entry chunk and downloaded before the login screen paints.
export function Modal({ open, onOpenChange, title, description, children, restoreFocusRef }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; description?: string; children: ReactNode; restoreFocusRef?: RefObject<HTMLElement | null> }) {
  return <Dialog.Root open={open} onOpenChange={onOpenChange}>
    <Dialog.Portal>
      <Dialog.Overlay className="dialog-overlay"/>
      <Dialog.Content className="dialog-content custom-scrollbar" onCloseAutoFocus={(event) => { if (restoreFocusRef?.current) { event.preventDefault(); restoreFocusRef.current.focus(); } }}>
        <div className="dialog-heading"><div><Dialog.Title>{title}</Dialog.Title>{description && <Dialog.Description>{description}</Dialog.Description>}</div><Dialog.Close asChild><IconButton label="창 닫기"><X/></IconButton></Dialog.Close></div>
        {children}
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>;
}
