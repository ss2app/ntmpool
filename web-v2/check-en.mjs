// 英文首页/仪表盘渲染核查：确认新词条都有 en 值（不出现中文、不出现 {var} 或裸 key）
import { spawn } from 'node:child_process';
const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const ORIGIN = process.argv[2] || 'http://127.0.0.1:8899';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const chrome = spawn(CHROME, ['--headless=new','--disable-gpu','--no-sandbox','--window-size=1440,1000','--remote-debugging-port=9226','about:blank'], { stdio: 'ignore' });
await sleep(2500);
const page = (await (await fetch('http://127.0.0.1:9226/json')).json()).find((x) => x.type === 'page');
const ws = new WebSocket(page.webSocketDebuggerUrl);
let id = 0; const pending = new Map();
const send = (method, params = {}) => new Promise((res) => { const m = ++id; pending.set(m, res); ws.send(JSON.stringify({ id: m, method, params })); });
ws.onmessage = (e) => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m.result); pending.delete(m.id); } };
await new Promise((r) => { ws.onopen = r; });
const evaluate = async (expr) => (await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true })).result?.value;
await send('Page.enable');
await send('Page.navigate', { url: `${ORIGIN}/` });
await sleep(3000);
await evaluate('localStorage.setItem("ntm_lang","en"); true');
await send('Page.navigate', { url: `${ORIGIN}/` });
await sleep(4500);
let fail = 0;
for (const [label, sel] of [['home', 'main'], ['header', '#site-header']]) {
  const text = await evaluate(`document.querySelector('${sel}')?.innerText || ''`);
  const cjk = (text.match(/[\u4e00-\u9fff]+/g) || []).filter((s) => !['矿'].includes(s));
  const raw = text.match(/\{[a-zA-Z]+\}/g) || [];
  console.log(`[${label}] cjk=${cjk.length ? JSON.stringify(cjk.slice(0, 6)) : 'none'} rawVars=${raw.length ? JSON.stringify(raw) : 'none'}`);
  if (cjk.length || raw.length) fail += 1;
}
await send('Page.navigate', { url: `${ORIGIN}/midstate` });
await sleep(4500);
const dash = await evaluate("document.querySelector('#coin-panel')?.innerText || ''");
const dashCjk = dash.match(/[\u4e00-\u9fff]+/g) || [];
const dashRaw = dash.match(/\{[a-zA-Z]+\}/g) || [];
console.log(`[dashboard] cjk=${dashCjk.length ? JSON.stringify(dashCjk.slice(0, 6)) : 'none'} rawVars=${dashRaw.length ? JSON.stringify(dashRaw) : 'none'}`);
console.log('[dashboard sample]', dash.replace(/\s+/g, ' ').slice(0, 220));
if (dashCjk.length || dashRaw.length) fail += 1;
await evaluate('localStorage.setItem("ntm_lang","zh"); true');
console.log(fail ? `EN CHECK FAILURES: ${fail}` : 'EN CHECK PASS');
ws.close(); chrome.kill(); process.exit(fail ? 1 : 0);
