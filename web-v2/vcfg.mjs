import { readFileSync } from 'node:fs';
globalThis.window = {};
globalThis.document = { documentElement: { lang: '' } };
globalThis.localStorage = { getItem() { return null; }, setItem() {} };
Object.defineProperty(globalThis, 'navigator', { value: { language: 'zh-CN' }, configurable: true });
new Function('window', readFileSync('site/config.js', 'utf8'))(globalThis.window);
const CFG = globalThis.window.NTM_CONFIG;
const { validateRuntimeConfig } = await import('./site/js/api.20260720d.js');
const errors = validateRuntimeConfig(CFG);
console.log('validateRuntimeConfig errors:', errors.length, errors.length ? errors : '');

// ★ 递归 walk 整个已解析对象找残留（只 grep 文件会漏——池费藏在 highlights[2].body 那次教训）
const hits = [];
(function walk(v, path) {
  if (typeof v === 'string') { if (/(hk3|sgp|ca)\.ntmminer\.com/.test(v)) hits.push(path + ' = ' + v.slice(0, 120)); return; }
  if (Array.isArray(v)) return v.forEach((x, i) => walk(x, `${path}[${i}]`));
  if (v && typeof v === 'object') return Object.entries(v).forEach(([k, x]) => walk(x, path ? `${path}.${k}` : k));
})(CFG, '');
console.log('递归 walk 残留提及:', hits.length, hits.length ? hits : '无');

// 每个启用的币，按 mode 统计剩下几个接入点
console.log('\n每币剩余接入点：');
for (const [id, c] of Object.entries(CFG.coins)) {
  const eps = c?.mining?.endpoints || [];
  const byMode = {};
  for (const e of eps) byMode[e.mode] = (byMode[e.mode] || 0) + 1;
  const hosts = [...new Set(eps.map((e) => e.host.split('.')[0]))];
  console.log(`  ${id.padEnd(10)} 共 ${String(eps.length).padStart(2)} 个  ${JSON.stringify(byMode).padEnd(24)} 主机: ${hosts.join(', ')}`);
}
if (errors.length || hits.length) process.exit(1);
