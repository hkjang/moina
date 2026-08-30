#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const errors = [];
const fail = (message) => errors.push(message);
const relative = (target) => path.relative(root, target).split(path.sep).join('/');
const read = (target) => readFile(path.join(root, target), 'utf8');

const legacyColors = [
  '#116d62', '#07594f', '#53c9b6', '#74dfce', '#173b35',
  '#6557e8', '#4a45c7', '#087e80', '#93f0e3', '#b7fff2',
  '#5b4ce8', '#3a2fa7', '#13a8a4', '#d8f8f3', '#e2dfff',
];

async function walk(directory) {
  const output = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name);
    if (entry.isDirectory()) output.push(...await walk(target));
    else output.push(target);
  }
  return output;
}

const textExtensions = new Set(['.css', '.html', '.svg', '.ts', '.tsx', '.js', '.mjs', '.json', '.webmanifest']);
const scanRoots = ['frontend/src', 'frontend/public', 'docs/assets', 'docs/index.html', 'docs/user-guide.html', 'docs/admin-guide.html', 'docs/manifest.webmanifest'];
const scanFiles = [];
for (const item of scanRoots) {
  const target = path.join(root, item);
  const files = path.extname(target) ? [target] : await walk(target);
  scanFiles.push(...files.filter((file) => textExtensions.has(path.extname(file)) && !relative(file).startsWith('docs/assets/screenshots/')));
}

for (const target of scanFiles) {
  const source = (await readFile(target, 'utf8')).toLowerCase();
  for (const color of legacyColors) if (source.includes(color)) fail(`${relative(target)}에 이전 브랜드 색상 ${color.toUpperCase()}가 남아 있습니다.`);
}

