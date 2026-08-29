import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const themeDirectory = resolve(process.cwd(), 'src/theme');
const darkSource = readFileSync(resolve(themeDirectory, 'dark.css'), 'utf8');
const lightSource = readFileSync(resolve(themeDirectory, 'light.css'), 'utf8');
const paletteSource = readFileSync(resolve(themeDirectory, 'palette.css'), 'utf8');
const semanticSource = readFileSync(resolve(themeDirectory, 'semantic.css'), 'utf8');

type RGB = readonly [number, number, number];

const declarationPattern = /(--[a-z0-9-]+)\s*:\s*([^;]+);/gi;

function declarations(...sources: string[]) {
  const values = new Map<string, string>();
  for (const source of sources) {
    for (const match of source.matchAll(declarationPattern)) {
      values.set(match[1], match[2].trim());
    }
  }
  return values;
}

function resolveColor(values: Map<string, string>, name: string, seen = new Set<string>()): string {
  if (seen.has(name)) throw new Error(`순환 토큰 참조: ${name}`);
  seen.add(name);
  const value = values.get(name);
  if (!value) throw new Error(`정의되지 않은 색상 토큰: ${name}`);
  const reference = value.match(/^var\((--[a-z0-9-]+)\)$/i);
  return reference ? resolveColor(values, reference[1], seen) : value;
}

function hexToRgb(hex: string): RGB {
  const value = hex.replace(/^#/, '');
  if (!/^[0-9a-f]{6}$/i.test(value)) throw new Error(`6자리 Hex 색상이 아닙니다: ${hex}`);
  return [0, 2, 4].map((offset) => Number.parseInt(value.slice(offset, offset + 2), 16)) as unknown as RGB;
}

function relativeLuminance(hex: string): number {
  const linear = hexToRgb(hex).map((channel) => {
    const normalized = channel / 255;
    return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
  });
  return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
}

function contrastRatio(foreground: string, background: string): number {
  const lighter = Math.max(relativeLuminance(foreground), relativeLuminance(background));
  const darker = Math.min(relativeLuminance(foreground), relativeLuminance(background));
  return (lighter + 0.05) / (darker + 0.05);
}

function expectContrast(values: Map<string, string>, foreground: string, background: string, minimum: number) {
  expect(
    contrastRatio(resolveColor(values, foreground), resolveColor(values, background)),
    `${foreground}와 ${background}의 대비가 ${minimum}:1 이상이어야 합니다`,
  ).toBeGreaterThanOrEqual(minimum);
}

const light = declarations(paletteSource, semanticSource, lightSource);
const dark = declarations(paletteSource, semanticSource, darkSource);

describe('MOINA 의미 색상 대비', () => {
  it.each([
    ['라이트', light],
    ['다크', dark],
  ] as const)('%s 테마의 채움형 CTA와 링크가 WCAG AA 대비를 만족한다', (_label, theme) => {
    expectContrast(theme, '--on-brand', '--brand-fill', 4.5);
    expectContrast(theme, '--on-brand', '--brand-fill-hover', 4.5);
    expectContrast(theme, '--brand-fg', '--surface', 4.5);
    expectContrast(theme, '--hero-note-fg', '--hero-note-bg', 4.5);
  });

  it.each([
    ['라이트', light],
    ['다크', dark],
  ] as const)('%s 테마의 포커스 링이 비텍스트 3:1 대비를 만족한다', (_label, theme) => {
    expectContrast(theme, '--focus-ring', '--surface', 3);
    expectContrast(theme, '--focus-ring', '--bg', 3);
  });

  it.each([
    ['라이트', light],
    ['다크', dark],
  ] as const)('%s 테마의 컨트롤 경계와 채움형 오류 동작이 접근성 대비를 만족한다', (_label, theme) => {
    expectContrast(theme, '--control-border', '--surface', 3);
    expectContrast(theme, '--on-brand', '--danger-fill', 4.5);
    expectContrast(theme, '--on-brand', '--danger-fill-hover', 4.5);
  });

  it.each([
    ['라이트', light],
    ['다크', dark],
  ] as const)('%s 테마의 텍스트 Avatar 양 끝 색상이 4.5:1 대비를 만족한다', (_label, theme) => {
    expectContrast(theme, '--on-brand', '--avatar-bg-start', 4.5);
    expectContrast(theme, '--on-brand', '--avatar-bg-end', 4.5);
  });

  it.each([
    ['라이트 성공', light, '--positive', '--positive-soft'],
    ['라이트 오류', light, '--danger', '--danger-soft'],
    ['라이트 경고', light, '--warning', '--warning-soft'],
    ['다크 성공', dark, '--positive', '--positive-soft'],
    ['다크 오류', dark, '--danger', '--danger-soft'],
    ['다크 경고', dark, '--warning', '--warning-soft'],
  ] as const)('%s 상태 텍스트와 전용 배경이 4.5:1 대비를 만족한다', (_label, theme, foreground, background) => {
    expectContrast(theme, foreground, background, 4.5);
  });
});
