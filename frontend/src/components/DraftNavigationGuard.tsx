import { useEffect, useRef } from "react";
import { useBlocker } from "react-router-dom";
import { useToast } from "./ToastProvider";

interface DraftState {
  dirty: boolean;
  busy: boolean;
}

export function DraftNavigationGuard({
  stateRef,
  allowNavigationRef,
  busyMessage,
  confirmMessage,
  onProceed,
}: {
  stateRef: { current: DraftState };
  allowNavigationRef?: { current: boolean };
  busyMessage: string;
  confirmMessage: string;
  onProceed?: () => void;
}) {
  const { notify } = useToast();
  const handling = useRef(false);
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      !allowNavigationRef?.current &&
      (stateRef.current.dirty || stateRef.current.busy) &&
      (currentLocation.key !== nextLocation.key ||
        currentLocation.pathname !== nextLocation.pathname ||
        currentLocation.search !== nextLocation.search ||
        currentLocation.hash !== nextLocation.hash),
  );

  useEffect(() => {
    if (blocker.state !== "blocked") {
      handling.current = false;
      return;
    }
    if (handling.current) return;
    handling.current = true;
    if (stateRef.current.busy) {
      notify(busyMessage, "error");
      blocker.reset();
      return;
    }
    if (stateRef.current.dirty && !window.confirm(confirmMessage)) {
      blocker.reset();
      return;
    }
    onProceed?.();
    blocker.proceed();
  }, [blocker, busyMessage, confirmMessage, notify, onProceed, stateRef]);

  return null;
}
