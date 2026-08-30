// 本地验收静态服务：模拟生产 /config alias + SPA fallback；/api* 反向代理到生产站拿真实数据（只读）
const http = require('http'); const fs = require('fs'); const path = require('path');
const ROOT = path.join(__dirname, 'site');
const TYPES = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.txt': 'text/plain', '.xml': 'application/xml' };
http.createServer(async (req, res) => {
  let url = decodeURIComponent(req.url.split('#')[0]);
  const pathOnly = url.split('?')[0];
  if (pathOnly.startsWith('/api')) {
    try { const r = await fetch('https://www.ntmminer.com' + url, { headers: { 'user-agent': 'ntm-local-preview' } }); const body = Buffer.from(await r.arrayBuffer()); res.writeHead(r.status, { 'content-type': r.headers.get('content-type') || 'application/json', 'cache-control': 'no-cache' }); res.end(body); }
    catch (e) { res.writeHead(502); res.end('proxy error ' + e.message); }
    return;
  }
  let file = path.join(ROOT, pathOnly === '/config' ? '/config.js' : pathOnly);
  if (!file.startsWith(ROOT)) { res.writeHead(403); res.end(); return; }
  if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(ROOT, 'index.html');
  // ★与生产 nginx 完全一致的 CSP：不带 unsafe-inline，内联 style/script 会被静默丢掉。
  // 本地不带这条头的话，「内联 style 的小圆点在生产不显示」这类 bug 只有上线后才暴露。
  res.writeHead(200, {
    'content-type': TYPES[path.extname(file)] || 'application/octet-stream',
    'cache-control': 'no-cache',
    'content-security-policy': "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'",
  });
  fs.createReadStream(file).pipe(res);
}).listen(8899, '127.0.0.1', () => console.log('serving on http://127.0.0.1:8899'));
