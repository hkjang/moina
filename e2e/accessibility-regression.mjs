import assert from 'node:assert/strict';
import { access, mkdir, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const axePath = resolve(root, 'e2e/node_modules/axe-core/axe.min.js');
const resultDirectory = resolve(process.env.MOINA_E2E_OUTPUT || join(root, 'e2e/test-results'));
const resultPath = join(resultDirectory, 'accessibility-regression.json');
const failurePath = join(resultDirectory, 'accessibility-regression-failure.png');
const baseURL = new URL(process.env.MOINA_E2E_BASE_URL || 'http://127.0.0.1:8080');
const username = process.env.MOINA_E2E_USERNAME || 'e2e-admin';
const password = process.env.MOINA_E2E_PASSWORD;
const headless = process.env.MOINA_E2E_HEADLESS !== '0';

const defaultCoreRoutes = [
  { slug: 'flow', path: '/flow' },
  { slug: 'search', path: '/search' },
  { slug: 'notifications', path: '/notifications' },
  { slug: 'moims', path: '/moims' },
  { slug: 'settings-notifications', path: '/settings/notifications' },
  { slug: 'settings-accessibility', path: '/settings/accessibility' },
  { slug: 'ai', path: '/ai' },
  { slug: 'admin-dashboard', path: '/admin' },
  { slug: 'admin-settings', path: '/admin/settings' },
];

function routesFromEnvironment() {
  const raw = process.env.MOINA_E2E_A11Y_ROUTES_JSON?.trim();
  if (!raw) return defaultCoreRoutes;
  const parsed = JSON.parse(raw);
  if (!Array.isArray(parsed) || parsed.length === 0) throw new Error('MOINA_E2E_A11Y_ROUTES_JSON은 비어 있지 않은 배열이어야 합니다.');
  return parsed.map((route, index) => {
    const item = typeof route === 'string' ? { path: route, slug: route.replace(/^\/+/, '').replaceAll('/', '-') || `route-${index}` } : route;
    if (!item || typeof item.path !== 'string' || !item.path.startsWith('/') || typeof item.slug !== 'string') {
      throw new Error('접근성 route에는 /로 시작하는 path와 slug가 필요합니다.');
    }
    return item;
  });
}

if (!password) throw new Error('MOINA_E2E_PASSWORD가 필요합니다.');
await access(axePath);
await mkdir(resultDirectory, { recursive: true });

const routes = routesFromEnvironment();
const matrices = [
  { theme: 'light', viewport: 'desktop', width: 1440, height: 1000 },
  { theme: 'light', viewport: 'mobile', width: 390, height: 844 },
  { theme: 'dark', viewport: 'desktop', width: 1440, height: 1000 },
  { theme: 'dark', viewport: 'mobile', width: 390, height: 844 },
];
const scans = [];
const keyboardChecks = [];
const runtimeFailures = [];
const ignoredRequestAborts = [];
let phase = 'startup';

function monitor(page) {
  page.on('pageerror', (error) => runtimeFailures.push(`[${phase}] ${error.stack || error.message}`));
  page.on('requestfailed', (request) => {
    const reason = request.failure()?.errorText || '실패';
    const value = `[${phase}] ${request.method()} ${request.url()} (${reason})`;
    if (reason === 'net::ERR_ABORTED') ignoredRequestAborts.push(value);
    else runtimeFailures.push(value);
  });
  page.on('response', (response) => {
    if (response.status() >= 500) runtimeFailures.push(`[${phase}] ${response.status()} ${response.url()}`);
  });
}

async function settle(page) {
  await page.waitForLoadState('domcontentloaded');
  await page.waitForLoadState('networkidle', { timeout: 20_000 });
  await page.waitForTimeout(100);
}

async function login(page) {
  phase = 'login';
  await page.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: '로그인', exact: true }).waitFor({ state: 'visible' });
  await page.getByLabel(/사용자 이름|아이디/).fill(username);
  await page.getByLabel('비밀번호').fill(password);
  await Promise.all([
    page.waitForURL((url) => url.pathname !== '/login', { timeout: 15_000 }),
    page.getByRole('button', { name: '로그인', exact: true }).click(),
  ]);
  await settle(page);
}

