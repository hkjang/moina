import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const sourceDirectory = resolve(process.cwd(), 'src');
const stylesDirectory = resolve(sourceDirectory, 'styles');
const themeDirectory = resolve(sourceDirectory, 'theme');

describe('MOINA 스타일 토큰 계약', () => {
  it('페이지와 컴포넌트 모듈은 원시 색상이나 Palette 단계에 직접 의존하지 않는다', () => {
    const modules = readdirSync(stylesDirectory).filter((file) => file.endsWith('.css'));
    expect(modules.length).toBeGreaterThanOrEqual(10);

    for (const module of modules) {
      const source = readFileSync(resolve(stylesDirectory, module), 'utf8');
      expect(source, `${module}에 원시 Hex 색상이 있습니다`).not.toMatch(/#[0-9a-f]{3,8}\b/i);
      expect(source, `${module}에 원시 RGB 색상이 있습니다`).not.toMatch(/rgba?\(\s*\d/i);
      expect(source, `${module}이 Palette 단계에 직접 의존합니다`).not.toMatch(/var\(--(?:brand|ai)-\d+/i);
      expect(source, `${module}에 이전 의미 토큰이 남아 있습니다`).not.toMatch(/var\(--(?:brand|brand-strong|accent|accent-soft)\)/i);
    }
  });

  it('styles.css는 테마와 역할별 모듈만 불러오는 작은 진입점이다', () => {
    const entrypoint = readFileSync(resolve(sourceDirectory, 'styles.css'), 'utf8');
    expect(entrypoint).toContain('@import "./theme/palette.css";');
    expect(entrypoint).toContain('@import "./theme/semantic.css";');
    expect(entrypoint).toContain('@import "./theme/light.css";');
    expect(entrypoint).toContain('@import "./theme/dark.css";');
    expect(entrypoint).toContain('@import "./styles/responsive.css";');
    expect(entrypoint.split('\n').length).toBeLessThanOrEqual(24);
  });

  it('상호작용 컴포넌트는 전용 접근성 토큰과 키보드 포커스를 사용한다', () => {
    const controls = readFileSync(resolve(stylesDirectory, 'controls.css'), 'utf8');
    const login = readFileSync(resolve(stylesDirectory, 'login.css'), 'utf8');
    const discovery = readFileSync(resolve(stylesDirectory, 'discovery.css'), 'utf8');
    const ai = readFileSync(resolve(stylesDirectory, 'ai.css'), 'utf8');
    const shell = readFileSync(resolve(stylesDirectory, 'shell.css'), 'utf8');
    const responsive = readFileSync(resolve(stylesDirectory, 'responsive.css'), 'utf8');

    expect(controls).toMatch(/\.ui-button-danger\s*\{[^}]*var\(--danger-fill\)/s);
    expect(controls).toMatch(/\.ui-button-secondary\s*\{[^}]*var\(--control-border\)/s);
    expect(controls).toMatch(/input, textarea, select\s*\{[^}]*var\(--control-border\)/s);
    expect(controls).toMatch(/\.avatar\s*\{[^}]*var\(--avatar-bg-start\)[^}]*var\(--avatar-bg-end\)/s);
    expect(login).toMatch(/\.oidc-button\s*\{[^}]*border:[^;}]*var\(--control-border\)/s);
    expect(login).toMatch(/\.offline-note\s*\{[^}]*color:\s*var\(--hero-note-fg\)[^}]*background:\s*var\(--hero-note-bg\)/s);
    expect(discovery).toMatch(/\.search-hero\s*\{[^}]*border:[^;}]*var\(--control-border\)/s);
    expect(ai).toMatch(/\.ai-welcome button\s*\{[^}]*border:[^;}]*var\(--control-border\)/s);
    expect(ai).toMatch(/\.ai-welcome button:hover\s*\{[^}]*border-color:\s*var\(--brand-fg\)/s);
    expect(ai).toMatch(/\.chat-composer\s*\{[^}]*border:[^;}]*var\(--control-border\)/s);
    expect(responsive).toMatch(/\.notification-link > span\s*\{[^}]*var\(--danger-fill\)/s);
    expect(shell).toMatch(/\.profile-menu-item:focus-visible\s*\{[^}]*outline:\s*3px solid var\(--focus-ring\)/s);
    expect(shell).not.toMatch(/\.profile-menu-item\s*\{[^}]*outline:\s*0/s);
  });

  it('로그아웃 화면도 운영체제의 Dark 설정을 따른다', () => {
    const dark = readFileSync(resolve(themeDirectory, 'dark.css'), 'utf8');
    expect(dark).toMatch(/@media\s*\(prefers-color-scheme:\s*dark\)[\s\S]*:root:not\(\[data-theme\]\)/);
  });
});
