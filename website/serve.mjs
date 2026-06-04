// Local preview server. The built site uses base-prefixed links (/laredo/...),
// so strip that prefix to map requests onto dist/.
import { createServer } from 'node:http';
import { readFileSync, existsSync, statSync } from 'node:fs';
import { join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('./dist/', import.meta.url));
const port = Number(process.env.PORT) || 8080;
const BASE = '/laredo';
const types = {
  '.html': 'text/html;charset=utf-8', '.css': 'text/css;charset=utf-8',
  '.js': 'application/javascript', '.json': 'application/json',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon',
};

createServer((req, res) => {
  let url = decodeURIComponent(req.url.split('?')[0]);
  if (url === BASE) url = '/';
  else if (url.startsWith(BASE + '/')) url = url.slice(BASE.length);
  let p = join(root, url);
  if (existsSync(p) && statSync(p).isDirectory()) p = join(p, 'index.html');
  if (!existsSync(p)) { res.statusCode = 404; res.end('not found'); return; }
  res.setHeader('content-type', types[extname(p)] || 'application/octet-stream');
  res.end(readFileSync(p));
}).listen(port, () => console.log(`Serving ${root} at http://localhost:${port}${BASE}/`));
