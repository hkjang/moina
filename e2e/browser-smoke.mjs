import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { routeCatalogFromEnvironment } from './routes.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const resultDirectory = resolve(process.env.MOINA_E2E_OUTPUT || join(root, 'e2e/test-results'));
const resultPath = join(resultDirectory, 'browser-smoke.json');
const failurePath = join(resultDirectory, 'browser-smoke-failure.png');
const baseURL = new URL(process.env.MOINA_E2E_BASE_URL || 'http://127.0.0.1:8080');
const username = process.env.MOINA_E2E_USERNAME || 'e2e-admin';
const password = process.env.MOINA_E2E_PASSWORD;
const expectedVersion = process.env.MOINA_E2E_VERSION || 'v0.1.7';
const headless = process.env.MOINA_E2E_HEADLESS !== '0';
const routes = routeCatalogFromEnvironment();
const clipboardPNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=';

if (!password) throw new Error('MOINA_E2E_PASSWORD가 필요합니다.');
await mkdir(resultDirectory, { recursive: true });

const failures = { console: [], page: [], request: [], response: [], external: [] };
const ignored = [];
const completed = [];
let phase = 'startup';

function add(kind, value) {
  if (!failures[kind].includes(value)) failures[kind].push(value);
}

function summary() {
  return Object.entries(failures).filter(([, values]) => values.length)
    .map(([kind, values]) => `${kind}:\n${values.map((value) => `  - ${value}`).join('\n')}`).join('\n');
}

function monitor(page) {
  page.on('console', (message) => {
    if (message.type() !== 'error') return;
    const value = `[${phase}] ${message.text()}`;
    if (phase === 'login' && /401|Unauthorized/i.test(value)) ignored.push(value);
    else add('console', value);
  });
  page.on('pageerror', (error) => add('page', `[${phase}] ${error.stack || error.message}`));
  page.on('request', (request) => {
    const target = new URL(request.url());
    if (['http:', 'https:'].includes(target.protocol) && target.origin !== baseURL.origin) add('external', `[${phase}] ${request.method()} ${target}`);
  });
  page.on('requestfailed', (request) => {
    const reason = request.failure()?.errorText || '실패';
    const value = `[${phase}] ${request.method()} ${request.url()} (${reason})`;
    if (reason === 'net::ERR_ABORTED') ignored.push(value);
    else add('request', value);
  });
  page.on('response', (response) => {
    if (response.status() >= 500) add('response', `[${phase}] ${response.status()} ${response.url()}`);
  });
}

async function settle(page) {
  await page.waitForLoadState('domcontentloaded');
  await page.waitForLoadState('networkidle', { timeout: 20_000 });
  await page.waitForTimeout(120);
}

async function assertPage(page, route) {
  await settle(page);
  assert.equal(new URL(page.url()).pathname, route.path, `${route.path} 경로가 유지되어야 합니다.`);
  const heading = page.locator('h1');
  await heading.first().waitFor({ state: 'visible', timeout: 15_000 });
  assert.equal(await heading.count(), 1, `${route.path}에는 h1이 정확히 하나여야 합니다.`);
  if (!route.state) {
    assert.equal(await page.locator('[data-error-state], .error-state').count(), 0, `${route.path}에 오류 상태가 표시됐습니다.`);
    assert.equal(await page.locator('[data-login-page], .login-page').count(), 0, `${route.path}가 로그인으로 돌아갔습니다.`);
  }
  const width = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, inner: window.innerWidth }));
  assert.ok(width.scroll <= width.inner + 1, `${route.path}에 가로 overflow가 있습니다(${width.scroll}>${width.inner}).`);
  assert.equal(summary(), '', `${route.path} 브라우저 런타임 오류\n${summary()}`);
}

const browser = await chromium.launch({ headless });
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
  colorScheme: 'light',
  locale: 'ko-KR',
  timezoneId: 'Asia/Seoul',
});
const page = await context.newPage();
monitor(page);

