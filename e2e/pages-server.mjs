import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { createServer } from 'node:http';
import { extname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('../docs', import.meta.url)));
const host = process.env.MOINA_PAGES_HOST || '127.0.0.1';
const port = Number(process.env.MOINA_PAGES_PORT || 4173);
const types = {
  '.css': 'text/css; charset=utf-8', '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml', '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json; charset=utf-8', '.webp': 'image/webp', '.png': 'image/png',
  '.xml': 'application/xml; charset=utf-8',
};

const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url || '/', `http://${host}:${port}`);
    const relative = decodeURIComponent(url.pathname).replace(/^[/\\]+/, '');
    let target = resolve(root, relative || 'index.html');
    if (!target.startsWith(`${root}${sep}`)) return response.writeHead(403).end('Forbidden');
    if ((await stat(target)).isDirectory()) target = join(target, 'index.html');
    const info = await stat(target);
    response.writeHead(200, { 'Cache-Control': 'no-store', 'Content-Length': info.size, 'Content-Type': types[extname(target)] || 'application/octet-stream' });
    createReadStream(target).pipe(response);
  } catch {
    response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }).end('Not Found');
  }
});
server.listen(port, host, () => console.log(`Pages test server: http://${host}:${port}`));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => server.close(() => process.exit(0)));
