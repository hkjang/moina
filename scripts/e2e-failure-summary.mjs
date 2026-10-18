#!/usr/bin/env node

// e2e 결과 JSON에서 실패 항목만 추려 GitHub job summary용 Markdown으로 출력합니다.
// CI의 image 잡이 실패하면 남는 단서가 "exit code 1"뿐이라 artifact를 내려받아야
// 어느 화면·route가 깨졌는지 알 수 있었습니다. 이 스크립트는 진단 출력만 담당하며
// 통과 조건을 바꾸지 않으므로, 결과 파일이 없거나 깨져 있어도 종료 코드는 항상 0입니다.

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
// e2e 스크립트가 결과를 쓰는 경로와 같은 규칙을 씁니다.
const resultDirectory = path.resolve(process.argv[2] || process.env.MOINA_E2E_OUTPUT || path.join(root, 'e2e/test-results'));
const visualDirectory = path.resolve(process.env.MOINA_VISUAL_OUTPUT || path.join(resultDirectory, 'visual'));
// 요약은 실패 항목만 담습니다. 로그 전문은 artifact에 그대로 남습니다.
const maxItems = 20;

const lines = [];
const add = (line) => lines.push(line);

const displayPath = (target) => (target.startsWith(`${root}${path.sep}`) ? path.relative(root, target) : target);

async function readResult(file) {
  try {
    return { value: JSON.parse(await readFile(file, 'utf8')) };
  } catch (error) {
    if (error.code === 'ENOENT') return {};
    return { unreadable: `${displayPath(file)}를 읽을 수 없습니다: ${error.message}` };
  }
}

const percent = (ratio) => (typeof ratio === 'number' ? `${(ratio * 100).toFixed(3)}%` : '알 수 없음');
const firstLine = (text) => String(text).split(/\r?\n/)[0].slice(0, 300);
const inlineCode = (text) => `\`${String(text).replaceAll('`', "'").slice(0, 200)}\``;
// 표 안의 값은 selector처럼 |를 품을 수 있어 열이 깨지지 않게 escape 합니다.
const cell = (text) => String(text ?? '').replaceAll('|', '\\|');

// Playwright timeout은 첫 줄이 "Timeout 30000ms exceeded."뿐이라 어느 기다림이었는지
// 알 수 없습니다. 앞 몇 줄과 e2e 스크립트 stack frame까지만 덧붙입니다.
function addError(text) {
  const all = String(text).split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const detail = all.slice(0, 3);
  const frame = all.find((line) => /^at .*\.mjs:\d+/.test(line));
  if (frame && !detail.includes(frame)) detail.push(frame);
  const [first, ...rest] = detail;
  add(`- 오류: ${inlineCode(first)}`);
  for (const line of rest) add(`  - ${inlineCode(line)}`);
}

function addItems(items, render) {
  for (const item of items.slice(0, maxItems)) add(render(item));
  if (items.length > maxItems) add(`- …외 ${items.length - maxItems}건 (전체는 \`moina-ci-diagnostics\` artifact 참조)`);
}

// 통과로 기록된 결과와 아예 없는 결과를 구분해, 실패가 e2e 밖에서 났는지 보이게 합니다.
const passed = [];

function addSection(title, result) {
  if (result.unreadable) {
    add(`### ${title}`);
    add(`- ${result.unreadable}`);
    add('');
    return;
  }
  if (!result.value) return;
  if (result.value.ok === true) {
    passed.push(title);
    return;
  }
  add(`### ${title}`);
  return result.value;
}

