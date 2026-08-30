import axe, { type AxeResults, type ElementContext, type RunOptions } from 'axe-core';

const jsdomRules: RunOptions['rules'] = {
  // jsdom does not calculate layout or painted colors. Browser smoke/capture checks cover contrast.
  'color-contrast': { enabled: false },
};

export async function auditA11y(context: ElementContext, options: RunOptions = {}): Promise<AxeResults> {
  return axe.run(context, {
    ...options,
    rules: { ...jsdomRules, ...options.rules },
  });
}

export async function expectNoA11yViolations(context: ElementContext, options?: RunOptions) {
  const result = await auditA11y(context, options);
  if (!result.violations.length) return result;
  const detail = result.violations.map((violation) => {
    const nodes = violation.nodes.map((node) => `  - ${node.target.join(' ')}: ${node.failureSummary || node.html}`).join('\n');
    return `${violation.id} (${violation.impact || 'unknown'}): ${violation.help}\n${nodes}`;
  }).join('\n\n');
  throw new Error(`접근성 위반 ${result.violations.length}건\n\n${detail}`);
}
