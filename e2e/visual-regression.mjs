import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(scriptDirectory, '..');
const baselineDirectory = resolve(process.env.MOINA_VISUAL_BASELINES || join(scriptDirectory, 'visual-baselines'));
const resultDirectory = resolve(process.env.MOINA_VISUAL_OUTPUT || join(scriptDirectory, 'test-results/visual'));
const manifestPath = join(baselineDirectory, 'manifest.json');
const resultPath = join(resultDirectory, 'visual-regression.json');
const baseURL = new URL(process.env.MOINA_VISUAL_BASE_URL || process.env.MOINA_E2E_BASE_URL || 'http://127.0.0.1:18080');
const username = process.env.MOINA_VISUAL_USERNAME || process.env.MOINA_E2E_USERNAME || 'ci-admin';
const password = process.env.MOINA_VISUAL_PASSWORD || process.env.MOINA_E2E_PASSWORD;
const updateBaselines = process.env.MOINA_UPDATE_VISUALS === '1';
const headless = process.env.MOINA_VISUAL_HEADLESS !== '0';
const pixelThreshold = numberFromEnvironment('MOINA_VISUAL_PIXEL_THRESHOLD', 24, { minimum: 0, maximum: 255 });
const maxDiffRatio = numberFromEnvironment('MOINA_VISUAL_MAX_DIFF_RATIO', 0.005, { minimum: 0, maximum: 1 });
const loopbackHosts = new Set(['127.0.0.1', 'localhost', '[::1]']);

// 핵심 제품 경험 13개를 각 테마와 viewport에서 동일하게 비교합니다.
// 상세 데이터 route는 seed 상태에 따라 달라지므로 문서용 전체 캡처에서 다루고,
// 여기서는 빈 전용 DB에서도 결정적으로 렌더링되는 route만 사용합니다.
const screens = [
  { slug: 'login', path: '/login', public: true },
  { slug: 'flow', path: '/flow' },
  { slug: 'explore', path: '/explore' },
  { slug: 'search', path: '/search' },
  { slug: 'notifications', path: '/notifications' },
  { slug: 'moims', path: '/moims' },
  { slug: 'ai', path: '/ai' },
  { slug: 'settings-profile', path: '/settings/profile' },
  { slug: 'settings-notifications', path: '/settings/notifications' },
  { slug: 'settings-accessibility', path: '/settings/accessibility' },
  { slug: 'admin-dashboard', path: '/admin' },
  { slug: 'admin-smtp', path: '/admin/smtp' },
  { slug: 'admin-settings', path: '/admin/settings' },
];

const themes = ['light', 'dark'];
const viewports = [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
];
const variants = themes.flatMap((theme) => viewports.map((viewport) => ({ theme, viewport })));
const expectedEntries = variants.flatMap(({ theme, viewport }) => screens.map((screen) => ({
  id: `${theme}-${viewport.name}-${screen.slug}`,
  path: screen.path,
  theme,
  viewport: { name: viewport.name, width: viewport.width, height: viewport.height },
  public: Boolean(screen.public),
})));

if (process.argv.includes('--list')) {
  console.log(JSON.stringify({ screens: screens.length, baselines: expectedEntries.length, entries: expectedEntries }, null, 2));
  process.exit(0);
}

if (!password) throw new Error('MOINA_VISUAL_PASSWORD 또는 MOINA_E2E_PASSWORD가 필요합니다.');
if (!loopbackHosts.has(baseURL.hostname)) throw new Error('시각 회귀 캡처는 개인정보 보호를 위해 localhost의 전용 데이터베이스에서만 실행합니다.');
if (updateBaselines && process.env.CI && process.env.MOINA_ALLOW_CI_VISUAL_UPDATE !== '1') {
  throw new Error('CI에서는 베이스라인 갱신을 기본 차단합니다. 의도한 작업이면 MOINA_ALLOW_CI_VISUAL_UPDATE=1을 함께 지정하세요.');
}
assertSafeDirectory(baselineDirectory, scriptDirectory, '베이스라인');
assertSafeDirectory(resultDirectory, scriptDirectory, '결과');
await mkdir(resultDirectory, { recursive: true });
if (updateBaselines) await mkdir(baselineDirectory, { recursive: true });

