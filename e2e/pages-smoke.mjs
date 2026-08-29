import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { chromium } from 'playwright';

const baseURL = (process.env.MOINA_PAGES_BASE_URL || 'http://127.0.0.1:4173').replace(/\/$/, '');
const output = process.env.MOINA_PAGES_OUTPUT || '../dist/pages-smoke';
const routes = [
  { path: '/', heading: '사람과 생각이' },
  { path: '/user-guide.html', heading: 'MOINA 사용자 가이드' },
  { path: '/admin-guide.html', heading: 'MOINA 관리자 가이드' },
];
const viewports = [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
];
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: true });
const failures = [];

try {
  for (const viewport of viewports) {
    const context = await browser.newContext({ viewport, colorScheme: 'light', locale: 'ko-KR', reducedMotion: 'reduce' });
    for (const route of routes) {
      const page = await context.newPage();
      const label = `${viewport.name}:${route.path}`;
      page.on('console', (message) => { if (message.type() === 'error') failures.push(`${label} console: ${message.text()}`); });
      page.on('pageerror', (error) => failures.push(`${label} pageerror: ${error.message}`));
      page.on('requestfailed', (request) => failures.push(`${label} request: ${request.url()} ${request.failure()?.errorText || ''}`));
      page.on('response', (response) => { if (response.status() >= 400) failures.push(`${label} HTTP ${response.status()}: ${response.url()}`); });
      const response = await page.goto(`${baseURL}${route.path}`, { waitUntil: 'networkidle' });
      assert.ok(response?.ok(), `${label}: 성공 응답이어야 합니다.`);
      const h1 = page.locator('h1');
      assert.equal(await h1.count(), 1, `${label}: h1은 하나여야 합니다.`);
      assert.ok((await h1.innerText()).includes(route.heading), `${label}: 예상 제목이 없습니다.`);
      const width = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, inner: window.innerWidth }));
      assert.ok(width.scroll <= width.inner + 1, `${label}: 가로 overflow가 있습니다.`);
      const broken = await page.locator('img').evaluateAll((images) => images.filter((image) => image.complete && image.naturalWidth === 0).map((image) => image.src));
      assert.deepEqual(broken, [], `${label}: 깨진 이미지가 있습니다.`);
      const gallery = page.locator('[data-screenshot-gallery]');
      if (await gallery.count()) {
        const lightTheme = page.locator('[data-gallery-theme="light"]');
        const darkTheme = page.locator('[data-gallery-theme="dark"]');
        await lightTheme.waitFor({ state: 'visible' });
        assert.equal(await lightTheme.getAttribute('aria-pressed'), 'true', `${label}: 밝은 화면이 기본 선택이어야 합니다.`);
        assert.equal(await darkTheme.getAttribute('aria-pressed'), 'false', `${label}: 어두운 화면은 기본 선택이 아니어야 합니다.`);
        const firstImage = gallery.locator('img').first();
        await firstImage.waitFor({ state: 'visible' });
        const lightSource = await firstImage.getAttribute('src');
        assert.ok(lightSource && !lightSource.includes('/dark-'), `${label}: 밝은 화면 이미지가 표시되어야 합니다.`);
        await darkTheme.click();
        assert.equal(await darkTheme.getAttribute('aria-pressed'), 'true', `${label}: 어두운 화면 선택 상태가 표시되어야 합니다.`);
        assert.equal(await lightTheme.getAttribute('aria-pressed'), 'false', `${label}: 밝은 화면 선택 상태가 해제되어야 합니다.`);
        const darkSource = await firstImage.getAttribute('src');
        assert.ok(darkSource?.includes('/dark-'), `${label}: 어두운 화면 이미지로 교체되어야 합니다.`);
        assert.notEqual(darkSource, lightSource, `${label}: 테마 전환 시 이미지가 변경되어야 합니다.`);
        await lightTheme.click();
        assert.equal(await firstImage.getAttribute('src'), lightSource, `${label}: 밝은 화면 이미지로 복원되어야 합니다.`);
      }
      await page.screenshot({ path: `${output}/${viewport.name}-${route.path === '/' ? 'index' : route.path.replace(/\W+/g, '-')}.png`, fullPage: true, animations: 'disabled', caret: 'hide' });
      if (viewport.name === 'mobile') {
        const menu = page.getByRole('button', { name: '메뉴 열기', exact: true });
        if (await menu.count()) {
          await menu.click();
          assert.ok(await page.locator('[data-mobile-menu]').isVisible(), `${label}: 모바일 메뉴가 열려야 합니다.`);
          await page.keyboard.press('Escape');
          assert.equal(await page.locator('[data-mobile-menu]').isVisible(), false, `${label}: Escape로 메뉴가 닫혀야 합니다.`);
        }
      }
      await page.close();
    }
    await context.close();
  }
} finally {
  await browser.close();
}
if (failures.length) throw new Error(`Pages 런타임 오류:\n${failures.join('\n')}`);
console.log(`Pages 브라우저 QA 통과: ${routes.length}개 문서 × ${viewports.length}개 viewport`);
