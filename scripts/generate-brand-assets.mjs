#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const temporary = join(root, 'dist', 'brand-assets');
const requireFromE2E = createRequire(join(root, 'e2e', 'package.json'));
const { chromium } = requireFromE2E('playwright');
const requiredPalette = ['#F8745D', '#E63E23', '#B72A17', '#FFD1C8', '#FFF1ED'];

function runFFmpeg(arguments_, label) {
  const result = spawnSync('ffmpeg', arguments_, { encoding: 'utf8' });
  if (result.status !== 0) throw new Error(`${label} 변환 실패: ${result.stderr.trim() || 'ffmpeg를 실행할 수 없습니다.'}`);
}

function digest(buffer) {
  return createHash('sha256').update(buffer).digest('hex');
}

async function assertSVG(sourcePath, width, height) {
  const source = await readFile(sourcePath, 'utf8');
  if (!source.includes(`width="${width}" height="${height}"`) || !source.includes(`viewBox="0 0 ${width} ${height}"`)) {
    throw new Error(`${sourcePath}: ${width}x${height} SVG 치수가 필요합니다.`);
  }
  for (const color of requiredPalette) if (!source.includes(color)) throw new Error(`${sourcePath}: 브랜드 색상 ${color}이 없습니다.`);
  return source;
}

async function rasterizePNG(page, sourcePath, outputPath, sourceWidth, sourceHeight, width, height) {
  const source = await assertSVG(sourcePath, sourceWidth, sourceHeight);
  const dataURL = `data:image/svg+xml;base64,${Buffer.from(source).toString('base64')}`;
  const encoded = await page.evaluate(async ({ dataURL: sourceURL, width: targetWidth, height: targetHeight }) => {
    const image = new Image();
    image.src = sourceURL;
    await image.decode();
    const canvas = document.createElement('canvas');
    canvas.width = targetWidth;
    canvas.height = targetHeight;
    const context = canvas.getContext('2d');
    if (!context) throw new Error('2D canvas를 사용할 수 없습니다.');
    context.imageSmoothingEnabled = true;
    context.imageSmoothingQuality = 'high';
    context.drawImage(image, 0, 0, targetWidth, targetHeight);
    return canvas.toDataURL('image/png');
  }, { dataURL, width, height });
  if (!encoded.startsWith('data:image/png;base64,')) throw new Error(`${sourcePath}: PNG 인코딩에 실패했습니다.`);
  await writeFile(outputPath, Buffer.from(encoded.slice(encoded.indexOf(',') + 1), 'base64'));
}

const encoderProbe = spawnSync('ffmpeg', ['-hide_banner', '-encoders'], { encoding: 'utf8' });
const availableEncoders = `${encoderProbe.stdout}\n${encoderProbe.stderr}`;
if (encoderProbe.status !== 0 || !/\bpng\b/.test(availableEncoders) || !/\blibwebp\b/.test(availableEncoders)) {
  throw new Error('PNG와 libwebp encoder가 포함된 ffmpeg가 필요합니다.');
}

await rm(temporary, { recursive: true, force: true });
await mkdir(temporary, { recursive: true });
let browser;

try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  const iconSource = join(root, 'docs', 'assets', 'icon-maskable.svg');
  const generated = [];

  for (const size of [192, 512]) {
    const raw = join(temporary, `icon-${size}.raw.png`);
    const optimized = join(temporary, `icon-${size}.png`);
    await rasterizePNG(page, iconSource, raw, 64, 64, size, size);
    runFFmpeg([
      '-hide_banner', '-loglevel', 'error', '-y', '-i', raw,
      '-map_metadata', '-1', '-c:v', 'png', '-pred', 'mixed', '-compression_level', '9', optimized,
    ], `${size}x${size} 아이콘`);
    const buffer = await readFile(optimized);
    for (const target of [
      join(root, 'frontend', 'public', `icon-${size}.png`),
      join(root, 'docs', 'assets', `icon-${size}.png`),
    ]) await writeFile(target, buffer);
    generated.push({ label: `icon-${size}.png`, buffer });
  }

  const ogSource = join(root, 'docs', 'assets', 'og.svg');
  const rawOG = join(temporary, 'og.raw.png');
  const optimizedOG = join(temporary, 'og.webp');
  await rasterizePNG(page, ogSource, rawOG, 1200, 630, 1200, 630);
  runFFmpeg([
    '-hide_banner', '-loglevel', 'error', '-y', '-i', rawOG,
    '-map_metadata', '-1', '-c:v', 'libwebp', '-preset', 'text',
    '-quality', '92', '-compression_level', '6', optimizedOG,
  ], '1200x630 OG');
  const ogBuffer = await readFile(optimizedOG);
  await writeFile(join(root, 'docs', 'assets', 'og.webp'), ogBuffer);
  generated.push({ label: 'og.webp', buffer: ogBuffer });

  for (const { label, buffer } of generated) {
    const size = (await stat(join(temporary, label))).size;
    console.log(`${label}: ${size} bytes sha256=${digest(buffer)}`);
  }
} finally {
  await browser?.close();
  await rm(temporary, { recursive: true, force: true });
}
