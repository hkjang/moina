import { API_BASE } from '../config';
import type { PublicConfig } from '../types';

// sessionStorage rather than localStorage: a silent attempt is scoped to this
// tab's browsing session, so a fresh tab tries again while a reload after a
// refusal does not.
const ATTEMPTED_KEY = 'moina.sso.silentAttempted';
const SIGNED_OUT_KEY = 'moina.sso.signedOut';

function readFlag(key: string): boolean {
  try {
    return window.sessionStorage.getItem(key) === 'true';
  } catch {
    // Private modes and blocked site data throw. Treating that as "already
    // attempted" is the safe answer: reading it as "not yet" would redirect on
    // every load and bounce the browser between the provider and the app.
    return true;
  }
}

function writeFlag(key: string, value: boolean) {
  try {
    if (value) window.sessionStorage.setItem(key, 'true');
    else window.sessionStorage.removeItem(key);
  } catch {
    /* readFlag already fails closed, so there is nothing more to protect. */
  }
}

/** Records that the user signed out on purpose, which suppresses auto-login. */
export function markSignedOut() {
  writeFlag(SIGNED_OUT_KEY, true);
  writeFlag(ATTEMPTED_KEY, true);
}

/** Clears the suppression once a session exists again. */
export function clearSilentSsoState() {
  writeFlag(SIGNED_OUT_KEY, false);
  writeFlag(ATTEMPTED_KEY, false);
}

export function silentSsoAttempted(): boolean {
  return readFlag(ATTEMPTED_KEY);
}

/**
 * Pages where a silent attempt must never start. The login page is where a
 * refused attempt lands, so trying again from there is the most common source
 * of a redirect loop; API, MCP and health paths are not browser pages at all.
 */
const EXCLUDED_PATHS = ['/login', '/api', '/auth', '/mcp', '/healthz', '/readyz', '/metrics'];
export function silentSsoEligiblePath(pathname: string): boolean {
  return !EXCLUDED_PATHS.some((prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`));
}

/**
 * Decides whether to try signing in without showing a login screen.
 *
 * It must never run more than once per browsing session: prompt=none either
 * answers immediately or redirects back with login_required, and retrying that
 * on every page load would bounce the browser in a loop. The local guards run
 * first so the server is only asked whether auto-login is on when an attempt
 * is actually possible.
 */
export async function shouldAttemptSilentSso(loadConfig: () => Promise<PublicConfig | undefined>, location: { pathname: string; search: string } = window.location): Promise<boolean> {
  if (!silentSsoEligiblePath(location.pathname)) return false;
  // The callback appends this marker when the provider had no session, so a
  // refusal is remembered even if sessionStorage was cleared in between.
  const sso = new URLSearchParams(location.search).get('sso');
  if (sso === 'none' || sso === 'error') return false;
  if (readFlag(SIGNED_OUT_KEY)) return false;
  if (readFlag(ATTEMPTED_KEY)) return false;
  const config = await loadConfig();
  return config?.oidc?.enabled === true && config.oidc.autoLogin === true;
}

/** Sends the browser to the provider for a silent attempt (top-level navigation, not an iframe). */
export function beginSilentSso(returnTo: string) {
  writeFlag(ATTEMPTED_KEY, true);
  const safe = returnTo.startsWith('/') && !returnTo.startsWith('//') ? returnTo : '/';
  window.location.assign(`${API_BASE}/auth/oidc/login?prompt=none&returnTo=${encodeURIComponent(safe)}`);
}
