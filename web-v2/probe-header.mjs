import { spawn } from 'node:child_process';
const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const ORIGIN = process.argv[2] || 'https://www.ntmminer.com';
const LANG = process.argv[3] || 'en';
const W = Number(process.argv[4] || 1440);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const chrome = spawn(CHROME, ['--headless=new','--disable-gpu','--no-sandbox',`--window-size=${W},1000`,'--remote-debugging-port=9227','about:blank'], { stdio: 'ignore' });
await sleep(2500);
const page = (await (await fetch('http://127.0.0.1:9227/json')).json()).find((x) => x.type === 'page');
const ws = new WebSocket(page.webSocketDebuggerUrl);
let id = 0; const pending = new Map();
const send = (method, params = {}) => new Promise((res) => { const m = ++id; pending.set(m, res); ws.send(JSON.stringify({ id: m, method, params })); });
ws.onmessage = (e) => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m.result); pending.delete(m.id); } };
await new Promise((r) => { ws.onopen = r; });
const evaluate = async (expr) => (await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true })).result?.value;
await send('Page.enable');
await send('Emulation.setDeviceMetricsOverride', { width: W, height: 1000, deviceScaleFactor: 1, mobile: false });
await send('Page.navigate', { url: `${ORIGIN}/` });
await sleep(3000);
await evaluate(`localStorage.setItem("ntm_lang","${LANG}"); true`);
await send('Page.navigate', { url: `${ORIGIN}/` });
await sleep(5000);
const info = await evaluate(`(() => {
  const out = {};
  const q = (s) => document.querySelector(s);
  for (const [k, s] of [['cta','.header-cta'],['full','.header-cta .full-label'],['short','.header-cta .short-label'],['lang','.header-language'],['brand','.brand-lockup'],['inner','.site-header .container'],['switcher','.coin-switcher'],['search','.header-search'],['actions','.header-actions']]) {
    const el = q(s); if (!el) { out[k] = null; continue; }
    const r = el.getBoundingClientRect(); const cs = getComputedStyle(el);
    out[k] = { w: Math.round(r.width), h: Math.round(r.height), x: Math.round(r.left), fs: cs.fontSize, disp: cs.display, text: (el.innerText||'').slice(0,30) };
  }
  out.headerScrollW = document.querySelector('.site-header')?.scrollWidth;
  out.bodyScrollW = document.documentElement.scrollWidth;
  out.win = innerWidth;
  return out;
})()`);
console.log(JSON.stringify(info, null, 1));
ws.close(); chrome.kill(); process.exit(0);
