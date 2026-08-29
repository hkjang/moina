import assert from 'node:assert/strict';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { routeCatalogFromEnvironment } from './routes.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = resolve(process.env.MOINA_CAPTURE_OUTPUT || join(root, 'dist/screenshots-png'));
const baseURL = new URL(process.env.MOINA_CAPTURE_BASE_URL || 'http://127.0.0.1:18080');
const username = process.env.MOINA_CAPTURE_USERNAME || 'capture-admin';
const password = process.env.MOINA_CAPTURE_PASSWORD;
const version = process.env.MOINA_CAPTURE_VERSION || 'v0.1.2';
const staticRoutes = routeCatalogFromEnvironment();
let routes = staticRoutes;
const loopback = ['127.0.0.1', 'localhost', '[::1]'].includes(baseURL.hostname);
const generatedRoot = resolve(root, 'dist');

if (!password) throw new Error('MOINA_CAPTURE_PASSWORD가 필요합니다.');
if (!loopback) throw new Error('실제 화면 캡처는 개인정보 보호를 위해 localhost의 전용 데이터베이스에서만 실행합니다.');
if (output === generatedRoot || !output.startsWith(`${generatedRoot}${sep}`)) throw new Error('캡처 PNG 출력은 프로젝트 dist 하위의 전용 디렉터리여야 합니다.');
await rm(output, { recursive: true, force: true });
await mkdir(output, { recursive: true });

const viewports = [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
];
const themes = [
  { name: 'light', label: '밝은' },
  { name: 'dark', label: '어두운' },
];
const screenshots = [];
const failures = { console: [], page: [], request: [], response: [], external: [] };
const ignored = [];
let phase = 'startup';

const push = (kind, value) => { if (!failures[kind].includes(value)) failures[kind].push(value); };
const failureSummary = () => Object.entries(failures).filter(([, values]) => values.length)
  .map(([kind, values]) => `${kind}: ${values.join(' | ')}`).join('\n');

function monitor(page) {
  page.on('console', (message) => {
    if (message.type() !== 'error') return;
    const value = `[${phase}] ${message.text()}`;
    if (phase.startsWith('login') && /401|Unauthorized/i.test(value)) ignored.push(value);
    else push('console', value);
  });
  page.on('pageerror', (error) => push('page', `[${phase}] ${error.stack || error.message}`));
  page.on('request', (request) => {
    const target = new URL(request.url());
    if (['http:', 'https:'].includes(target.protocol) && target.origin !== baseURL.origin) push('external', `[${phase}] ${target}`);
  });
  page.on('requestfailed', (request) => push('request', `[${phase}] ${request.url()} ${request.failure()?.errorText || ''}`));
  page.on('response', (response) => { if (response.status() >= 500) push('response', `[${phase}] ${response.status()} ${response.url()}`); });
}

async function settle(page) {
  await page.waitForLoadState('networkidle', { timeout: 20_000 });
  await page.waitForTimeout(160);
  await page.evaluate(() => window.scrollTo(0, 0));
}

function themedSlug(theme, base) {
  return theme.name === 'light' ? base : `${theme.name}-${base}`;
}

