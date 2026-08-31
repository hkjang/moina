import '@testing-library/jest-dom/vitest';
import { transferableAbortController } from 'node:util';

// jsdom installs its own AbortController realm, while Node's built-in Request
// (undici) validates RequestInit.signal against Node's realm. Node 24 tightened
// that validation, so React Router navigation requests fail when they receive a
// jsdom AbortSignal. Keep Request and the abort primitives in the same realm in
// tests. Browser builds never load this setup file.
const nodeAbortController = transferableAbortController();
Object.defineProperties(globalThis, {
  AbortController: {
    configurable: true,
    writable: true,
    value: nodeAbortController.constructor,
  },
  AbortSignal: {
    configurable: true,
    writable: true,
    value: nodeAbortController.signal.constructor,
  },
});

if (!window.localStorage) {
  const values = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (key: string) => values.get(key) ?? null,
      key: (index: number) => [...values.keys()][index] ?? null,
      removeItem: (key: string) => { values.delete(key); },
      setItem: (key: string, value: string) => { values.set(key, String(value)); },
    },
  });
}
