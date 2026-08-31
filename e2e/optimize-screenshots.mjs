import { spawnSync } from 'node:child_process';
import { mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const input = resolve(process.env.MOINA_CAPTURE_OUTPUT || join(root, 'dist/screenshots-png'));
const output = resolve(process.env.MOINA_SCREENSHOT_OUTPUT || join(root, 'docs/assets/screenshots'));
const generatedRoot = resolve(root, 'dist');
const publishedOutput = resolve(root, 'docs/assets/screenshots');
const ffmpeg = process.env.MOINA_FFMPEG || 'ffmpeg';
if (input === generatedRoot || !input.startsWith(`${generatedRoot}${sep}`)) throw new Error('캡처 입력은 프로젝트 dist 하위의 전용 디렉터리여야 합니다.');
if (output !== publishedOutput) throw new Error('WebP 출력은 docs/assets/screenshots 전용 디렉터리만 허용합니다.');
const manifest = JSON.parse(await readFile(join(input, 'manifest.json'), 'utf8'));
if (!Array.isArray(manifest.screenshots) || !manifest.screenshots.length) throw new Error('최적화할 실제 캡처가 없습니다.');

const probe = spawnSync(ffmpeg, ['-hide_banner', '-encoders'], { encoding: 'utf8' });
if (probe.status !== 0 || !/\blibwebp\b/.test(`${probe.stdout}\n${probe.stderr}`)) throw new Error('libwebp encoder가 포함된 ffmpeg가 필요합니다.');

await mkdir(output, { recursive: true });
let sourceBytes = 0;
let targetBytes = 0;
for (const screenshot of manifest.screenshots) {
  if (!/^[a-z0-9-]+$/.test(screenshot.slug)) throw new Error(`안전하지 않은 screenshot slug: ${screenshot.slug}`);
  const source = join(input, `${screenshot.slug}.png`);
  const temporary = join(output, `.${screenshot.slug}.tmp.webp`);
  const target = join(output, `${screenshot.slug}.webp`);
  const result = spawnSync(ffmpeg, [
    '-hide_banner', '-loglevel', 'error', '-y', '-i', source,
    '-map_metadata', '-1', '-c:v', 'libwebp', '-preset', 'text',
    '-quality', '82', '-compression_level', '6', temporary,
  ], { encoding: 'utf8' });
  if (result.status !== 0) throw new Error(`${screenshot.slug} WebP 변환 실패: ${result.stderr.trim()}`);
  const sourceSize = (await stat(source)).size;
  const targetSize = (await stat(temporary)).size;
  if (targetSize >= sourceSize) throw new Error(`${screenshot.slug}: WebP가 PNG보다 작지 않습니다.`);
  await rename(temporary, target);
  sourceBytes += sourceSize;
  targetBytes += targetSize;
  screenshot.path = `assets/screenshots/${screenshot.slug}.webp`;
}

const keep = new Set(manifest.screenshots.map(({ slug }) => `${slug}.webp`));
const { readdir } = await import('node:fs/promises');
for (const entry of await readdir(output)) {
  if (entry.endsWith('.webp') && !keep.has(entry)) await rm(join(output, entry));
}
const temporaryManifest = join(output, '.manifest.json.tmp');
await writeFile(temporaryManifest, `${JSON.stringify(manifest, null, 2)}\n`);
await rename(temporaryManifest, join(output, 'manifest.json'));
console.log(`WebP 완료: ${manifest.screenshots.length}개, ${sourceBytes} → ${targetBytes} bytes (${((1 - targetBytes / sourceBytes) * 100).toFixed(1)}% 절감)`);