async function assertTheme(page, theme, label, { stored = true } = {}) {
  await page.emulateMedia({ colorScheme: theme.name });
  const result = await page.evaluate(() => {
    const root = document.documentElement;
    const style = getComputedStyle(root);
    const parseValue = (value) => {
      const normalized = value.trim();
      const hex = normalized.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i)?.[1];
      if (hex) {
        const expanded = hex.length === 3 ? [...hex].map((digit) => `${digit}${digit}`).join('') : hex;
        return [0, 2, 4].map((offset) => Number.parseInt(expanded.slice(offset, offset + 2), 16));
      }
      const match = normalized.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i);
      return match ? match.slice(1, 4).map(Number) : null;
    };
    const parse = (name) => {
      const probe = document.createElement('span');
      probe.style.color = `var(${name})`;
      root.append(probe);
      const value = getComputedStyle(probe).color;
      probe.remove();
      return parseValue(value);
    };
    const luminance = (rgb) => {
      const channels = rgb.map((value) => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
    };
    const contrast = (left, right) => {
      if (!left || !right) return 0;
      const values = [luminance(left), luminance(right)].sort((a, b) => b - a);
      return (values[0] + 0.05) / (values[1] + 0.05);
    };
    const identity = parse('--brand-identity');
    const fill = parse('--brand-fill');
    const foreground = parse('--brand-fg');
    const onBrand = parse('--on-brand');
    const background = parse('--bg');
    const surface = parse('--surface');
    const positive = parse('--positive');
    const focusRing = parse('--focus-ring');
    const fixture = document.createElement('div');
    fixture.setAttribute('aria-hidden', 'true');
    fixture.style.cssText = 'position:fixed;left:-10000px;top:0;pointer-events:none';
    fixture.innerHTML = '<button class="ui-button ui-button-danger">삭제</button><button class="ui-button ui-button-secondary">취소</button><input aria-label="대비 검사 입력"><button class="oidc-button">조직 계정</button><div class="search-hero"><input aria-label="검색 대비 검사"></div><div class="chat-composer"><textarea aria-label="AI 작성 대비 검사"></textarea></div><div class="ai-welcome"><div><button>AI 제안</button></div></div><span class="offline-note">오프라인 안내</span>';
    document.body.append(fixture);
    const dangerStyle = getComputedStyle(fixture.children[0]);
    const secondaryStyle = getComputedStyle(fixture.children[1]);
    const inputStyle = getComputedStyle(fixture.children[2]);
    const oidcStyle = getComputedStyle(fixture.children[3]);
    const searchStyle = getComputedStyle(fixture.children[4]);
    const composerStyle = getComputedStyle(fixture.children[5]);
    const aiSuggestionStyle = getComputedStyle(fixture.children[6].querySelector('button'));
    const heroNoteStyle = getComputedStyle(fixture.children[7]);
    const componentContrast = {
      danger: contrast(parseValue(dangerStyle.color), parseValue(dangerStyle.backgroundColor)),
      secondaryBorder: contrast(parseValue(secondaryStyle.borderTopColor), parseValue(secondaryStyle.backgroundColor)),
      inputBorder: contrast(parseValue(inputStyle.borderTopColor), parseValue(inputStyle.backgroundColor)),
      oidcBorder: contrast(parseValue(oidcStyle.borderTopColor), parseValue(oidcStyle.backgroundColor)),
      searchBorder: contrast(parseValue(searchStyle.borderTopColor), parseValue(searchStyle.backgroundColor)),
      composerBorder: contrast(parseValue(composerStyle.borderTopColor), parseValue(composerStyle.backgroundColor)),
      aiSuggestionBorder: contrast(parseValue(aiSuggestionStyle.borderTopColor), parseValue(aiSuggestionStyle.backgroundColor)),
      heroNote: contrast(parseValue(heroNoteStyle.color), parseValue(heroNoteStyle.backgroundColor)),
    };
    fixture.remove();
    return {
      theme: root.dataset.theme || null,
      colorScheme: style.colorScheme,
      identity,
      positive,
      fillContrast: contrast(fill, onBrand),
      linkContrast: contrast(foreground, surface),
      focusContrast: contrast(focusRing, background),
      componentContrast,
    };
  });
  if (stored) assert.equal(result.theme, theme.name, `${label}: 저장된 ${theme.name} 테마가 적용되지 않았습니다.`);
  assert.ok(result.colorScheme.includes(theme.name), `${label}: color-scheme이 ${theme.name}이 아닙니다.`);
  assert.deepEqual(result.identity?.map(Math.round), [230, 62, 35], `${label}: 브랜드 대표색이 #E63E23이 아닙니다.`);
  assert.notDeepEqual(result.positive?.map(Math.round), result.identity?.map(Math.round), `${label}: 성공 색상은 브랜드 색상과 분리되어야 합니다.`);
  assert.ok(result.fillContrast >= 4.5, `${label}: Primary CTA 대비가 4.5:1 미만입니다(${result.fillContrast.toFixed(2)}).`);
  assert.ok(result.linkContrast >= 4.5, `${label}: 링크 대비가 4.5:1 미만입니다(${result.linkContrast.toFixed(2)}).`);
  assert.ok(result.focusContrast >= 3, `${label}: Focus Ring 대비가 3:1 미만입니다(${result.focusContrast.toFixed(2)}).`);
  assert.ok(result.componentContrast.danger >= 4.5, `${label}: Danger Button 대비가 4.5:1 미만입니다(${result.componentContrast.danger.toFixed(2)}).`);
  assert.ok(result.componentContrast.secondaryBorder >= 3, `${label}: Secondary Button 경계 대비가 3:1 미만입니다(${result.componentContrast.secondaryBorder.toFixed(2)}).`);
  assert.ok(result.componentContrast.inputBorder >= 3, `${label}: 입력 경계 대비가 3:1 미만입니다(${result.componentContrast.inputBorder.toFixed(2)}).`);
  assert.ok(result.componentContrast.oidcBorder >= 3, `${label}: OIDC Button 경계 대비가 3:1 미만입니다(${result.componentContrast.oidcBorder.toFixed(2)}).`);
  assert.ok(result.componentContrast.searchBorder >= 3, `${label}: 검색 입력 영역 경계 대비가 3:1 미만입니다(${result.componentContrast.searchBorder.toFixed(2)}).`);
  assert.ok(result.componentContrast.composerBorder >= 3, `${label}: AI 작성 영역 경계 대비가 3:1 미만입니다(${result.componentContrast.composerBorder.toFixed(2)}).`);
  assert.ok(result.componentContrast.aiSuggestionBorder >= 3, `${label}: AI 제안 Button 경계 대비가 3:1 미만입니다(${result.componentContrast.aiSuggestionBorder.toFixed(2)}).`);
  assert.ok(result.componentContrast.heroNote >= 4.5, `${label}: 로그인 Hero 안내문 대비가 4.5:1 미만입니다(${result.componentContrast.heroNote.toFixed(2)}).`);
}