const approvedTokenFiles = new Set([
  'frontend/src/theme/palette.css',
  'frontend/src/theme/semantic.css',
  'frontend/src/theme/light.css',
  'frontend/src/theme/dark.css',
]);
for (const target of scanFiles.filter((file) => relative(file).startsWith('frontend/src/'))) {
  const name = relative(target);
  const source = await readFile(target, 'utf8');
  const literals = [...source.matchAll(/#[0-9a-f]{3,8}\b/gi)].map((match) => match[0]);
  if (literals.length && !approvedTokenFiles.has(name)) fail(`${name}에서 승인된 theme 파일 밖 원시 Hex를 사용했습니다: ${[...new Set(literals)].join(', ')}`);
  if (name.startsWith('frontend/src/styles/') && /var\(--(?:brand|ai)-\d+\)/.test(source)) fail(`${name}에서 Palette Token을 직접 사용했습니다. Semantic Token을 사용하세요.`);
}

const entrypoint = await read('frontend/src/styles.css');
const imports = [
  './theme/palette.css', './theme/semantic.css', './theme/light.css', './theme/dark.css',
  './styles/base.css', './styles/controls.css', './styles/shell.css', './styles/login.css', './styles/feed.css',
  './styles/composer.css', './styles/discovery.css', './styles/ai.css', './styles/settings.css',
  './styles/admin.css', './styles/misc.css', './styles/responsive.css',
];
let importPosition = -1;
for (const item of imports) {
  const position = entrypoint.indexOf(`@import "${item}"`);
  if (position < 0) fail(`frontend/src/styles.css에 ${item} import가 없습니다.`);
  else if (position <= importPosition) fail(`frontend/src/styles.css의 ${item} import 순서가 올바르지 않습니다.`);
  importPosition = position;
}
if (entrypoint.length > 1_500) fail(`frontend/src/styles.css는 import 진입점이어야 합니다(${entrypoint.length} bytes).`);

function declarations(source) {
  return Object.fromEntries([...source.matchAll(/--([a-z0-9-]+)\s*:\s*([^;]+);/gi)].map((match) => [match[1], match[2].trim()]));
}

const palette = declarations(await read('frontend/src/theme/palette.css'));
const semantic = declarations(await read('frontend/src/theme/semantic.css'));
const themeValues = {
  light: declarations(await read('frontend/src/theme/light.css')),
  dark: declarations(await read('frontend/src/theme/dark.css')),
};
const expectedPalette = {
  'brand-50': '#fff4f1', 'brand-100': '#ffe5df', 'brand-200': '#ffc9be', 'brand-300': '#ffa18f',
  'brand-400': '#f8745d', 'brand-500': '#e63e23', 'brand-600': '#d4311a', 'brand-700': '#b72a17',
  'brand-800': '#8f2417', 'brand-900': '#6f2118', 'brand-950': '#3d0e09',
};
for (const [name, value] of Object.entries(expectedPalette)) if (palette[name]?.toLowerCase() !== value) fail(`--${name}은 ${value.toUpperCase()}여야 합니다.`);

function resolveVariable(name, values, seen = new Set()) {
  if (seen.has(name)) throw new Error(`순환 Token: --${name}`);
  seen.add(name);
  const raw = values[name];
  if (!raw) throw new Error(`Token 없음: --${name}`);
  const variable = raw.match(/^var\(--([a-z0-9-]+)\)$/i)?.[1];
  return variable ? resolveVariable(variable, values, seen) : raw.toLowerCase();
}

function rgb(hex) {
  const match = hex.match(/^#([0-9a-f]{6})$/i);
  if (!match) throw new Error(`6자리 Hex가 아닙니다: ${hex}`);
  return [0, 2, 4].map((offset) => Number.parseInt(match[1].slice(offset, offset + 2), 16));
}

function luminance(hex) {
  const channels = rgb(hex).map((value) => {
    const channel = value / 255;
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

function contrast(left, right) {
  const values = [luminance(left), luminance(right)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

const expectedThemes = {
  light: {
    bg: '#faf8f7', surface: '#ffffff', 'surface-soft': '#f8f2f0', 'surface-raised': '#ffffff', ink: '#261c1a',
    'ink-muted': '#6f5e59', 'ink-soft': '#806b65', line: '#e9ddd9', 'line-strong': '#d8c8c3', 'control-border': '#9b8179',
    'brand-fg': '#b72a17', 'brand-soft': '#fff4f1', 'brand-soft-hover': '#ffe5df', 'brand-border': '#ffc9be',
    positive: '#147a55', 'positive-soft': '#e8f7f1', 'danger-fill': '#b42345', 'danger-fill-hover': '#8f1d38',
    'ai-fg': '#7657d6', 'ai-soft': '#f3f0ff',
  },
  dark: {
    bg: '#17110f', surface: '#211816', 'surface-soft': '#2b1f1c', 'surface-raised': '#32231f', ink: '#fff3ef',
    'ink-muted': '#d2bbb4', 'ink-soft': '#b0958d', line: '#48342f', 'line-strong': '#62463e', 'control-border': '#8f6c62',
    'brand-fg': '#ff8a73', 'brand-soft': '#3a1b16', 'brand-soft-hover': '#4a211a', 'brand-border': '#7b3428',
    positive: '#65d4a8', 'positive-soft': '#17372d', 'danger-fill': '#b42345', 'danger-fill-hover': '#8f1d38',
    'ai-fg': '#b29af8', 'ai-soft': '#302747',
  },
};

for (const [theme, expected] of Object.entries(expectedThemes)) {
  const values = { ...palette, ...semantic, ...themeValues[theme] };
  for (const [name, expectedValue] of Object.entries(expected)) {
    try {
      const actual = resolveVariable(name, values);
      if (actual !== expectedValue) fail(`${theme} --${name}은 ${expectedValue.toUpperCase()}여야 합니다(${actual}).`);
    } catch (error) { fail(`${theme}: ${error.message}`); }
  }
  const required = (name) => resolveVariable(name, values);
  try {
    if (required('brand-identity') !== '#e63e23') fail(`${theme}: --brand-identity는 #E63E23이어야 합니다.`);
    if (required('brand-fill') !== '#d4311a' || required('brand-fill-hover') !== '#b72a17') fail(`${theme}: CTA Fill/Hover Token이 올바르지 않습니다.`);
    const checks = [
      ['Primary CTA', required('brand-fill'), required('on-brand'), 4.5],
      ['Primary CTA Hover', required('brand-fill-hover'), required('on-brand'), 4.5],
      ['링크', required('brand-fg'), required('surface'), 4.5],
      ['Focus Ring', required('focus-ring'), required('bg'), 3],
      ['Control Border', required('control-border'), required('surface'), 3],
      ['Danger Fill', required('danger-fill'), required('on-brand'), 4.5],
      ['Danger Fill Hover', required('danger-fill-hover'), required('on-brand'), 4.5],
      ['Avatar Start', required('avatar-bg-start'), required('on-brand'), 4.5],
      ['Avatar End', required('avatar-bg-end'), required('on-brand'), 4.5],
      ['Login Hero Note', required('hero-note-fg'), required('hero-note-bg'), 4.5],
      ['AI Accent', required('ai-fg'), required('ai-soft'), 4.5],
    ];
    for (const [label, foreground, background, minimum] of checks) {
      const ratio = contrast(foreground, background);
      if (ratio < minimum) fail(`${theme} ${label} 대비 ${ratio.toFixed(2)}:1은 ${minimum}:1 미만입니다.`);
    }
    if (required('positive') === required('brand-identity')) fail(`${theme}: 성공색과 브랜드색이 분리되지 않았습니다.`);
  } catch (error) { fail(`${theme} 대비 계산 실패: ${error.message}`); }
}

const controls = await read('frontend/src/styles/controls.css');
const base = await read('frontend/src/styles/base.css');
const feed = await read('frontend/src/styles/feed.css');
const login = await read('frontend/src/styles/login.css');
const discovery = await read('frontend/src/styles/discovery.css');
const ai = await read('frontend/src/styles/ai.css');
if (!/\.ui-button-primary\s*\{[^}]*color:\s*var\(--on-brand\)[^}]*background:\s*var\(--brand-fill\)/s.test(controls)) fail('Primary Button이 on-brand/brand-fill Token을 사용하지 않습니다.');
if (!/\.ui-button-primary:hover[^}]*background:\s*var\(--brand-fill-hover\)/s.test(controls)) fail('Primary Button Hover가 brand-fill-hover Token을 사용하지 않습니다.');
if (!/\.badge-positive\s*\{[^}]*color:\s*var\(--positive\)[^}]*background:\s*var\(--positive-soft\)/s.test(controls)) fail('Positive Badge가 positive-soft Token을 사용하지 않습니다.');
if (!/\.ui-button-danger\s*\{[^}]*background:\s*var\(--danger-fill\)/s.test(controls)) fail('Danger Button이 접근 가능한 danger-fill Token을 사용하지 않습니다.');
if (!/\.ui-button-secondary\s*\{[^}]*border-color:\s*var\(--control-border\)/s.test(controls)) fail('Secondary Button이 3:1 control-border Token을 사용하지 않습니다.');
if (!/\.avatar\s*\{[^}]*var\(--avatar-bg-start\)[^}]*var\(--avatar-bg-end\)/s.test(controls)) fail('텍스트 Avatar가 접근 가능한 전용 Gradient Token을 사용하지 않습니다.');
if (!/\.oidc-button\s*\{[^}]*border:[^;}]*var\(--control-border\)/s.test(login)) fail('OIDC Button이 3:1 control-border Token을 사용하지 않습니다.');
if (!/\.offline-note\s*\{[^}]*color:\s*var\(--hero-note-fg\)[^}]*background:\s*var\(--hero-note-bg\)/s.test(login)) fail('로그인 Hero 안내문이 전용 고대비 Token을 사용하지 않습니다.');
if (!/\.search-hero\s*\{[^}]*border:[^;}]*var\(--control-border\)/s.test(discovery)) fail('검색 입력 영역이 3:1 control-border Token을 사용하지 않습니다.');
if (!/\.ai-welcome button\s*\{[^}]*border:[^;}]*var\(--control-border\)/s.test(ai)) fail('AI 제안 Button이 3:1 control-border Token을 사용하지 않습니다.');
if (!/\.ai-welcome button:hover\s*\{[^}]*border-color:\s*var\(--brand-fg\)/s.test(ai)) fail('AI 제안 Button Hover가 고대비 brand-fg Border를 유지하지 않습니다.');
if (!/\.chat-composer\s*\{[^}]*border:[^;}]*var\(--control-border\)/s.test(ai)) fail('AI 작성 영역이 3:1 control-border Token을 사용하지 않습니다.');
if (!/:focus-visible\s*\{[^}]*outline:\s*3px solid var\(--focus-ring\)/s.test(base)) fail('Focus Ring이 3px focus-ring Token을 사용하지 않습니다.');
const shell = await read('frontend/src/styles/shell.css');
if (!/\.profile-menu-item:focus-visible\s*\{[^}]*outline:\s*3px solid var\(--focus-ring\)/s.test(shell)) fail('프로필 메뉴가 키보드 Focus Ring을 보존하지 않습니다.');
if (!/button:disabled\s*\{[^}]*cursor:\s*not-allowed[^}]*opacity:/s.test(base)) fail('Disabled Button은 색상 외 cursor/opacity 구분이 필요합니다.');
if (!/\.remoin-label\s*\{[^}]*color:\s*var\(--positive\)/s.test(feed)) fail('Remoin 성공 상태가 positive Token을 사용하지 않습니다.');