async function apiJSON(context, method, path, body) {
  const unsafe = !['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase());
  const csrf = unsafe ? (await context.cookies(baseURL.toString())).find((cookie) => cookie.name === 'moina_csrf') : undefined;
  if (unsafe && !csrf?.value) throw new Error(`${method} ${path}: CSRF cookie가 없습니다.`);
  const response = await context.request.fetch(new URL(`/api/v1${path}`, baseURL).toString(), {
    method,
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(csrf ? { 'X-CSRF-Token': csrf.value } : {}),
    },
    ...(body === undefined ? {} : { data: body }),
  });
  const payload = await response.json().catch(() => null);
  if (!response.ok()) throw new Error(`${method} ${path}가 ${response.status()}로 실패했습니다: ${JSON.stringify(payload)}`);
  return payload?.data ?? payload;
}

async function persistTheme(context, page, theme) {
  const current = await apiJSON(context, 'GET', '/profile/preferences');
  const preferences = current && typeof current === 'object' ? current : {};
  const appearance = preferences.appearance && typeof preferences.appearance === 'object' ? preferences.appearance : {};
  await apiJSON(context, 'PUT', '/profile/preferences', {
    ...preferences,
    appearance: { ...appearance, theme, reduceMotion: true },
  });
  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await page.waitForFunction((expected) => document.documentElement.dataset.theme === expected, theme);
  await settle(page);
}

async function assertRouteReady(page, route) {
  await settle(page);
  assert.equal(new URL(page.url()).pathname, route.path, `${route.path} 경로가 유지되어야 합니다.`);
  const heading = page.locator('h1');
  await heading.first().waitFor({ state: 'visible', timeout: 15_000 });
  assert.equal(await heading.count(), 1, `${route.path}에는 h1이 정확히 하나여야 합니다.`);
  assert.equal(await page.locator('[data-error-state], .error-state, [data-login-page], .login-page').count(), 0, `${route.path}에 오류 또는 로그인 상태가 표시됐습니다.`);
  const overflow = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth: window.innerWidth }));
  assert.ok(overflow.scrollWidth <= overflow.innerWidth + 1, `${route.path}에 가로 overflow가 있습니다(${overflow.scrollWidth}>${overflow.innerWidth}).`);
}

function compactViolations(violations) {
  return violations.map((violation) => ({
    id: violation.id,
    impact: violation.impact,
    description: violation.description,
    help: violation.help,
    helpUrl: violation.helpUrl,
    nodes: violation.nodes.slice(0, 5).map((node) => ({
      target: node.target,
      html: node.html.slice(0, 500),
      failureSummary: node.failureSummary,
    })),
  }));
}

async function scanWithAxe(page) {
  await page.addScriptTag({ path: axePath });
  return page.evaluate(async () => {
    const result = await window.axe.run(document, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
      resultTypes: ['violations'],
    });
    return result.violations;
  });
}

async function assertFocusInside(page, selector, message) {
  assert.equal(await page.evaluate((value) => {
    const container = document.querySelector(value);
    return container instanceof HTMLElement && document.activeElement instanceof HTMLElement && container.contains(document.activeElement);
  }, selector), true, message);
}

