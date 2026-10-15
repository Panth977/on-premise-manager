// Minimal stand-in for the OPM edge used by local-check.sh: /api/* -> API (prefix stripped), else -> WEB.
import http from 'node:http';
const [port, api, web] = process.argv.slice(2);
http.createServer((req, res) => {
  let target = web, path = req.url;
  if (path === '/api' || path.startsWith('/api/')) { target = api; path = path.slice(4) || '/'; }
  const u = new URL(target);
  const p = http.request({ host: u.hostname, port: u.port, method: req.method, path, headers: req.headers }, (r) => {
    res.writeHead(r.statusCode, r.headers); r.pipe(res);
  });
  p.on('error', (e) => { res.writeHead(502); res.end(String(e)); });
  req.pipe(p);
}).listen(Number(port), '127.0.0.1');