for (const html of ['frontend/index.html', 'docs/index.html', 'docs/user-guide.html', 'docs/admin-guide.html']) {
  const source = await read(html);
  if (!/<meta\b[^>]*name="theme-color"[^>]*content="#e63e23"/i.test(source)) fail(`${html}의 theme-color가 #E63E23이 아닙니다.`);
}
for (const manifest of ['frontend/public/manifest.webmanifest', 'docs/manifest.webmanifest']) {
  const value = JSON.parse(await read(manifest));
  if (value.theme_color?.toLowerCase() !== '#e63e23' || value.background_color?.toLowerCase() !== '#faf8f7') fail(`${manifest}의 PWA 색상이 올바르지 않습니다.`);
}

const vectorAssets = [
  'frontend/public/moina-logo.svg', 'frontend/public/moina-mark.svg', 'docs/assets/logo.svg',
  'docs/assets/favicon.svg', 'docs/assets/icon-maskable.svg', 'docs/assets/og.svg',
];
for (const asset of vectorAssets) {
  const source = (await read(asset)).toLowerCase();
  for (const color of ['#f8745d', '#e63e23', '#b72a17', '#ffd1c8', '#fff1ed']) if (!source.includes(color)) fail(`${asset}에 새 브랜드 색상 ${color.toUpperCase()}가 없습니다.`);
}

