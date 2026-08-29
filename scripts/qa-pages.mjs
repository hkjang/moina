#!/usr/bin/env node

import { access, readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { captureRoutes } from '../e2e/routes.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const docs = path.join(root, 'docs');
const canonicalBase = 'https://hkjang.github.io/moina/';
const version = (await readFile(path.join(root, 'VERSION'), 'utf8')).trim();
const requireScreenshots = process.env.MOINA_REQUIRE_SCREENSHOTS === '1';
const pages = [
  { file: 'index.html', canonical: canonicalBase, jsonTypes: ['WebSite', 'SoftwareApplication', 'FAQPage'], gallery: 'featured' },
  { file: 'user-guide.html', canonical: `${canonicalBase}user-guide.html`, jsonTypes: ['TechArticle', 'BreadcrumbList', 'HowTo'], gallery: 'user' },
  { file: 'admin-guide.html', canonical: `${canonicalBase}admin-guide.html`, jsonTypes: ['TechArticle', 'BreadcrumbList', 'HowTo'], gallery: 'admin' },
];
const errors = [];
const warnings = [];
const sources = new Map();
const pageIDs = new Map();
const fail = (message) => errors.push(message);
const matches = (source, expression) => [...source.matchAll(expression)];
const attribute = (tag, name) => {
  const match = tag.match(new RegExp(`\\s${name}=(?:"([^"]*)"|'([^']*)')`, 'i'));
  return match ? (match[1] ?? match[2]) : null;
};

async function exists(target) {
  try { await access(target); return true; } catch { return false; }
}

async function webpDimensions(target) {
  const data = await readFile(target);
  if (data.toString('ascii', 0, 4) !== 'RIFF' || data.toString('ascii', 8, 12) !== 'WEBP') throw new Error('RIFF WebP가 아닙니다.');
  for (let offset = 12; offset + 8 <= data.length;) {
    const type = data.toString('ascii', offset, offset + 4);
    const size = data.readUInt32LE(offset + 4);
    const payload = offset + 8;
    if (type === 'VP8X' && payload + 10 <= data.length) return { width: 1 + data.readUIntLE(payload + 4, 3), height: 1 + data.readUIntLE(payload + 7, 3) };
    if (type === 'VP8 ' && payload + 10 <= data.length && data.toString('hex', payload + 3, payload + 6) === '9d012a') return { width: data.readUInt16LE(payload + 6) & 0x3fff, height: data.readUInt16LE(payload + 8) & 0x3fff };
    if (type === 'VP8L' && payload + 5 <= data.length && data[payload] === 0x2f) {
      const bits = data.readUInt32LE(payload + 1);
      return { width: 1 + (bits & 0x3fff), height: 1 + ((bits >>> 14) & 0x3fff) };
    }
    offset = payload + size + (size % 2);
  }
  throw new Error('지원하는 WebP image chunk가 없습니다.');
}

async function pngDimensions(target) {
  const data = await readFile(target);
  const signature = '89504e470d0a1a0a';
  if (data.length < 24 || data.toString('hex', 0, 8) !== signature || data.toString('ascii', 12, 16) !== 'IHDR') throw new Error('유효한 PNG IHDR이 없습니다.');
  return { width: data.readUInt32BE(16), height: data.readUInt32BE(20) };
}

function jsonLDTypes(value, output = new Set()) {
  if (Array.isArray(value)) value.forEach((item) => jsonLDTypes(item, output));
  else if (value && typeof value === 'object') {
    const type = value['@type'];
    if (Array.isArray(type)) type.forEach((item) => output.add(item));
    else if (typeof type === 'string') output.add(type);
    Object.values(value).forEach((item) => jsonLDTypes(item, output));
  }
  return output;
}

for (const page of pages) {
  const target = path.join(docs, page.file);
  if (!(await exists(target))) { fail(`${page.file}: 파일이 없습니다.`); continue; }
  const source = await readFile(target, 'utf8');
  sources.set(page.file, source);

  if (!/^<!doctype html>/i.test(source.trimStart())) fail(`${page.file}: HTML5 doctype이 없습니다.`);
  if (!/<html\b[^>]*\blang="ko"/i.test(source)) fail(`${page.file}: lang=ko가 없습니다.`);
  if (!/<meta\b[^>]*name="viewport"[^>]*width=device-width/i.test(source)) fail(`${page.file}: 반응형 viewport가 없습니다.`);
  if (!/<meta\b[^>]*name="theme-color"[^>]*content="#e63e23"/i.test(source)) fail(`${page.file}: Orange-Red 브랜드 theme-color가 없습니다.`);
  if (!/<meta\b[^>]*name="application-name"[^>]*content="MOINA"/i.test(source)) fail(`${page.file}: application-name이 없습니다.`);
  if (!/<meta\b[^>]*name="apple-mobile-web-app-capable"[^>]*content="yes"/i.test(source)) fail(`${page.file}: Apple mobile web app 메타가 없습니다.`);
  if (!/<main\b/i.test(source)) fail(`${page.file}: main이 없습니다.`);
  if (matches(source, /<h1\b/gi).length !== 1) fail(`${page.file}: h1은 정확히 하나여야 합니다.`);

  const descriptionTag = matches(source, /<meta\b[^>]*name="description"[^>]*>/gi)[0]?.[0];
  const description = descriptionTag ? attribute(descriptionTag, 'content') : null;
  if (!description || [...description].length < 50 || [...description].length > 180) fail(`${page.file}: description은 50~180자여야 합니다.`);
  if (!/<meta\b[^>]*name="robots"[^>]*content="[^"]*index[^"]*follow/i.test(source)) fail(`${page.file}: index/follow robots가 없습니다.`);

  const canonicalTag = matches(source, /<link\b[^>]*rel="canonical"[^>]*>/gi)[0]?.[0];
  if (!canonicalTag || attribute(canonicalTag, 'href') !== page.canonical) fail(`${page.file}: canonical이 올바르지 않습니다.`);
  const faviconTag = matches(source, /<link\b[^>]*rel="icon"[^>]*>/gi)[0]?.[0];
  if (!faviconTag || attribute(faviconTag, 'href') !== 'assets/favicon.svg' || attribute(faviconTag, 'type') !== 'image/svg+xml') fail(`${page.file}: 브랜드 SVG favicon 연결이 올바르지 않습니다.`);
  const appleIconTag = matches(source, /<link\b[^>]*rel="apple-touch-icon"[^>]*>/gi)[0]?.[0];
  if (!appleIconTag || attribute(appleIconTag, 'href') !== 'assets/icon-192.png' || attribute(appleIconTag, 'sizes') !== '192x192') fail(`${page.file}: Apple touch icon 연결이 올바르지 않습니다.`);
  const manifestTag = matches(source, /<link\b[^>]*rel="manifest"[^>]*>/gi)[0]?.[0];
  if (!manifestTag || attribute(manifestTag, 'href') !== 'manifest.webmanifest') fail(`${page.file}: web manifest 연결이 올바르지 않습니다.`);
  if (!/<img\b[^>]*src="assets\/logo\.svg"[^>]*alt="MOINA"/i.test(source)) fail(`${page.file}: MOINA wordmark 연결 또는 대체 텍스트가 없습니다.`);
  for (const property of ['og:title', 'og:description', 'og:url', 'og:image', 'og:image:width', 'og:image:height', 'og:image:alt']) {
    if (!new RegExp(`<meta\\b[^>]*property="${property.replace(':', '\\:')}"`, 'i').test(source)) fail(`${page.file}: ${property}가 없습니다.`);
  }
  for (const name of ['twitter:card', 'twitter:title', 'twitter:description', 'twitter:image', 'twitter:image:alt']) {
    if (!new RegExp(`<meta\\b[^>]*name="${name.replace(':', '\\:')}"`, 'i').test(source)) fail(`${page.file}: ${name}가 없습니다.`);
  }
  if (!source.includes(`${canonicalBase}assets/og.webp`)) fail(`${page.file}: OG/Twitter image가 1200x630 WebP를 가리키지 않습니다.`);

  const ids = matches(source, /\sid=(?:"([^"]+)"|'([^']+)')/gi).map((match) => match[1] ?? match[2]);
  const duplicateIDs = ids.filter((id, index) => ids.indexOf(id) !== index);
  if (duplicateIDs.length) fail(`${page.file}: 중복 id(${[...new Set(duplicateIDs)].join(', ')})가 있습니다.`);
  pageIDs.set(page.file, new Set(ids));

  const structuredData = matches(source, /<script\b[^>]*type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/gi);
  if (!structuredData.length) fail(`${page.file}: JSON-LD가 없습니다.`);
  const types = new Set();
  for (const block of structuredData) {
    try { jsonLDTypes(JSON.parse(block[1]), types); } catch (error) { fail(`${page.file}: JSON-LD 파싱 실패(${error.message}).`); }
  }
  for (const required of page.jsonTypes) if (!types.has(required)) fail(`${page.file}: JSON-LD ${required}가 없습니다.`);

  for (const image of matches(source, /<img\b[^>]*>/gi).map((match) => match[0])) {
    if (attribute(image, 'alt') === null) fail(`${page.file}: alt 없는 img가 있습니다.`);
  }
  const marker = page.gallery === 'featured' ? 'data-screenshot-gallery' : `data-gallery-filter="${page.gallery}"`;
  if (!source.includes(marker)) fail(`${page.file}: 실제 screenshot gallery marker가 없습니다.`);

  const staleVersions = matches(source, /\bv\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?\b/g).map((match) => match[0]).filter((item) => item !== version);
  if (staleVersions.length) fail(`${page.file}: 현재 VERSION과 다른 버전 표기(${[...new Set(staleVersions)].join(', ')})가 있습니다.`);
}

for (const [file, source] of sources) {
  const references = matches(source, /\s(?:href|src)=(?:"([^"]+)"|'([^']+)')/gi).map((match) => match[1] ?? match[2]);
  for (const reference of references) {
    if (!reference || /^(?:https?:|mailto:|tel:|data:|javascript:)/i.test(reference)) continue;
    const [pathAndQuery, fragment = ''] = reference.split('#', 2);
    const pathname = pathAndQuery.split('?', 1)[0];
    const target = pathname ? path.resolve(docs, path.dirname(file), decodeURIComponent(pathname)) : path.join(docs, file);
    if (target !== docs && !target.startsWith(`${docs}${path.sep}`)) { fail(`${file}: docs 밖 경로(${reference})입니다.`); continue; }
    if (!(await exists(target))) { fail(`${file}: 로컬 링크/asset(${reference})이 없습니다.`); continue; }
    if (fragment) {
      const info = await stat(target);
      const targetFile = info.isFile() && target.endsWith('.html') ? path.basename(target) : file;
      if (pageIDs.has(targetFile) && !pageIDs.get(targetFile).has(decodeURIComponent(fragment))) fail(`${file}: 없는 anchor(${reference})입니다.`);
    }
  }
}

const requiredFiles = [
  '.nojekyll', 'robots.txt', 'sitemap.xml', 'llms.txt', 'manifest.webmanifest',
  'assets/favicon.svg', 'assets/logo.svg', 'assets/icon-maskable.svg',
  'assets/icon-192.png', 'assets/icon-512.png', 'assets/og.svg', 'assets/og.webp', 'assets/site.css',
  'assets/site.js', 'assets/guide.css', 'assets/screenshots/manifest.json',
  'configuration.md', 'operations.md', 'security.md', 'api-mcp.md', 'architecture.md',
];
for (const file of requiredFiles) if (!(await exists(path.join(docs, file)))) fail(`${file}: 필수 Pages 파일이 없습니다.`);

for (const file of ['assets/favicon.svg', 'assets/logo.svg', 'assets/icon-maskable.svg', 'assets/og.svg']) {
  try {
    const source = await readFile(path.join(docs, file), 'utf8');
    if (!/<svg\b[^>]*\bviewBox=/i.test(source) || !/<\/svg>\s*$/i.test(source)) fail(`${file}: viewBox를 가진 완결된 SVG가 아닙니다.`);
    if (/<script\b|<foreignObject\b|\b(?:href|src)=["']https?:|@import\b/i.test(source)) fail(`${file}: 실행 코드 또는 외부 asset 참조가 있습니다.`);
  } catch (error) { fail(`${file}: SVG 검증 실패(${error.message}).`); }
}

try {
  const og = await webpDimensions(path.join(docs, 'assets/og.webp'));
  if (og.width !== 1200 || og.height !== 630) fail(`og.webp가 1200x630이 아닙니다(${og.width}x${og.height}).`);
} catch (error) { fail(`og.webp 검증 실패(${error.message}).`); }

try {
  const webManifest = JSON.parse(await readFile(path.join(docs, 'manifest.webmanifest'), 'utf8'));
  if (webManifest.name !== 'MOINA — AI Social Knowledge Network' || webManifest.short_name !== 'MOINA' || webManifest.lang !== 'ko-KR') fail('manifest.webmanifest 브랜드 이름·언어가 올바르지 않습니다.');
  if (webManifest.id !== './' || webManifest.start_url !== './' || webManifest.scope !== './') fail('manifest.webmanifest의 Pages 상대 경로 범위가 올바르지 않습니다.');
  if (webManifest.theme_color.toLowerCase() !== '#e63e23' || webManifest.background_color.toLowerCase() !== '#faf8f7') fail('manifest.webmanifest Orange-Red 브랜드 색상이 올바르지 않습니다.');
  const icons = Array.isArray(webManifest.icons) ? webManifest.icons : [];
  for (const size of [192, 512]) {
    const source = `assets/icon-${size}.png`;
    const icon = icons.find((item) => item?.src === source);
    if (!icon || icon.type !== 'image/png' || icon.sizes !== `${size}x${size}` || !String(icon.purpose || '').split(/\s+/).includes('maskable')) fail(`manifest.webmanifest의 ${source} maskable 선언이 올바르지 않습니다.`);
    const dimensions = await pngDimensions(path.join(docs, source));
    if (dimensions.width !== size || dimensions.height !== size) fail(`${source}가 ${size}x${size}가 아닙니다(${dimensions.width}x${dimensions.height}).`);
  }
  if (!icons.some((item) => item?.src === 'assets/favicon.svg' && item.type === 'image/svg+xml' && item.sizes === 'any')) fail('manifest.webmanifest에 SVG favicon 선언이 없습니다.');
} catch (error) { fail(`PWA icon/manifest 검증 실패(${error.message}).`); }

const sitemap = await readFile(path.join(docs, 'sitemap.xml'), 'utf8').catch(() => '');
for (const page of pages) if (!sitemap.includes(`<loc>${page.canonical}</loc>`)) fail(`sitemap.xml에 ${page.canonical}이 없습니다.`);
const robots = await readFile(path.join(docs, 'robots.txt'), 'utf8').catch(() => '');
if (!robots.includes(`Sitemap: ${canonicalBase}sitemap.xml`)) fail('robots.txt Sitemap URL이 올바르지 않습니다.');
const llms = await readFile(path.join(docs, 'llms.txt'), 'utf8').catch(() => '');
for (const required of ['MOINA', version, 'MOINA_POSTGRES_DSN', `${canonicalBase}user-guide.html`]) if (!llms.includes(required)) fail(`llms.txt에 ${required}가 없습니다.`);

try {
  const manifestPath = path.join(docs, 'assets/screenshots/manifest.json');
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
  if (manifest.product !== 'moina' || manifest.version !== version) fail('screenshot manifest 제품/버전이 일치하지 않습니다.');
  if (manifest.schemaVersion !== 2 || JSON.stringify(manifest.themes) !== JSON.stringify(['light', 'dark'])) fail('screenshot manifest가 Light/Dark schema v2가 아닙니다.');
  for (const [kind, values] of Object.entries(manifest.runtimeFailures || {})) if (!Array.isArray(values) || values.length) fail(`runtimeFailures.${kind}가 비어 있지 않습니다.`);
  if (!Array.isArray(manifest.skipped) || manifest.skipped.length) fail('screenshot manifest에 생략된 화면이 있습니다.');
  const dynamic = ['profile-bootstrap', 'moin-detail', 'topic-moina', 'moim-detail'];
  const expected = new Set();
  for (const theme of ['light', 'dark']) {
    const prefix = theme === 'light' ? '' : `${theme}-`;
    for (const viewport of ['desktop', 'mobile']) {
      expected.add(`${prefix}${viewport}-login`);
      expected.add(`${prefix}${viewport}-profile-menu-version`);
      for (const route of captureRoutes) expected.add(`${prefix}${viewport}-${route.slug}`);
      for (const slug of dynamic) expected.add(`${prefix}${viewport}-${slug}`);
    }
  }
  const screenshots = Array.isArray(manifest.screenshots) ? manifest.screenshots : [];
  if (requireScreenshots) {
    if (manifest.pending || !manifest.generatedAt || manifest.source !== 'Playwright actual application capture') fail('실제 Playwright screenshot manifest가 아직 완성되지 않았습니다.');
    const actual = new Set(screenshots.map(({ slug }) => slug));
    for (const slug of expected) if (!actual.has(slug)) fail(`필수 실제 screenshot이 없습니다: ${slug}`);
    for (const slug of actual) if (!expected.has(slug)) fail(`catalog 밖 screenshot이 있습니다: ${slug}`);
    if (actual.size !== expected.size) fail(`screenshot 개수가 다릅니다(예상 ${expected.size}, 실제 ${actual.size}).`);
    for (const screenshot of screenshots) {
      const expectedTheme = screenshot.slug.startsWith('dark-') ? 'dark' : 'light';
      if (screenshot.theme !== expectedTheme) fail(`${screenshot.slug}: theme metadata가 ${expectedTheme}가 아닙니다.`);
      if (screenshot.path !== `assets/screenshots/${screenshot.slug}.webp`) fail(`${screenshot.slug}: manifest path가 올바르지 않습니다.`);
      const target = path.join(docs, screenshot.path);
      if (!(await exists(target))) { fail(`${screenshot.slug}: WebP가 없습니다.`); continue; }
      try {
        const dimensions = await webpDimensions(target);
        if (dimensions.width !== screenshot.viewport?.width || dimensions.height < screenshot.viewport?.height) fail(`${screenshot.slug}: 실제 크기와 viewport가 맞지 않습니다.`);
      } catch (error) { fail(`${screenshot.slug}: WebP 검증 실패(${error.message}).`); }
    }
  } else if (screenshots.length === 0) warnings.push('실제 screenshot은 아직 pending입니다. 배포 전 MOINA_REQUIRE_SCREENSHOTS=1 검사를 통과해야 합니다.');
} catch (error) { fail(`screenshot manifest 파싱 실패(${error.message}).`); }

if (errors.length) {
  console.error(`GitHub Pages QA 실패 (${errors.length}건)`);
  errors.forEach((error) => console.error(`- ${error}`));
  process.exit(1);
}
warnings.forEach((warning) => console.warn(`경고: ${warning}`));
console.log(`GitHub Pages QA 통과: HTML ${pages.length}개, canonical/SEO/AEO/JSON-LD/OG/링크 정상${requireScreenshots ? ', 실제 전체 화면 캡처 정상' : ''}`);