async function assertSafe(page, label) {
  const exposure = await page.evaluate(() => {
    const text = document.body?.innerText || '';
    const emails = text.match(/\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/gi) || [];
    const safeEmail = (email) => /@(example\.(com|org|net)|example\.internal|invalid|test)$/i.test(email);
    const values = Array.from(document.querySelectorAll('input')).filter((input) => {
      const descriptor = `${input.type} ${input.name} ${input.id} ${input.autocomplete}`.toLowerCase();
      return Boolean(input.value) && /(password|secret|token|api.?key|credential)/.test(descriptor);
    });
    return {
      secretInputs: values.length,
      privateKey: /-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(text),
      accessKey: /\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/.test(text),
      jwt: /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/.test(text),
      personalEmail: emails.some((email) => !safeEmail(email)),
    };
  });
  assert.deepEqual(exposure, { secretInputs: 0, privateKey: false, accessKey: false, jwt: false, personalEmail: false }, `${label}에서 비밀/개인정보 패턴이 발견됐습니다.`);
  const width = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, inner: window.innerWidth }));
  assert.ok(width.scroll <= width.inner + 1, `${label}에 가로 overflow가 있습니다.`);
  assert.equal(failureSummary(), '', `${label} 런타임 오류\n${failureSummary()}`);
}