const expectedAssets = {
  'frontend/public/icon-192.png': '7b02e5b05eb5920df5a5e89b90d988b484e719cc86c35db66176aeb95fc166e6',
  'frontend/public/icon-512.png': 'a87acd6cc3e93fc8479031d9c40bf6b5e4fcb4e7229b3131a50ce3f115c5e7d7',
  'docs/assets/icon-192.png': '7b02e5b05eb5920df5a5e89b90d988b484e719cc86c35db66176aeb95fc166e6',
  'docs/assets/icon-512.png': 'a87acd6cc3e93fc8479031d9c40bf6b5e4fcb4e7229b3131a50ce3f115c5e7d7',
  'docs/assets/og.webp': '743db216c798efbf862a80e575d76ae029e4e02ec47b62c56ad06edef755df42',
};
for (const [asset, expected] of Object.entries(expectedAssets)) {
  const digest = createHash('sha256').update(await readFile(path.join(root, asset))).digest('hex');
  if (digest !== expected) fail(`${asset}가 검증된 Orange-Red raster fixture와 다릅니다(${digest}).`);
}

if (errors.length) {
  console.error(`브랜드·대비 검사 실패 (${errors.length}건)`);
  errors.forEach((error) => console.error(`- ${error}`));
  process.exit(1);
}
console.log('브랜드·대비 검사 통과: #E63E23 identity, WCAG CTA/link/focus, Light/Dark, SVG·PWA·raster 정상');