try {
  phase = 'login';
  const loginDocument = await page.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  assert.equal(loginDocument?.headers()['cache-control'], 'no-cache', 'SPA 문서는 배포 후 재검증되어야 합니다.');
  await page.getByRole('heading', { name: '로그인', exact: true }).waitFor({ state: 'visible' });
  await settle(page);
  phase = 'asset-contract';
  const moduleSource = await page.locator('script[type="module"][src]').first().getAttribute('src');
  assert.ok(moduleSource, '진입 ES module 경로를 확인할 수 있어야 합니다.');
  const proxyOrigin = new URL(baseURL);
  proxyOrigin.protocol = 'https:';
  const moduleResponse = await context.request.get(new URL(moduleSource, baseURL).toString(), {
    headers: { Origin: proxyOrigin.origin },
  });
  assert.equal(moduleResponse.status(), 200, 'TLS 종료 proxy 뒤에서도 진입 ES module을 제공해야 합니다.');
  assert.equal(moduleResponse.headers()['cache-control'], 'public, max-age=31536000, immutable', 'hash asset은 immutable이어야 합니다.');
  assert.match(moduleResponse.headers()['content-type'] || '', /javascript/, 'ES module MIME이 올바르게 설정되어야 합니다.');
  const missingAsset = await context.request.get(new URL('/assets/stale-release-chunk.js', baseURL).toString());
  assert.equal(missingAsset.status(), 404, '이전 릴리스의 누락 chunk는 SPA HTML이 아니라 404여야 합니다.');
  assert.equal(missingAsset.headers()['cache-control'], 'no-store', '누락 chunk 응답을 저장하면 안 됩니다.');
  phase = 'login';
  await page.getByText(new RegExp(`moina\\s+${expectedVersion.replaceAll('.', '\\.')}|MOINA\\s+${expectedVersion.replaceAll('.', '\\.')}`, 'i')).first().waitFor({ state: 'visible' });
  await page.getByLabel(/사용자 이름|아이디/).fill(username);
  await page.getByLabel('비밀번호').fill(password);
  await Promise.all([
    page.waitForURL((url) => url.pathname !== '/login', { timeout: 15_000 }),
    page.getByRole('button', { name: '로그인', exact: true }).click(),
  ]);

  for (const route of routes) {
    phase = `route:${route.path}`;
    await page.goto(new URL(route.path, baseURL).toString(), { waitUntil: 'domcontentloaded' });
    await assertPage(page, route);
    await page.reload({ waitUntil: 'domcontentloaded' });
    await assertPage(page, route);
    completed.push(route.path);
  }

  phase = 'profile-version';
  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await settle(page);
  const profileTrigger = page.getByRole('button', { name: /프로필|내 계정|사용자 메뉴/ }).last();
  await profileTrigger.click();
  await page.getByText(new RegExp(`^moina\\s+${expectedVersion.replaceAll('.', '\\.')}\$`, 'i')).waitFor({ state: 'visible' });
  assert.equal(summary(), '', `프로필 버전 확인 중 오류\n${summary()}`);

  phase = 'mobile-admin-navigation';
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(new URL('/admin', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await settle(page);
  assert.ok(await page.evaluate(() => Number.parseFloat(getComputedStyle(document.body).fontSize) >= 16), '기본 본문 글자 크기는 16px 이상이어야 합니다.');
  await page.getByRole('button', { name: '메뉴 열기', exact: true }).click();
  const panel = page.locator('.mobile-nav-panel');
  await panel.waitFor({ state: 'visible' });
  const scroll = await panel.locator('[data-radix-scroll-area-viewport]').evaluate((element) => ({
    scrollHeight: element.scrollHeight,
    clientHeight: element.clientHeight,
    overflowY: getComputedStyle(element).overflowY,
  }));
  assert.ok(scroll.scrollHeight > scroll.clientHeight, '모바일 관리자 메뉴는 실제 스크롤 가능한 높이여야 합니다.');
  const styledScrollbar = panel.locator('.radix-scrollbar');
  assert.ok(await styledScrollbar.count(), '관리자 메뉴에 서비스 전용 scrollbar가 있어야 합니다.');
  const workflow = await context.request.get(new URL('/api/v1/workflow/status', baseURL).toString());
  const workflowBody = await workflow.json();
  if (!(workflowBody?.data?.approvalEnabled || workflowBody?.data?.approvalPending)) {
    assert.equal(await panel.getByRole('link', { name: '검토·승인', exact: true }).count(), 0, '승인 정책이 꺼져 있으면 승인 메뉴를 제외해야 합니다.');
  }
  await page.keyboard.press('Escape');
  assert.equal(await panel.isVisible(), false, 'Escape로 모바일 관리자 메뉴가 닫혀야 합니다.');

  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await settle(page);
  await page.getByRole('button', { name: '프로필 메뉴', exact: true }).click();
  const mobileProfileMenu = page.locator('.profile-popover');
  await mobileProfileMenu.waitFor({ state: 'visible' });
  const profileBounds = await mobileProfileMenu.boundingBox();
  assert.ok(profileBounds, '모바일 프로필 메뉴의 화면 위치를 확인할 수 있어야 합니다.');
  assert.ok(profileBounds.x >= 0 && profileBounds.x + profileBounds.width <= 390, '모바일 프로필 메뉴 전체가 화면 너비 안에 있어야 합니다.');
  assert.ok(profileBounds.y >= 0 && profileBounds.y + profileBounds.height <= 844, '모바일 프로필 메뉴 전체가 화면 높이 안에 있어야 합니다.');
  await page.getByText(new RegExp(`^moina\\s+${expectedVersion.replaceAll('.', '\\.')}\$`, 'i')).waitFor({ state: 'visible' });
  assert.equal(summary(), '', `모바일 관리자 메뉴 확인 중 오류\n${summary()}`);

  phase = 'composer-clipboard-media';
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(new URL('/flow', baseURL).toString(), { waitUntil: 'domcontentloaded' });
  await settle(page);
  await page.locator('.composer-prompt').click();
  const composerDialog = page.getByRole('dialog', { name: '새 모인' });
  await composerDialog.waitFor({ state: 'visible' });
  const composerTextarea = composerDialog.getByRole('textbox', { name: '모인 내용', exact: true });
  await composerTextarea.waitFor({ state: 'visible' });
  await page.waitForFunction((element) => document.activeElement === element, await composerTextarea.elementHandle(), { timeout: 5_000 });
  assert.equal(await composerTextarea.evaluate((element) => document.activeElement === element), true, '새 모인 Dialog가 열리면 내용 입력란에 포커스가 가야 합니다.');
  const composerFileInput = composerDialog.locator('input[type="file"]');
  assert.equal(await composerFileInput.count(), 1, '새 모인 Dialog에는 파일 입력이 하나여야 합니다.');
  assert.equal(await composerFileInput.evaluate((element) => element.tabIndex), -1, '숨겨진 파일 입력은 Tab 순서에서 제외되어야 합니다.');
  const composerMediaButton = composerDialog.getByRole('button', { name: /이미지 또는 영상 첨부/ });
  await composerMediaButton.waitFor({ state: 'visible' });
  await page.waitForFunction((element) => !element.disabled, await composerMediaButton.elementHandle(), { timeout: 5_000 });

  const uploadResponsePromise = page.waitForResponse((response) => {
    const target = new URL(response.url());
    return response.request().method() === 'POST' && target.pathname === '/api/v1/media';
  });
  const pasteResult = await composerTextarea.evaluate((element, png) => {
    const bytes = Uint8Array.from(atob(png), (value) => value.charCodeAt(0));
    const transfer = new DataTransfer();
    transfer.items.add(new File([bytes], 'clipboard-smoke.png', {
      type: 'image/png',
      lastModified: 1_700_000_000_000,
    }));
    const event = new ClipboardEvent('paste', {
      bubbles: true,
      cancelable: true,
      clipboardData: transfer,
    });
    element.dispatchEvent(event);
    return { defaultPrevented: event.defaultPrevented, files: event.clipboardData?.files.length || 0 };
  }, clipboardPNG);
  assert.deepEqual(pasteResult, { defaultPrevented: true, files: 1 }, 'PNG ClipboardEvent를 미디어 첨부로 처리해야 합니다.');
  const uploadResponse = await uploadResponsePromise;
  assert.equal(uploadResponse.status(), 201, '클립보드 PNG 업로드는 201이어야 합니다.');
  const uploadBody = await uploadResponse.json();
  const mediaID = uploadBody?.data?.id;
  assert.ok(typeof mediaID === 'string' && mediaID, '업로드 응답에 미디어 ID가 있어야 합니다.');
  await composerDialog.getByText('1/1개 업로드 완료', { exact: true }).waitFor({ state: 'visible' });
  const preview = composerDialog.locator('[aria-label="첨부 미디어"] img');
  await preview.waitFor({ state: 'visible' });
  const previewDimensions = await preview.evaluate(async (element) => {
    await element.decode();
    return { width: element.naturalWidth, height: element.naturalHeight };
  });
  assert.deepEqual(previewDimensions, { width: 1, height: 1 }, '붙여넣은 PNG 미리보기를 렌더링해야 합니다.');
  const removeButton = composerDialog.getByRole('button', { name: '클립보드 이미지 1.png 제거', exact: true });
  await removeButton.waitFor({ state: 'visible' });

  const deletePath = `/api/v1/media/${encodeURIComponent(mediaID)}`;
  const deleteResponsePromise = page.waitForResponse((response) => {
    const target = new URL(response.url());
    return response.request().method() === 'DELETE' && target.pathname === deletePath;
  });
  await removeButton.click();
  const deleteResponse = await deleteResponsePromise;
  assert.equal(deleteResponse.status(), 204, '게시하지 않은 클립보드 미디어 제거는 204여야 합니다.');
  await composerDialog.locator('[aria-label="첨부 미디어"]').waitFor({ state: 'hidden' });
  await composerDialog.getByRole('button', { name: '창 닫기', exact: true }).click();
  await composerDialog.waitFor({ state: 'hidden' });
  assert.equal(summary(), '', `클립보드 미디어 확인 중 오류\n${summary()}`);

  await writeFile(resultPath, `${JSON.stringify({ ok: true, version: expectedVersion, routes: completed, failures, ignored }, null, 2)}\n`);
  console.log(`브라우저 smoke 통과: ${completed.length}개 route, 새로고침·버전·클립보드 미디어·외부요청·콘솔 오류 정상`);
} catch (error) {
  await page.screenshot({ path: failurePath, fullPage: true }).catch(() => undefined);
  await writeFile(resultPath, `${JSON.stringify({ ok: false, routes: completed, failures, ignored, error: error instanceof Error ? error.stack : String(error) }, null, 2)}\n`).catch(() => undefined);
  throw error;
} finally {
  await context.close();
  await browser.close();
}
