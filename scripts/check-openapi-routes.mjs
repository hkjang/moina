#!/usr/bin/env node

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const server = await readFile(path.join(root, 'backend/internal/httpapi/server.go'), 'utf8');
const openapi = await readFile(path.join(root, 'api/openapi.yaml'), 'utf8');
const prefixes = { router: '', root: '', api: '/api/v1', auth: '/api/v1', admin: '/api/v1/admin' };
const methods = new Map([['Get', 'get'], ['Post', 'post'], ['Put', 'put'], ['Patch', 'patch'], ['Delete', 'delete']]);
const actual = new Set();

for (const line of server.split(/\r?\n/)) {
  const owner = Object.keys(prefixes).find((candidate) => new RegExp(`\\b${candidate}\\.`).test(line));
  if (!owner) continue;
  const direct = line.match(/\.(Get|Post|Put|Patch|Delete)\("([^"]+)"/);
  if (direct) actual.add(`${methods.get(direct[1])} ${prefixes[owner]}${direct[2]}`);
  const generic = line.match(/\.Method\(http\.Method(Get|Post|Put|Patch|Delete),\s*"([^"]+)"/);
  if (generic) actual.add(`${methods.get(generic[1])} ${prefixes[owner]}${generic[2]}`);
}

const documented = new Set();
let currentPath = '';
for (const line of openapi.split(/\r?\n/)) {
  const pathMatch = line.match(/^  (\/[^:]+):\s*$/);
  if (pathMatch) { currentPath = pathMatch[1]; continue; }
  const methodMatch = line.match(/^    (get|post|put|patch|delete):(?:\s|$)/);
  if (currentPath && methodMatch) documented.add(`${methodMatch[1]} ${currentPath}`);
}

const missing = [...actual].filter((route) => !documented.has(route)).sort();
const extra = [...documented].filter((route) => !actual.has(route)).sort();
if (missing.length || extra.length) {
  console.error('서버 route와 OpenAPI path가 일치하지 않습니다.');
  missing.forEach((route) => console.error(`- 문서 누락: ${route}`));
  extra.forEach((route) => console.error(`- 서버에 없음: ${route}`));
  process.exit(1);
}
console.log(`OpenAPI route 계약 통과: ${actual.size}개 method/path`);