async function apiJSON(context, method, path, body, allowed = []) {
  const unsafe = !['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase());
  const csrfCookie = unsafe ? (await context.cookies(baseURL.toString())).find((cookie) => cookie.name === 'moina_csrf') : undefined;
  if (unsafe && !csrfCookie?.value) throw new Error(`${method} ${path}: moina_csrf cookie가 없어 안전하게 seed할 수 없습니다.`);
  const response = await context.request.fetch(new URL(`/api/v1${path}`, baseURL).toString(), {
    method,
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(csrfCookie ? { 'X-CSRF-Token': csrfCookie.value } : {}),
    },
    ...(body === undefined ? {} : { data: body }),
  });
  const payload = await response.json().catch(() => null);
  if (!response.ok() && !allowed.includes(response.status())) {
    throw new Error(`${method} ${path}가 ${response.status()}로 실패했습니다: ${JSON.stringify(payload)}`);
  }
  return { status: response.status(), data: payload?.data ?? payload };
}

async function persistAndAssertTheme(context, page, theme, label) {
  const response = await apiJSON(context, 'GET', '/profile/preferences');
  const current = response.data && typeof response.data === 'object' ? response.data : {};
  const appearance = current.appearance && typeof current.appearance === 'object' ? current.appearance : {};
  const feed = current.feed && typeof current.feed === 'object' ? current.feed : {};
  const finalPreferences = {
    ...current,
    appearance: { fontScale: 112, reduceMotion: true, density: 'comfortable', ...appearance, theme: theme.name },
    feed: { ...feed, topicWeight: 40, linkWeight: 30, discoveryWeight: 20, recencyWeight: 10 },
  };
  // 샘플 Moin 생성 전에 만들어진 For Me snapshot을 공개 API 계약 안에서
  // 확실히 분리하고, 홍보용 계정에는 합계 100의 설명 가능한 가중치를 유지한다.
  await apiJSON(context, 'PUT', '/profile/preferences', finalPreferences);
  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await page.waitForFunction((themeName) => document.documentElement.dataset.theme === themeName, theme.name);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForFunction((themeName) => document.documentElement.dataset.theme === themeName, theme.name);
  await settle(page);
  await assertTheme(page, theme, `${label}:reload`);
}

async function seedDynamicRoutes(context) {
  const moimBody = {
    name: 'MOINA 연구소',
    slug: 'moina-capture',
    description: '실제 화면 캡처 전용 공개 Moim입니다.',
    visibility: 'public',
  };
  let moim = await apiJSON(context, 'POST', '/moims', moimBody, [409]);
  if (moim.status === 409) moim = await apiJSON(context, 'GET', `/moims/${moimBody.slug}`);
  assert.ok(moim.data?.id, '캡처용 Moim ID가 필요합니다.');
  const post = await apiJSON(context, 'POST', '/posts', {
    content: '사람과 생각이 지식으로 모이는 MOINA를 시작합니다. #MOINA #AI',
    visibility: 'public',
    mediaIds: [],
    moimId: moim.data.id,
  });
  assert.ok(post.data?.id, '캡처용 Moin ID가 필요합니다.');

  return [
    { slug: 'profile-bootstrap', path: `/profile/${encodeURIComponent(username)}`, title: `${username} 프로필`, dynamic: true },
    { slug: 'moin-detail', path: `/moin/${encodeURIComponent(post.data.id)}`, title: 'Moin 상세', dynamic: true },
    { slug: 'topic-moina', path: '/topics/moina', title: '#MOINA Topic', dynamic: true },
    { slug: 'moim-detail', path: `/moims/${encodeURIComponent(moim.data.slug || moimBody.slug)}`, title: 'MOINA 연구소 Moim', dynamic: true },
  ];
}