async function runKeyboardChecks(context, page) {
  phase = 'keyboard:desktop';
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce', forcedColors: 'none' });
  await persistTheme(context, page, 'light');

  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await assertRouteReady(page, { path: '/flow' });
  await page.evaluate(() => {
    document.body.tabIndex = -1;
    document.body.focus({ preventScroll: true });
    document.body.removeAttribute('tabindex');
  });
  await page.keyboard.press('Tab');
  assert.equal(await page.evaluate(() => document.activeElement?.classList.contains('skip-link')), true, '첫 Tab에서 본문 바로가기 링크에 포커스가 가야 합니다.');
  await page.keyboard.press('Enter');
  assert.equal(await page.evaluate(() => document.activeElement?.id), 'main-content', '본문 바로가기는 main-content로 포커스를 옮겨야 합니다.');
  keyboardChecks.push('skip-link');

  await page.keyboard.press('Control+K');
  const quickNavigationDialog = page.getByRole('dialog', { name: '빠른 이동' });
  await quickNavigationDialog.waitFor({ state: 'visible' });
  const quickNavigationInput = quickNavigationDialog.getByRole('combobox', { name: '빠른 이동 검색' });
  await page.waitForFunction((element) => document.activeElement === element, await quickNavigationInput.elementHandle(), { timeout: 5_000 });
  await quickNavigationInput.fill('포켓');
  await page.keyboard.press('ArrowDown');
  const activeOption = await quickNavigationInput.getAttribute('aria-activedescendant');
  assert.ok(activeOption, '방향키 이동 시 빠른 이동의 활성 결과가 지정되어야 합니다.');
  assert.match(await page.locator(`#${activeOption}`).innerText(), /포켓/, '방향키로 포켓 결과를 선택해야 합니다.');
  for (let index = 0; index < 4; index += 1) {
    await page.keyboard.press('Tab');
    await assertFocusInside(page, '.quick-navigation-dialog', '빠른 이동 Tab 순환 중 포커스가 Dialog 밖으로 빠지면 안 됩니다.');
  }
  await page.keyboard.press('Escape');
  await quickNavigationDialog.waitFor({ state: 'hidden' });
  assert.equal(await page.evaluate(() => document.activeElement?.id), 'main-content', '빠른 이동이 닫히면 원래 본문 포커스로 돌아가야 합니다.');
  keyboardChecks.push('quick-navigation');

  const profileTrigger = page.locator('button[aria-label="프로필 메뉴"]:visible').last();
  await profileTrigger.focus();
  await page.keyboard.press('Enter');
  const profileMenu = page.locator('[role="menu"].profile-popover');
  await profileMenu.waitFor({ state: 'visible' });
  await assertFocusInside(page, '.profile-popover', '프로필 메뉴가 열리면 포커스가 메뉴 안에 있어야 합니다.');
  await page.keyboard.press('ArrowDown');
  await assertFocusInside(page, '.profile-popover', '방향키 탐색 중 포커스가 프로필 메뉴 안에 유지되어야 합니다.');
  await page.keyboard.press('Escape');
  await profileMenu.waitFor({ state: 'hidden' });
  assert.equal(await profileTrigger.evaluate((element) => document.activeElement === element), true, '프로필 메뉴가 닫히면 Trigger로 포커스가 돌아가야 합니다.');
  keyboardChecks.push('profile-menu');

  const composerTrigger = page.locator('.composer-prompt');
  await composerTrigger.focus();
  await page.keyboard.press('Enter');
  const composerDialog = page.getByRole('dialog', { name: '새 모인' });
  await composerDialog.waitFor({ state: 'visible' });
  const composerTabs = await composerDialog.locator('button:not(:disabled), input:not(:disabled):not([type="hidden"]), textarea:not(:disabled), select:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])').count();
  assert.ok(composerTabs > 1, 'Moin 작성 Dialog에 키보드로 사용할 Control이 있어야 합니다.');
  for (let index = 0; index < composerTabs + 2; index += 1) {
    await page.keyboard.press('Tab');
    await assertFocusInside(page, '[role="dialog"]', 'Tab 순환 중 포커스가 Moin 작성 Dialog 밖으로 빠지면 안 됩니다.');
  }
  await page.keyboard.press('Escape');
  await composerDialog.waitFor({ state: 'hidden' });
  await page.waitForFunction((element) => document.activeElement === element, await composerTrigger.elementHandle(), { timeout: 5_000 });
  await page.waitForTimeout(100);
  assert.equal(await composerTrigger.evaluate((element) => document.activeElement === element), true, 'Moin 작성 Dialog가 닫히면 Trigger로 포커스가 돌아가야 합니다.');
  keyboardChecks.push('composer-focus-trap');

  phase = 'keyboard:mobile-navigation';
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(new URL('/admin', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await assertRouteReady(page, { path: '/admin' });
  const menuTrigger = page.getByRole('button', { name: '메뉴 열기', exact: true });
  await menuTrigger.focus();
  await page.keyboard.press('Enter');
  const mobileDialog = page.getByRole('dialog', { name: '주 메뉴' });
  await mobileDialog.waitFor({ state: 'visible' });
  const mobileTabs = await mobileDialog.locator('button:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])').count();
  assert.ok(mobileTabs > 1, '모바일 메뉴에 키보드로 탐색할 항목이 있어야 합니다.');
  for (let index = 0; index < mobileTabs + 2; index += 1) {
    await page.keyboard.press('Tab');
    await assertFocusInside(page, '.mobile-nav-panel', 'Tab 순환 중 포커스가 모바일 메뉴 밖으로 빠지면 안 됩니다.');
  }
  await page.keyboard.press('Escape');
  await mobileDialog.waitFor({ state: 'hidden' });
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === '메뉴 열기', undefined, { timeout: 5_000 });
  assert.equal(await menuTrigger.evaluate((element) => document.activeElement === element), true, '모바일 메뉴가 닫히면 Trigger로 포커스가 돌아가야 합니다.');
  keyboardChecks.push('mobile-menu-focus-trap');
}

async function runMediaAndReflowChecks(page) {
  phase = 'media:reduced-motion';
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce', forcedColors: 'none' });
  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await assertRouteReady(page, { path: '/flow' });
  const reduced = await page.evaluate(() => {
    const probe = document.createElement('span');
    probe.className = 'spin';
    document.body.append(probe);
    const style = getComputedStyle(probe);
    const duration = style.animationDuration.trim();
    const milliseconds = duration.endsWith('ms') ? Number.parseFloat(duration) : Number.parseFloat(duration) * 1_000;
    const value = { matches: matchMedia('(prefers-reduced-motion: reduce)').matches, duration, milliseconds, iterations: style.animationIterationCount };
    probe.remove();
    return value;
  });
  assert.equal(reduced.matches, true, '모션 축소 Media Query가 활성화되어야 합니다.');
  assert.ok(reduced.milliseconds <= 0.1, `모션 축소 시 Animation이 사실상 정지해야 합니다(${reduced.duration}).`);
  assert.equal(reduced.iterations, '1', '모션 축소 시 반복 Animation은 한 번만 실행되어야 합니다.');
  keyboardChecks.push('reduced-motion');

  phase = 'media:forced-colors';
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce', forcedColors: 'active' });
  await page.goto(new URL('/settings/accessibility', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await assertRouteReady(page, { path: '/settings/accessibility' });
  const forced = await page.evaluate(() => {
    const controls = [...document.querySelectorAll('.ui-button, input, textarea, select')].filter((element) => element instanceof HTMLElement && element.offsetParent !== null);
    const bordered = controls.map((element) => {
      const style = getComputedStyle(element);
      return {
        target: `${element.tagName.toLowerCase()}${element.id ? `#${element.id}` : ''}${element.className ? `.${String(element.className).trim().replace(/\s+/g, '.')}` : ''}`,
        visible: Number.parseFloat(style.borderTopWidth) >= 1 && style.borderTopStyle !== 'none',
        border: `${style.borderTopWidth} ${style.borderTopStyle}`,
      };
    });
    const target = controls.find((element) => element instanceof HTMLElement);
    if (target instanceof HTMLElement) target.focus();
    const focused = document.activeElement instanceof HTMLElement ? getComputedStyle(document.activeElement) : null;
    return {
      matches: matchMedia('(forced-colors: active)').matches,
      controlCount: controls.length,
      allBordered: bordered.every((item) => item.visible),
      missingBorders: bordered.filter((item) => !item.visible),
      outlineWidth: focused ? Number.parseFloat(focused.outlineWidth) : 0,
      outlineStyle: focused?.outlineStyle || 'none',
    };
  });
  assert.equal(forced.matches, true, '강제 색상 Media Query가 활성화되어야 합니다.');
  assert.ok(forced.controlCount > 1, '강제 색상에서 검증할 Control이 있어야 합니다.');
  assert.equal(forced.allBordered, true, `강제 색상에서 주요 Control 경계가 보여야 합니다: ${JSON.stringify(forced.missingBorders)}`);
  assert.ok(forced.outlineWidth >= 3 && forced.outlineStyle !== 'none', '강제 색상에서 3px Focus Ring이 보여야 합니다.');
  keyboardChecks.push('forced-colors');

  phase = 'zoom:200-percent-reflow';
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce', forcedColors: 'none' });
  // 1440px 화면을 브라우저 200% 확대했을 때의 CSS viewport(720px)를 사용해 Reflow를 검증합니다.
  await page.setViewportSize({ width: 720, height: 500 });
  for (const path of ['/flow', '/settings/notifications', '/admin']) {
    await page.goto(new URL(path, baseURL).toString(), { waitUntil: 'domcontentloaded' });
    await assertRouteReady(page, { path });
  }
  keyboardChecks.push('zoom-200-reflow');

  phase = 'screen-reader-live-regions';
  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await assertRouteReady(page, { path: '/flow' });
  assert.equal(await page.locator('.toast-region[role="region"][aria-live="polite"][aria-label]').count(), 1, '실시간 알림 Toast에는 이름 있는 polite Live Region이 있어야 합니다.');
  await page.goto(new URL('/ai', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await assertRouteReady(page, { path: '/ai' });
  if (await page.getByLabel('AI 질문').count()) {
    assert.equal(await page.locator('.sr-only[aria-live="polite"]').count(), 1, 'AI 스트리밍 상태에는 Screen Reader Live Region이 있어야 합니다.');
  }
  keyboardChecks.push('live-regions');
}

const browser = await chromium.launch({ headless });
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
  colorScheme: 'light',
  locale: 'ko-KR',
  timezoneId: 'Asia/Seoul',
  reducedMotion: 'reduce',
  bypassCSP: true,
});
const page = await context.newPage();
monitor(page);

try {
  await login(page);
  for (const matrix of matrices) {
    phase = `theme:${matrix.theme}:${matrix.viewport}`;
    await page.setViewportSize({ width: matrix.width, height: matrix.height });
    await page.emulateMedia({ colorScheme: matrix.theme, reducedMotion: 'reduce', forcedColors: 'none' });
    await persistTheme(context, page, matrix.theme);

    for (const route of routes) {
      phase = `axe:${matrix.theme}:${matrix.viewport}:${route.path}`;
      await page.goto(new URL(route.path, baseURL).toString(), { waitUntil: 'domcontentloaded' });
      await assertRouteReady(page, route);
      const violations = compactViolations(await scanWithAxe(page));
      scans.push({ ...matrix, route: route.path, violations });
    }
  }

  await runKeyboardChecks(context, page);
  await runMediaAndReflowChecks(page);

  const blocking = scans.flatMap((scan) => scan.violations
    .filter((violation) => ['serious', 'critical'].includes(violation.impact))
    .map((violation) => ({ theme: scan.theme, viewport: scan.viewport, route: scan.route, ...violation })));
  await writeFile(resultPath, `${JSON.stringify({ ok: blocking.length === 0 && runtimeFailures.length === 0, matrices, routes, scans, keyboardChecks, blocking, runtimeFailures, ignoredRequestAborts }, null, 2)}\n`);
  assert.equal(blocking.length, 0, `Axe Serious/Critical 위반이 있습니다:\n${blocking.map((item) => `${item.route} (${item.theme}/${item.viewport}) ${item.id}: ${item.nodes.map((node) => node.target.join(' ')).join(', ')}`).join('\n')}`);
  assert.equal(runtimeFailures.length, 0, `접근성 검사 중 브라우저 런타임 오류가 있습니다:\n${runtimeFailures.join('\n')}`);
  console.log(`접근성 회귀 검사 통과: ${scans.length}개 Axe DOM scan, ${keyboardChecks.length}개 Keyboard/Media/Reflow 검증`);
} catch (error) {
  await page.screenshot({ path: failurePath, fullPage: true }).catch(() => undefined);
  await writeFile(resultPath, `${JSON.stringify({ ok: false, phase, matrices, routes, scans, keyboardChecks, runtimeFailures, ignoredRequestAborts, error: error instanceof Error ? error.stack : String(error) }, null, 2)}\n`).catch(() => undefined);
  throw error;
} finally {
  await context.close();
  await browser.close();
}
