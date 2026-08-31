import { readFileSync } from 'node:fs';
globalThis.window = {}; globalThis.document = { documentElement: { lang: '' } };
globalThis.localStorage = { getItem(){return null;}, setItem(){} };
Object.defineProperty(globalThis, 'navigator', { value: { language: 'zh-CN' }, configurable: true });
new Function('window', readFileSync('site/config.js','utf8'))(globalThis.window);
const CFG = globalThis.window.NTM_CONFIG;
// 找出所有含 host+port 的数组，报出它们的路径
const found = [];
(function walk(v, path){
  if (Array.isArray(v)) {
    if (v.length && v[0] && typeof v[0]==='object' && 'host' in v[0] && 'port' in v[0]) found.push([path, v]);
    return v.forEach((x,i)=>walk(x, `${path}[${i}]`));
  }
  if (v && typeof v === 'object') return Object.entries(v).forEach(([k,x])=>walk(x, path?`${path}.${k}`:k));
})(CFG,'');
for (const [path, arr] of found) {
  const byMode = {}; for (const e of arr) byMode[e.mode] = (byMode[e.mode]||0)+1;
  const hosts=[...new Set(arr.map(e=>e.host.split('.')[0]))];
  console.log(`${path.padEnd(34)} n=${String(arr.length).padStart(2)}  ${JSON.stringify(byMode).padEnd(22)} 主机: ${hosts.join(', ')}`);
}