async function captureLoginScreenshot(browser, theme, viewport) {
  phase = `login:${theme.name}:${viewport.name}`;
  const loginContext = await browser.newContext({
    viewport: { width: viewport.width, height: viewport.height },
    colorScheme: theme.name,
    locale: 'ko-KR',
    timezoneId: 'Asia/Seoul',
    reducedMotion: 'reduce',
  });
  const loginPage = await loginContext.newPage();
  monitor(loginPage);
  try {
    await loginPage.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' });
    await loginPage.getByRole('heading', { name: '로그인', exact: true }).waitFor({ state: 'visible' });
    await settle(loginPage);
    await assertTheme(loginPage, theme, phase, { stored: false });
    await loginPage.getByText(new RegExp(`moina\\s+${version.replaceAll('.', '\\.')}|MOINA\\s+${version.replaceAll('.', '\\.')}`, 'i')).first().waitFor({ state: 'visible' });
    await assertSafe(loginPage, phase);
    const loginSlug = themedSlug(theme, `${viewport.name}-login`);
    await loginPage.screenshot({ path: join(output, `${loginSlug}.png`), fullPage: true, animations: 'disabled', caret: 'hide', scale: 'css' });
    screenshots.push({ slug: loginSlug, title: `${theme.label} 테마 ${viewport.name === 'desktop' ? '데스크톱' : '모바일'} 로그인`, route: '/login', viewport, theme: theme.name, fullPage: true });
  } finally {
    await loginContext.close();
  }
}

const browser = await chromium.launch({ headless: process.env.MOINA_CAPTURE_HEADLESS !== '0' });
const context = await browser.newContext({ colorScheme: 'light', locale: 'ko-KR', timezoneId: 'Asia/Seoul', reducedMotion: 'reduce' });
const page = await context.newPage();
monitor(page);

