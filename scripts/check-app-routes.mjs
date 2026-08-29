#!/usr/bin/env node

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { appRoutes, stateRoutes } from '../e2e/routes.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const source = await readFile(path.join(root, 'frontend/src/App.tsx'), 'utf8');
const pageRoutes = new Set(['/login']);
for (const line of source.split(/\r?\n/)) {
  const match = line.match(/<Route\s+path="([^"]+)"\s+element=/);
  if (!match || match[1] === '*' || line.includes('<Navigate ')) continue;
  pageRoutes.add(`/${match[1].replace(/^\//, '')}`);
}

const catalog = new Set(appRoutes.map(({ path: route }) => route));
for (const route of stateRoutes) if (route.path === '/access-denied') catalog.add(route.path);
catalog.add('/login');
for (const dynamic of ['/moin/:id', '/topics/:slug', '/moims/:slug', '/profile/:username']) catalog.add(dynamic);

const missing = [...pageRoutes].filter((route) => !catalog.has(route)).sort();
const stale = [...catalog].filter((route) => !pageRoutes.has(route)).sort();
if (missing.length || stale.length) {
  console.error('React page route와 Playwright 전체 화면 catalog가 일치하지 않습니다.');
  missing.forEach((route) => console.error(`- 캡처 누락: ${route}`));
  stale.forEach((route) => console.error(`- 앱에 없는 catalog: ${route}`));
  process.exit(1);
}
console.log(`앱 화면 catalog 통과: 정적·동적 page ${pageRoutes.size}개`);