const browser = await chromium.launch({ headless });
const results = [];
const failures = [];
const updatedImages = [];
let storedSession;

try {
  storedSession = await createAuthenticatedSession(browser);
  const manifest = updateBaselines ? null : await loadAndValidateManifest();
  const comparator = updateBaselines ? null : await browser.newPage();

  try {
    for (const variant of variants) {
      const context = await browser.newContext({
        ...(storedSession ? { storageState: storedSession } : {}),
        viewport: { width: variant.viewport.width, height: variant.viewport.height },
        deviceScaleFactor: 1,
        colorScheme: variant.theme,
        locale: 'ko-KR',
        timezoneId: 'Asia/Seoul',
        reducedMotion: 'reduce',
      });
      const runtime = createRuntimeMonitor();
      const page = await context.newPage();
      runtime.monitor(page);

      try {
        await persistTheme(context, variant.theme);
        for (const screen of screens) {
          const entry = expectedEntries.find((candidate) => candidate.theme === variant.theme
            && candidate.viewport.name === variant.viewport.name && candidate.path === screen.path);
          assert.ok(entry, `시각 회귀 entry를 찾을 수 없습니다: ${screen.path}`);
          runtime.phase = entry.id;
          // 로그인 화면은 인증 상태에서 자동 이동될 수 있으므로 동일 context의
          // cookie를 잠시 비우고 캡처한 뒤, 나머지 화면을 위해 저장 session을 복원합니다.
          if (screen.public) await context.clearCookies();
          await page.goto(new URL(screen.path, baseURL).toString(), { waitUntil: 'domcontentloaded' });
          await settle(page);
          await assertScreen(page, entry, runtime);
          await normalizeDynamicContent(page, username);

          const actual = await page.screenshot({
            animations: 'disabled',
            caret: 'hide',
            fullPage: false,
            scale: 'css',
            mask: [page.locator('.avatar, .moin-avatar, img[data-user-avatar], [data-visual-mask]')],
            maskColor: '#806B65',
          });
          const baselinePath = join(baselineDirectory, `${entry.id}.png`);

          if (updateBaselines) {
            // 전체 matrix가 정상 렌더링된 뒤 한 번에 기록해 중간 실패 시 기존
            // 승인 베이스라인이 일부만 바뀌는 상황을 방지합니다.
            updatedImages.push({ path: baselinePath, content: actual });
            results.push({ ...entry, status: 'updated', sha256: sha256(actual) });
            console.log(`베이스라인 준비: ${entry.id}`);
            if (screen.public) await context.addCookies(storedSession.cookies);
            continue;
          }

          const baseline = await readFile(baselinePath).catch((error) => {
            if (error?.code === 'ENOENT') {
              throw new Error(`${relative(projectRoot, baselinePath)}이 없습니다. 명시적 visual:update 명령으로 베이스라인을 생성하세요.`);
            }
            throw error;
          });
          const manifestEntry = manifest.entries.find((candidate) => candidate.id === entry.id);
          assert.equal(sha256(baseline), manifestEntry.sha256, `${entry.id} 베이스라인 SHA-256이 manifest와 다릅니다.`);
          const comparison = await comparePNGs(comparator, baseline, actual, pixelThreshold);
          const passed = comparison.sameDimensions && comparison.diffRatio <= maxDiffRatio;
          const result = {
            ...entry,
            status: passed ? 'passed' : 'failed',
            baselineSha256: manifestEntry.sha256,
            actualSha256: sha256(actual),
            changedPixels: comparison.changedPixels,
            totalPixels: comparison.totalPixels,
            diffRatio: comparison.diffRatio,
            dimensions: comparison.dimensions,
          };
          results.push(result);

          if (!passed) {
            const actualPath = join(resultDirectory, `${entry.id}-actual.png`);
            const diffPath = join(resultDirectory, `${entry.id}-diff.png`);
            await Promise.all([
              writeFile(actualPath, actual),
              writeFile(diffPath, Buffer.from(comparison.diffBase64, 'base64')),
            ]);
            failures.push({
              id: entry.id,
              reason: comparison.sameDimensions
                ? `차이 비율 ${(comparison.diffRatio * 100).toFixed(3)}%가 허용치 ${(maxDiffRatio * 100).toFixed(3)}%를 초과했습니다.`
                : `이미지 크기가 다릅니다(${comparison.dimensions.baseline.width}x${comparison.dimensions.baseline.height} -> ${comparison.dimensions.actual.width}x${comparison.dimensions.actual.height}).`,
              actual: relative(projectRoot, actualPath),
              diff: relative(projectRoot, diffPath),
            });
          } else {
            console.log(`시각 회귀 통과: ${entry.id} (${(comparison.diffRatio * 100).toFixed(3)}%)`);
          }
          if (screen.public) await context.addCookies(storedSession.cookies);
        }
      } finally {
        await context.close();
      }
    }
  } finally {
    await comparator?.close();
  }

  if (updateBaselines) {
    const entries = results.map(({ id, path, theme, viewport, sha256: digest }) => ({ id, path, theme, viewport, sha256: digest }));
    for (const image of updatedImages) await writeFile(image.path, image.content);
    await writeFile(manifestPath, `${JSON.stringify({
      schemaVersion: 1,
      screenCount: screens.length,
      baselineCount: entries.length,
      browser: { name: 'chromium', version: browser.version() },
      rendering: { locale: 'ko-KR', timezone: 'Asia/Seoul', deviceScaleFactor: 1, reducedMotion: true },
      comparison: { pixelThreshold, maxDiffRatio },
      entries,
    }, null, 2)}\n`);
  }

  await writeResult({ ok: failures.length === 0, mode: updateBaselines ? 'update' : 'compare' });
  assert.equal(failures.length, 0, `시각 회귀 ${failures.length}건 실패\n${failures.map((failure) => `- ${failure.id}: ${failure.reason}`).join('\n')}`);
  console.log(updateBaselines
    ? `시각 회귀 베이스라인 ${results.length}개를 갱신했습니다.`
    : `시각 회귀 ${results.length}개 통과(화면 ${screens.length}개 × 테마 2 × viewport 2).`);
} catch (error) {
  await writeResult({ ok: false, mode: updateBaselines ? 'update' : 'compare', error: error instanceof Error ? error.stack : String(error) }).catch(() => undefined);
  throw error;
} finally {
  await browser.close();
}