const visual = await readResult(path.join(visualDirectory, 'visual-regression.json'));
const visualResult = addSection('시각 회귀 (`npm run test:visual`)', visual);
if (visualResult) {
  const failures = Array.isArray(visualResult.failures) ? visualResult.failures : [];
  const ratios = new Map((visualResult.results || []).map((entry) => [entry.id, entry.diffRatio]));
  const allowed = percent(visualResult.thresholds?.maxDiffRatio);
  if (failures.length) {
    // 항목 수를 제한하므로 가장 크게 어긋난 화면이 먼저 보이도록 정렬합니다.
    const ordered = [...failures].sort((left, right) => (ratios.get(right.id) ?? 0) - (ratios.get(left.id) ?? 0));
    add(`허용치 ${allowed}를 넘은 화면 ${failures.length}개 / 비교 ${(visualResult.results || []).length}개 (차이 비율 내림차순)`);
    add('');
    add('| 화면 id | 차이 비율 | diff 이미지 |');
    add('| --- | --- | --- |');
    addItems(ordered, (failure) => `| \`${cell(failure.id)}\` | ${percent(ratios.get(failure.id))} | \`${cell(failure.diff)}\` |`);
  } else {
    add(`비교를 마치기 전에 중단됐습니다(비교 ${(visualResult.results || []).length}개, 허용치 ${allowed}).`);
  }
  if (visualResult.error) addError(visualResult.error);
  add('');
}

const accessibility = await readResult(path.join(resultDirectory, 'accessibility-regression.json'));
const accessibilityResult = addSection('접근성 회귀 (`npm run test:accessibility`)', accessibility);
if (accessibilityResult) {
  const blocking = Array.isArray(accessibilityResult.blocking) ? accessibilityResult.blocking : [];
  const runtimeFailures = Array.isArray(accessibilityResult.runtimeFailures) ? accessibilityResult.runtimeFailures : [];
  if (blocking.length) {
    add(`Axe Serious/Critical 위반 ${blocking.length}건 / Axe DOM scan ${(accessibilityResult.scans || []).length}개`);
    add('');
    add('| route | 테마/viewport | 규칙 | 심각도 | 요소 |');
    add('| --- | --- | --- | --- | --- |');
    addItems(blocking, (item) => `| \`${cell(item.route)}\` | ${cell(item.theme)}/${cell(item.viewport)} | \`${cell(item.id)}\` | ${cell(item.impact)} | \`${cell((item.nodes || []).map((node) => [node.target].flat(2).join(' ')).join(', ').slice(0, 200))}\` |`);
    add('');
  }
  if (runtimeFailures.length) {
    add(`브라우저 런타임 오류 ${runtimeFailures.length}건`);
    addItems(runtimeFailures, (failure) => `- ${inlineCode(firstLine(failure))}`);
    add('');
  }
  if (accessibilityResult.phase) add(`- 중단 단계: \`${accessibilityResult.phase}\``);
  if (accessibilityResult.error) addError(accessibilityResult.error);
  add('');
}

const smoke = await readResult(path.join(resultDirectory, 'browser-smoke.json'));
const smokeResult = addSection('브라우저 smoke (`npm run test:smoke`)', smoke);
if (smokeResult) {
  const routes = Array.isArray(smokeResult.routes) ? smokeResult.routes : [];
  add(`통과한 route ${routes.length}개${routes.length ? `, 마지막 route \`${routes[routes.length - 1]}\`` : ''}`);
  for (const [kind, values] of Object.entries(smokeResult.failures || {})) {
    if (!Array.isArray(values) || !values.length) continue;
    add('');
    add(`${kind} 오류 ${values.length}건`);
    addItems(values, (value) => `- ${inlineCode(firstLine(value))}`);
  }
  if (smokeResult.error) {
    add('');
    addError(smokeResult.error);
  }
  add('');
}

if (!lines.length) {
  add(passed.length
    ? `e2e 결과 ${passed.length}개가 모두 통과로 기록돼 있습니다(${passed.join(', ')}). 실패는 e2e 밖의 단계에서 났을 수 있습니다.`
    : `e2e 결과 JSON이 없습니다(\`${displayPath(resultDirectory)}\`). e2e를 실행하기 전 단계에서 실패했을 수 있습니다.`);
}
console.log(`## e2e 실패 요약\n\n${lines.join('\n')}`);