try {
  let routesSeeded = false;
  let authenticated = false;
  for (const theme of themes) {
    for (const viewport of viewports) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await captureLoginScreenshot(browser, theme, viewport);

      if (!authenticated) {
        phase = 'login:authenticate:capture-session';
        await page.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' });
        await page.getByLabel(/사용자 이름|아이디/).fill(username);
        await page.getByLabel('비밀번호').fill(password);
        await Promise.all([
          page.waitForURL((url) => url.pathname !== '/login', { timeout: 15_000 }),
          page.getByRole('button', { name: '로그인', exact: true }).click(),
        ]);
        authenticated = true;
        // 로그인 직후 생성된 빈 For Me snapshot이 seed 이후까지 남지 않도록
        // 초기 Flow 요청을 완전히 정리한 뒤 샘플 데이터를 만든다.
        await settle(page);
      }

      if (!routesSeeded) {
        routes = [...staticRoutes, ...(await seedDynamicRoutes(context))];
        routesSeeded = true;
      }
      phase = `theme-preflight:${theme.name}:${viewport.name}`;
      await persistAndAssertTheme(context, page, theme, phase);

      for (const route of routes) {
        phase = `capture:${theme.name}:${viewport.name}:${route.path}`;
        await page.goto(new URL(route.path, baseURL).toString(), { waitUntil: 'domcontentloaded' });
        await page.locator('h1').first().waitFor({ state: 'visible', timeout: 15_000 });
        await settle(page);
        await assertTheme(page, theme, phase);
        assert.equal(new URL(page.url()).pathname, route.path, `${route.path} 경로가 유지되어야 합니다.`);
        if (!route.state) assert.equal(await page.locator('[data-error-state], .error-state, [data-login-page], .login-page').count(), 0, `${route.path}에 오류/로그인 상태가 있습니다.`);
        await assertSafe(page, phase);
        const slug = themedSlug(theme, `${viewport.name}-${route.slug}`);
        await page.screenshot({ path: join(output, `${slug}.png`), fullPage: true, animations: 'disabled', caret: 'hide', scale: 'css' });
        screenshots.push({ slug, title: `${theme.label} 테마 ${viewport.name === 'desktop' ? '데스크톱' : '모바일'} ${route.title}`, route: route.path, viewport, theme: theme.name, fullPage: true });
      }

      phase = `capture:${theme.name}:${viewport.name}:profile-menu-version`;
      await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
      await settle(page);
      await assertTheme(page, theme, phase);
      await page.getByRole('button', { name: /프로필|내 계정|사용자 메뉴/ }).last().click();
      await page.getByText(new RegExp(`^moina\\s+${version.replaceAll('.', '\\.')}\$`, 'i')).waitFor({ state: 'visible' });
      await page.keyboard.press('ArrowDown');
      const menuFocus = await page.evaluate(() => {
        const element = document.activeElement;
        const popover = document.querySelector('.profile-popover');
        if (!(element instanceof HTMLElement) || !(popover instanceof HTMLElement)) return null;
        const style = getComputedStyle(element);
        const parse = (value) => {
          const match = value.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i);
          return match ? match.slice(1, 4).map(Number) : null;
        };
        const luminance = (rgb) => {
          const channels = rgb.map((value) => {
            const channel = value / 255;
            return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
          });
          return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
        };
        const foreground = parse(style.outlineColor);
        const background = parse(getComputedStyle(popover).backgroundColor);
        const ratio = foreground && background
          ? (Math.max(luminance(foreground), luminance(background)) + 0.05) / (Math.min(luminance(foreground), luminance(background)) + 0.05)
          : 0;
        return {
          isMenuItem: element.classList.contains('profile-menu-item'),
          width: Number.parseFloat(style.outlineWidth),
          style: style.outlineStyle,
          ratio,
        };
      });
      assert.ok(menuFocus?.isMenuItem, `${theme.label} ${viewport.name} 프로필 메뉴 항목에 키보드 포커스가 이동해야 합니다.`);
      assert.ok(menuFocus.width >= 3 && menuFocus.style !== 'none', `${theme.label} ${viewport.name} 프로필 메뉴에 3px Focus Ring이 보여야 합니다.`);
      assert.ok(menuFocus.ratio >= 3, `${theme.label} ${viewport.name} 프로필 메뉴 Focus Ring 대비가 3:1 이상이어야 합니다.`);
      const profileMenu = page.locator('.profile-popover');
      const profileBounds = await profileMenu.boundingBox();
      assert.ok(profileBounds, `${theme.label} ${viewport.name} 프로필 메뉴의 화면 위치를 확인할 수 있어야 합니다.`);
      assert.ok(profileBounds.x >= 0 && profileBounds.x + profileBounds.width <= viewport.width, `${theme.label} ${viewport.name} 프로필 메뉴 전체가 화면 너비 안에 있어야 합니다.`);
      assert.ok(profileBounds.y >= 0 && profileBounds.y + profileBounds.height <= viewport.height, `${theme.label} ${viewport.name} 프로필 메뉴 전체가 화면 높이 안에 있어야 합니다.`);
      await assertSafe(page, phase);
      const profileSlug = themedSlug(theme, `${viewport.name}-profile-menu-version`);
      await page.screenshot({ path: join(output, `${profileSlug}.png`), fullPage: false, animations: 'disabled', caret: 'hide', scale: 'css' });
      assert.equal(await profileMenu.isVisible(), true, `${theme.label} ${viewport.name} 프로필 메뉴가 캡처 중 열린 상태를 유지해야 합니다.`);
      screenshots.push({ slug: profileSlug, title: `${theme.label} 테마 ${viewport.name === 'desktop' ? '데스크톱' : '모바일'} 프로필 버전 메뉴`, route: '/flow', viewport, theme: theme.name, fullPage: false });

      // 로그인 화면은 별도 무세션 context로 캡처하고, 실제 route는 하나의 검증 세션을 재사용한다.
    }
  }

  const manifest = {
    schemaVersion: 2,
    product: 'moina',
    version,
    generatedAt: new Date().toISOString(),
    source: 'Playwright actual application capture',
    themes: themes.map(({ name }) => name),
    screenshots,
    skipped: [],
    runtimeFailures: failures,
    ignoredExpectedConsoleMessages: ignored,
  };
  await writeFile(join(output, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(`실제 화면 캡처 완료: ${screenshots.length}개 (${routes.length}개 route × ${themes.length} themes × ${viewports.length} viewports + 로그인/버전 메뉴)`);
} finally {
  await context.close();
  await browser.close();
}
