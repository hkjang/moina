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
const version = process.env.MOINA_CAPTURE_VERSION || 'v0.1.0';
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

const browser = await chromium.launch({ headless: process.env.MOINA_CAPTURE_HEADLESS !== '0' });
const context = await browser.newContext({ colorScheme: 'light', locale: 'ko-KR', timezoneId: 'Asia/Seoul', reducedMotion: 'reduce' });
const page = await context.newPage();
monitor(page);

try {
  for (const viewport of viewports) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    phase = `login:${viewport.name}`;
    await page.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' });
    await page.getByRole('heading', { name: '로그인', exact: true }).waitFor({ state: 'visible' });
    await settle(page);
    await page.getByText(new RegExp(`moina\\s+${version.replaceAll('.', '\\.')}|MOINA\\s+${version.replaceAll('.', '\\.')}`, 'i')).first().waitFor({ state: 'visible' });
    await assertSafe(page, phase);
    const loginSlug = `${viewport.name}-login`;
    await page.screenshot({ path: join(output, `${loginSlug}.png`), fullPage: true, animations: 'disabled', caret: 'hide', scale: 'css' });
    screenshots.push({ slug: loginSlug, title: `${viewport.name === 'desktop' ? '데스크톱' : '모바일'} 로그인`, route: '/login', viewport, fullPage: true });

    await page.getByLabel(/사용자 이름|아이디/).fill(username);
    await page.getByLabel('비밀번호').fill(password);
    await Promise.all([
      page.waitForURL((url) => url.pathname !== '/login', { timeout: 15_000 }),
      page.getByRole('button', { name: '로그인', exact: true }).click(),
    ]);

    if (viewport.name === 'desktop') routes = [...staticRoutes, ...(await seedDynamicRoutes(context))];

    for (const route of routes) {
      phase = `capture:${viewport.name}:${route.path}`;
      await page.goto(new URL(route.path, baseURL).toString(), { waitUntil: 'domcontentloaded' });
      await page.locator('h1').first().waitFor({ state: 'visible', timeout: 15_000 });
      await settle(page);
      assert.equal(new URL(page.url()).pathname, route.path, `${route.path} 경로가 유지되어야 합니다.`);
      if (!route.state) assert.equal(await page.locator('[data-error-state], .error-state, [data-login-page], .login-page').count(), 0, `${route.path}에 오류/로그인 상태가 있습니다.`);
      await assertSafe(page, phase);
      const slug = `${viewport.name}-${route.slug}`;
      await page.screenshot({ path: join(output, `${slug}.png`), fullPage: true, animations: 'disabled', caret: 'hide', scale: 'css' });
      screenshots.push({ slug, title: `${viewport.name === 'desktop' ? '데스크톱' : '모바일'} ${route.title}`, route: route.path, viewport, fullPage: true });
    }

    phase = `capture:${viewport.name}:profile-menu-version`;
    await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
    await settle(page);
    await page.getByRole('button', { name: /프로필|내 계정|사용자 메뉴/ }).last().click();
    await page.getByText(new RegExp(`^moina\\s+${version.replaceAll('.', '\\.')}\$`, 'i')).waitFor({ state: 'visible' });
    const profileMenu = page.locator('.profile-popover');
    const profileBounds = await profileMenu.boundingBox();
    assert.ok(profileBounds, `${viewport.name} 프로필 메뉴의 화면 위치를 확인할 수 있어야 합니다.`);
    assert.ok(profileBounds.x >= 0 && profileBounds.x + profileBounds.width <= viewport.width, `${viewport.name} 프로필 메뉴 전체가 화면 너비 안에 있어야 합니다.`);
    assert.ok(profileBounds.y >= 0 && profileBounds.y + profileBounds.height <= viewport.height, `${viewport.name} 프로필 메뉴 전체가 화면 높이 안에 있어야 합니다.`);
    await assertSafe(page, phase);
    const profileSlug = `${viewport.name}-profile-menu-version`;
    await page.screenshot({ path: join(output, `${profileSlug}.png`), fullPage: false, animations: 'disabled', caret: 'hide', scale: 'css' });
    assert.equal(await profileMenu.isVisible(), true, `${viewport.name} 프로필 메뉴가 캡처 중 열린 상태를 유지해야 합니다.`);
    screenshots.push({ slug: profileSlug, title: `${viewport.name === 'desktop' ? '데스크톱' : '모바일'} 프로필 버전 메뉴`, route: '/flow', viewport, fullPage: false });

    // 다음 viewport는 새 세션으로 로그인 화면부터 검증한다.
    await context.clearCookies();
  }

  const manifest = {
    schemaVersion: 1,
    product: 'moina',
    version,
    generatedAt: new Date().toISOString(),
    source: 'Playwright actual application capture',
    screenshots,
    skipped: [],
    runtimeFailures: failures,
    ignoredExpectedConsoleMessages: ignored,
  };
  await writeFile(join(output, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(`실제 화면 캡처 완료: ${screenshots.length}개 (${routes.length}개 route × 2 + 로그인/버전 메뉴)`);
} finally {
  await context.close();
  await browser.close();
}