function numberFromEnvironment(name, fallback, { minimum, maximum }) {
  const raw = process.env[name];
  if (raw === undefined || raw === '') return fallback;
  const parsed = Number(raw);
  if (!Number.isFinite(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${name}은 ${minimum} 이상 ${maximum} 이하의 숫자여야 합니다.`);
  }
  return parsed;
}

function assertSafeDirectory(directory, parent, label) {
  if (directory === parent || !directory.startsWith(`${parent}${sep}`)) {
    throw new Error(`${label} 디렉터리는 ${relative(projectRoot, parent)} 아래의 전용 디렉터리여야 합니다.`);
  }
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function createRuntimeMonitor() {
  const state = { phase: 'startup', console: [], page: [], request: [], response: [], external: [] };
  const add = (kind, value) => { if (!state[kind].includes(value)) state[kind].push(value); };
  return {
    get phase() { return state.phase; },
    set phase(value) { state.phase = value; },
    monitor(page) {
      page.on('console', (message) => {
        if (message.type() !== 'error') return;
        const value = `[${state.phase}] ${message.text()}`;
        // The public login page probes the current session and an anonymous
        // 401 is its expected state, not a rendering failure.
        if (state.phase.endsWith('-login') && /401|Unauthorized/i.test(value)) return;
        add('console', value);
      });
      page.on('pageerror', (error) => add('page', `[${state.phase}] ${error.stack || error.message}`));
      page.on('requestfailed', (request) => {
        const reason = request.failure()?.errorText || '';
        // Route changes intentionally abort stale query consumers.
        if (reason === 'net::ERR_ABORTED') return;
        add('request', `[${state.phase}] ${request.url()} ${reason}`);
      });
      page.on('response', (response) => { if (response.status() >= 500) add('response', `[${state.phase}] ${response.status()} ${response.url()}`); });
      page.on('request', (request) => {
        const target = new URL(request.url());
        if (['http:', 'https:'].includes(target.protocol) && target.origin !== baseURL.origin) add('external', `[${state.phase}] ${target}`);
      });
    },
    failures() {
      return ['console', 'page', 'request', 'response', 'external']
        .flatMap((kind) => state[kind].map((value) => `${kind}: ${value}`));
    },
  };
}

async function settle(page) {
  await page.waitForLoadState('networkidle', { timeout: 20_000 });
  // 모바일 로그인 화면은 접근 가능한 단일 h1을 유지하되 장식용 hero와 함께
  // 숨깁니다. 모든 route에서 실제로 표시되는 주 콘텐츠를 안정 기준으로 삼습니다.
  await page.locator('main:visible, [role="main"]:visible').first().waitFor({ state: 'visible', timeout: 15_000 });
  await page.evaluate(async () => {
    await document.fonts?.ready;
    await Promise.all(Array.from(document.images).map((image) => image.complete
      ? Promise.resolve()
      : new Promise((resolveImage) => {
        image.addEventListener('load', resolveImage, { once: true });
        image.addEventListener('error', resolveImage, { once: true });
      })));
    window.scrollTo(0, 0);
  });
  await page.waitForTimeout(120);
}

async function assertScreen(page, entry, runtime) {
  assert.equal(new URL(page.url()).pathname, entry.path, `${entry.id}: 요청한 route가 유지되어야 합니다.`);
  assert.equal(await page.locator('h1').count(), 1, `${entry.id}: h1이 정확히 하나여야 합니다.`);
  if (!entry.public) {
    assert.equal(await page.locator('[data-login-page], .login-page').count(), 0, `${entry.id}: 인증 세션이 로그인 화면으로 돌아갔습니다.`);
  }
  const layout = await page.evaluate((theme) => ({
    theme: document.documentElement.dataset.theme,
    colorScheme: getComputedStyle(document.documentElement).colorScheme,
    scrollWidth: document.documentElement.scrollWidth,
    viewportWidth: window.innerWidth,
    fontSize: Number.parseFloat(getComputedStyle(document.body).fontSize),
    errorState: Boolean(document.querySelector('[data-error-state], .error-state')),
    expectedTheme: theme,
  }), entry.theme);
  if (entry.public) {
    assert.ok(layout.colorScheme.includes(entry.theme), `${entry.id}: ${entry.theme} color-scheme이 적용되지 않았습니다.`);
  } else {
    assert.equal(layout.theme, entry.theme, `${entry.id}: ${entry.theme} 테마가 적용되지 않았습니다.`);
  }
  assert.ok(layout.scrollWidth <= layout.viewportWidth + 1, `${entry.id}: 가로 overflow가 있습니다(${layout.scrollWidth}>${layout.viewportWidth}).`);
  assert.ok(layout.fontSize >= 16, `${entry.id}: 본문 글자 크기가 16px 미만입니다.`);
  assert.equal(layout.errorState, false, `${entry.id}: 오류 상태가 렌더링됐습니다.`);
  assert.deepEqual(runtime.failures(), [], `${entry.id}: 브라우저 런타임 오류\n${runtime.failures().join('\n')}`);
}

async function createAuthenticatedSession(browser) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 1,
    colorScheme: 'light',
    locale: 'ko-KR',
    timezoneId: 'Asia/Seoul',
    reducedMotion: 'reduce',
  });
  const page = await context.newPage();
  try {
    await page.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' });
    await page.getByRole('heading', { name: '로그인', exact: true }).waitFor({ state: 'visible' });
    await page.getByLabel(/사용자 이름|아이디/).fill(username);
    await page.getByLabel('비밀번호').fill(password);
    await Promise.all([
      page.waitForURL((url) => url.pathname !== '/login', { timeout: 15_000 }),
      page.getByRole('button', { name: '로그인', exact: true }).click(),
    ]);
    await page.waitForLoadState('networkidle', { timeout: 20_000 });
    return await context.storageState();
  } finally {
    await context.close();
  }
}

async function apiJSON(context, method, path, body) {
  const unsafe = !['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase());
  const csrfCookie = unsafe ? (await context.cookies(baseURL.toString())).find((cookie) => cookie.name === 'moina_csrf') : undefined;
  if (unsafe && !csrfCookie?.value) throw new Error(`${method} ${path}: CSRF cookie가 없습니다.`);
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
  if (!response.ok()) throw new Error(`${method} ${path}가 ${response.status()}로 실패했습니다: ${JSON.stringify(payload)}`);
  return payload?.data ?? payload;
}

async function persistTheme(context, theme) {
  const current = await apiJSON(context, 'GET', '/profile/preferences');
  const preferences = current && typeof current === 'object' ? current : {};
  const appearance = preferences.appearance && typeof preferences.appearance === 'object' ? preferences.appearance : {};
  await apiJSON(context, 'PUT', '/profile/preferences', {
    ...preferences,
    appearance: { ...appearance, theme, reduceMotion: true },
  });
}

async function normalizeDynamicContent(page, currentUsername) {
  await page.addStyleTag({ content: `
    *, *::before, *::after {
      animation-delay: 0s !important;
      animation-duration: 0s !important;
      caret-color: transparent !important;
      scroll-behavior: auto !important;
      transition-delay: 0s !important;
      transition-duration: 0s !important;
    }
    html { scrollbar-gutter: stable; }
    [data-visual-dynamic] { visibility: hidden !important; }
  ` });
  await page.evaluate((name) => {
    const replacementRules = [
      [/\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b/gi, '00000000-0000-4000-8000-000000000000'],
      [/\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})\b/g, '2000-01-01T00:00:00Z'],
      [/\b\d{4}-\d{2}-\d{2}\b/g, '2000-01-01'],
      [/\b\d{4}\.\s*\d{1,2}\.\s*\d{1,2}\.?(?:\s*(?:오전|오후)\s*\d{1,2}:\d{2})?/g, '2000. 1. 1.'],
      [/\b\d+\s*(?:초|분|시간|일|주|개월|년)\s*전\b/g, '고정 시각'],
      [/\b(?:방금|오늘|어제)\b/g, '고정 시각'],
    ];
    if (name) replacementRules.push([new RegExp(name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi'), 'visual-admin']);
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const node of nodes) {
      if (node.parentElement?.closest('script, style, textarea, input')) continue;
      let next = node.textContent || '';
      for (const [pattern, replacement] of replacementRules) next = next.replace(pattern, replacement);
      if (next !== node.textContent) node.textContent = next;
    }
    for (const time of document.querySelectorAll('time')) {
      time.textContent = '고정 시각';
      time.removeAttribute('datetime');
    }
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    window.scrollTo(0, 0);
  }, currentUsername);
  await page.waitForTimeout(50);
}

async function loadAndValidateManifest() {
  const raw = await readFile(manifestPath, 'utf8').catch((error) => {
    if (error?.code === 'ENOENT') {
      throw new Error(`${relative(projectRoot, manifestPath)}이 없습니다. MOINA_UPDATE_VISUALS=1로 베이스라인을 먼저 생성하세요.`);
    }
    throw error;
  });
  const manifest = JSON.parse(raw);
  assert.equal(manifest.schemaVersion, 1, '지원하지 않는 시각 회귀 manifest schema입니다.');
  assert.equal(manifest.screenCount, screens.length, 'manifest의 핵심 화면 수가 현재 계약과 다릅니다.');
  assert.equal(manifest.baselineCount, expectedEntries.length, 'manifest의 베이스라인 수가 현재 계약과 다릅니다.');
  assert.ok(Array.isArray(manifest.entries), 'manifest entries가 배열이어야 합니다.');
  assert.equal(manifest.browser?.name, 'chromium', '베이스라인 browser는 Chromium이어야 합니다.');
  assert.equal(
    manifest.browser?.version,
    browser.version(),
    `베이스라인 Chromium(${manifest.browser?.version || '알 수 없음'})과 현재 Chromium(${browser.version()})이 다릅니다. 고정 toolchain으로 실행하거나 베이스라인을 명시적으로 갱신하세요.`,
  );
  assert.deepEqual(
    manifest.entries.map((entry) => entry.id).sort(),
    expectedEntries.map((entry) => entry.id).sort(),
    'manifest 시나리오가 현재 Light·Dark/Desktop·Mobile 계약과 다릅니다.',
  );
  for (const expected of expectedEntries) {
    const actual = manifest.entries.find((entry) => entry.id === expected.id);
    assert.deepEqual(
      { path: actual.path, theme: actual.theme, viewport: actual.viewport },
      { path: expected.path, theme: expected.theme, viewport: expected.viewport },
      `${expected.id} manifest metadata가 현재 계약과 다릅니다.`,
    );
    assert.match(actual.sha256, /^[0-9a-f]{64}$/, `${expected.id} manifest SHA-256 형식이 올바르지 않습니다.`);
  }
  return manifest;
}

async function comparePNGs(page, baseline, actual, threshold) {
  return page.evaluate(async ({ baselineBase64, actualBase64, channelThreshold }) => {
    const decode = async (base64) => {
      const response = await fetch(`data:image/png;base64,${base64}`);
      return createImageBitmap(await response.blob());
    };
    const [expected, received] = await Promise.all([decode(baselineBase64), decode(actualBase64)]);
    const width = Math.max(expected.width, received.width);
    const height = Math.max(expected.height, received.height);
    const canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    const pixels = (image) => {
      const source = document.createElement('canvas');
      source.width = width;
      source.height = height;
      const sourceContext = source.getContext('2d', { willReadFrequently: true });
      sourceContext.fillStyle = '#ffffff';
      sourceContext.fillRect(0, 0, width, height);
      sourceContext.drawImage(image, 0, 0);
      return sourceContext.getImageData(0, 0, width, height).data;
    };
    const left = pixels(expected);
    const right = pixels(received);
    const output = context.createImageData(width, height);
    let changedPixels = 0;
    for (let offset = 0; offset < output.data.length; offset += 4) {
      const changed = Math.max(
        Math.abs(left[offset] - right[offset]),
        Math.abs(left[offset + 1] - right[offset + 1]),
        Math.abs(left[offset + 2] - right[offset + 2]),
        Math.abs(left[offset + 3] - right[offset + 3]),
      ) > channelThreshold;
      if (changed) {
        changedPixels += 1;
        output.data.set([180, 35, 69, 255], offset);
      } else {
        const gray = Math.round(0.2126 * left[offset] + 0.7152 * left[offset + 1] + 0.0722 * left[offset + 2]);
        output.data.set([gray, gray, gray, 72], offset);
      }
    }
    context.putImageData(output, 0, 0);
    const totalPixels = width * height;
    const result = {
      sameDimensions: expected.width === received.width && expected.height === received.height,
      changedPixels,
      totalPixels,
      diffRatio: totalPixels === 0 ? 0 : changedPixels / totalPixels,
      dimensions: {
        baseline: { width: expected.width, height: expected.height },
        actual: { width: received.width, height: received.height },
      },
      diffBase64: canvas.toDataURL('image/png').split(',')[1],
    };
    expected.close();
    received.close();
    return result;
  }, { baselineBase64: baseline.toString('base64'), actualBase64: actual.toString('base64'), channelThreshold: threshold });
}

async function writeResult(extra) {
  await writeFile(resultPath, `${JSON.stringify({
    ...extra,
    baseURL: baseURL.origin,
    screenCount: screens.length,
    expectedBaselineCount: expectedEntries.length,
    thresholds: { pixelThreshold, maxDiffRatio },
    results,
    failures,
  }, null, 2)}\n`);
}
