// 端到端渲染验收（CDP）：逐页加载、收集 console 错误、断言关键 DOM、操作生成器、截图（桌面 1280 + 手机 375）
// 用法: node e2e.mjs <origin> [shotPrefix]     例: node e2e.mjs http://127.0.0.1:8899 local
import { spawn } from 'node:child_process';
import { writeFileSync } from 'node:fs';

const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const ORIGIN = process.argv[2] || 'http://127.0.0.1:8899';
const PREFIX = process.argv[3] || 'local';
const EXTRA = (process.env.CHROME_EXTRA || '').split(' ').filter(Boolean);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-sandbox', '--hide-scrollbars', '--force-device-scale-factor=1',
  '--window-size=1280,1000', '--remote-debugging-port=9224', ...EXTRA, 'about:blank',
], { stdio: 'ignore' });

let failures = 0;
const check = (ok, label) => { console.log(`${ok ? '  ✓' : '  ✗'} ${label}`); if (!ok) failures += 1; };

async function main() {
  await sleep(2500);
  const list = await (await fetch('http://127.0.0.1:9224/json')).json();
  const page = list.find((t) => t.type === 'page');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  let id = 0;
  const pending = new Map();
  const consoleErrors = [];
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const mid = ++id;
    pending.set(mid, { resolve, reject });
    ws.send(JSON.stringify({ id: mid, method, params }));
  });
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result); }
    if (m.method === 'Runtime.exceptionThrown') consoleErrors.push(`exception: ${m.params.exceptionDetails.text} ${m.params.exceptionDetails.exception?.description || ''}`.slice(0, 300));
    if (m.method === 'Runtime.consoleAPICalled' && (m.params.type === 'error' || m.params.type === 'warning')) consoleErrors.push(`${m.params.type}: ${m.params.args.map((a) => a.value ?? a.description ?? '').join(' ')}`.slice(0, 300));
    if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') consoleErrors.push(`log: ${m.params.entry.text} ${m.params.entry.url || ''}`.slice(0, 300));
  };
  await new Promise((r) => { ws.onopen = r; });
  await send('Runtime.enable'); await send('Log.enable'); await send('Page.enable');
  const evaluate = async (expression) => (await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })).result.value;
  const goto = async (url, wait = 2500) => { consoleErrors.length = 0; await send('Page.navigate', { url }); await sleep(wait); };
  const shot = async (name, width) => {
    await send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width < 700 });
    await sleep(700);
    const h = Math.min(Number(await evaluate('document.documentElement.scrollHeight')) || 900, 6000);
    await send('Emulation.setDeviceMetricsOverride', { width, height: h, deviceScaleFactor: 1, mobile: width < 700 });
    await sleep(500);
    const overflow = await evaluate('JSON.stringify({iw: window.innerWidth, sw: document.documentElement.scrollWidth})');
    const png = await send('Page.captureScreenshot', { format: 'png' });
    writeFileSync(`C:/ntmbuild/webv4/shots/${PREFIX}-${name}-${width}.png`, Buffer.from(png.data, 'base64'));
    const o = JSON.parse(overflow);
    check(o.sw <= o.iw, `${name}@${width}: no horizontal overflow (scrollWidth ${o.sw} / innerWidth ${o.iw}) → shot saved`);
    await send('Emulation.clearDeviceMetricsOverride');
  };

  // ── /download 锄头卡片 ──
  console.log('\n[/download]');
  await goto(`${ORIGIN}/download`);
  check(consoleErrors.length === 0, `console clean ${consoleErrors.length ? JSON.stringify(consoleErrors) : ''}`);
  check(Number(await evaluate('document.querySelectorAll(".miner-card").length')) === 3, 'three miner cards');
  check(await evaluate('document.querySelector("h1")?.textContent') === '下载锄头', 'h1 = 下载锄头');
  check(Number(await evaluate('document.querySelectorAll(".coin-miner-table tbody tr").length')) === 3, 'by-coin table has 3 rows (juno/dragonx/midstate)');
  check((await evaluate('document.querySelector(".coin-miner-table")?.textContent') || '').includes('第三方锄头'), 'juno row shows 第三方锄头');
  check((await evaluate('[...document.querySelectorAll(".miner-card .status-badge")].map(e=>e.textContent.trim()).join("|")')) === '●0% 抽水|●0% 抽水|◷抽水 3%', `fee badges: ${await evaluate('[...document.querySelectorAll(".miner-card .status-badge")].map(e=>e.textContent.trim()).join("|")')}`);
  await shot('download', 1280); await shot('download', 375);

  // ── /download/noid 详情 + 生成器 ──
  for (const [minerId, addr, expectWin, expectSel] of [
    ['noid', 'o1z8q6nz9dyzucd7w8evv95aufpsunrkwjtmcy6hdf88hxfl28rpys86zlcz', 'ntmminer-noid.exe --rpc http://parano1d-pool.fun:3784 --coinbase o1z8q6nz9dyzucd7w8evv95aufpsunrkwjtmcy6hdf88hxfl28rpys86zlcz --worker rig1 --gpu', null],
    ['midstate', 'a'.repeat(64), `NTMminer-midstate-win-x64-v1.21.0.exe -a midstate -o hk.ntmminer.com:13333 -u ${'a'.repeat(64)} --worker rig1 --gpu-only`, null],
    ['ntmminer', 'DRGXTESTADDRESS', 'NTMminer-windows-x64-v1.21.1.exe -a rx/dragonx -o hk.ntmminer.com:3333 -u DRGXTESTADDRESS --worker rig1', null],
  ]) {
    console.log(`\n[/download/${minerId}]`);
    await goto(`${ORIGIN}/download/${minerId}`);
    check(consoleErrors.length === 0, `console clean ${consoleErrors.length ? JSON.stringify(consoleErrors) : ''}`);
    check(Boolean(await evaluate('document.querySelector(".miner-identity h1")')), 'miner header rendered');
    check(Number(await evaluate('document.querySelectorAll(".download-card").length')) === 2, 'two download cards');
    check(Number(await evaluate('document.querySelectorAll(".cli-table tbody tr").length')) > 10, `cli rows = ${await evaluate('document.querySelectorAll(".cli-table tbody tr").length')}`);
    check(Number(await evaluate('document.querySelectorAll("#mb-output .command-box").length')) === 4, 'builder emits 4 command boxes');
    const before = await evaluate('document.querySelector("#mb-output .command-box pre")?.textContent');
    check(before.includes('YOUR_') , `placeholder before typing: ${before}`);
    await evaluate(`(() => { const i = document.getElementById('mb-address'); i.value = ${JSON.stringify(addr)}; i.dispatchEvent(new Event('input', {bubbles:true})); return true; })()`);
    await sleep(200);
    const after = await evaluate('document.querySelector("#mb-output .command-box pre")?.textContent');
    check(after === expectWin, `windows command after typing: ${after}`);
    const warning = await evaluate('document.getElementById("mb-warning")?.textContent');
    check(warning === '', `no address warning (${warning})`);
    const bat = await evaluate('document.querySelectorAll("#mb-output .command-box pre")[2]?.textContent');
    check(bat.includes('%~dp0') && bat.includes('goto loop'), 'bat has %~dp0 + loop');
    const sh = await evaluate('document.querySelectorAll("#mb-output .command-box pre")[3]?.textContent');
    check(sh.startsWith('#!/bin/sh') && sh.includes('while true'), 'sh loop script');
    if (minerId === 'noid') {
      await evaluate(`(() => { const s = document.getElementById('mb-target'); s.value = 'ariabrain'; s.dispatchEvent(new Event('change', {bubbles:true})); return true; })()`);
      await sleep(200);
      const aria = await evaluate('document.querySelector("#mb-output .command-box pre")?.textContent');
      check(aria === 'ntmminer-noid.exe --rpc https://pool.ariabrain.com/noid-rpc/ --key o1z8q6nz9dyzucd7w8evv95aufpsunrkwjtmcy6hdf88hxfl28rpys86zlcz.rig1 --gpu', `ariabrain form: ${aria}`);
      await evaluate(`(() => { const s = document.getElementById('mb-mode'); s.value = 'cpu'; s.dispatchEvent(new Event('change', {bubbles:true})); return true; })()`);
      await sleep(200);
      const cpu = await evaluate('document.querySelector("#mb-output .command-box pre")?.textContent');
      check(!cpu.includes('--gpu'), `cpu mode drops --gpu: ${cpu}`);
      await evaluate(`(() => { const i = document.getElementById('mb-address'); i.value = 'BADADDR'; i.dispatchEvent(new Event('input', {bubbles:true})); return true; })()`);
      await sleep(200);
      check((await evaluate('document.getElementById("mb-warning")?.textContent')).length > 0, 'bad address shows warning');
    }
    if (minerId === 'ntmminer') {
      await evaluate(`(() => { const s = document.getElementById('mb-target'); s.value = 'custom'; s.dispatchEvent(new Event('change', {bubbles:true})); return true; })()`);
      await sleep(200);
      await evaluate(`(() => { const a = document.getElementById('mb-algo'); a.value = 'rx/kad'; a.dispatchEvent(new Event('change', {bubbles:true})); const p = document.getElementById('mb-pool'); p.value = 'pool.example.com:4444'; p.dispatchEvent(new Event('input', {bubbles:true})); return true; })()`);
      await sleep(200);
      const custom = await evaluate('document.querySelector("#mb-output .command-box pre")?.textContent');
      check(custom === 'NTMminer-windows-x64-v1.21.1.exe -a rx/kad -o pool.example.com:4444 -u DRGXTESTADDRESS --worker rig1', `custom pool: ${custom}`);
    }
    await shot(minerId, 1280); await shot(minerId, 375);
  }

  // ── 币页 connect：命令用对应锄头文件名；第 2 步链接到锄头页 ──
  for (const [coin, expectBin, expectHref] of [['dragonx', 'NTMminer-windows-x64-v1.21.1.exe -a rx/dragonx', '/download/ntmminer'], ['midstate', 'NTMminer-midstate-win-x64-v1.21.0.exe -a midstate', '/download/midstate'], ['juno', 'junorig.exe -a rx/juno', '/download']]) {
    console.log(`\n[/${coin}#connect]`);
    await goto(`${ORIGIN}/${coin}#connect`, 3500);
    const errs = consoleErrors.filter((e) => !/api|502|Failed to load resource|fetch/i.test(e));
    check(errs.length === 0, `console clean (api errors ignored locally) ${errs.length ? JSON.stringify(errs) : ''}`);
    const cmd = await evaluate('document.querySelector("#connect-commands .command-box pre")?.textContent');
    check((cmd || '').startsWith(expectBin), `connect command: ${cmd}`);
    const href = await evaluate('document.querySelector(".connect-steps li:nth-child(2) a")?.getAttribute("href")');
    check(href === expectHref, `step-2 link: ${href}`);
    check(Number(await evaluate('document.querySelectorAll("#connect-commands .settlement-list dt").length')) > 0, `mode parameter rows = ${await evaluate('document.querySelectorAll("#connect-commands .settlement-list dt").length')}`);
  }

  // ── 首页（v3b 换骨后：状态条 + 矿池账本行 + 三步开挖 + 抽水公示）──
  console.log('\n[/]');
  await goto(`${ORIGIN}/`, 4000);
  const coinCount = Number(await evaluate('Object.values(window.NTM_CONFIG.coins).filter(c => c.enabled).length'));
  check(Number(await evaluate('document.querySelectorAll("#home-pools .pool-row:not(.pool-row-loading)").length')) === coinCount, `pool ledger rows = ${coinCount}`);
  check(Number(await evaluate('document.querySelectorAll("#home-strip .strip-metric").length')) === 3 && Number(await evaluate('document.querySelectorAll("#home-strip .strip-live").length')) === 1, `home strip filled: ${(await evaluate('document.getElementById("home-strip")?.textContent || ""')).replace(/\s+/g, ' ').trim().slice(0, 90)}`);
  check(Number(await evaluate('document.querySelectorAll("#home-pools .spark-host svg, #home-pools .spark-host .chart-empty").length')) === coinCount, 'sparkline host filled per row');
  check(Number(await evaluate('document.querySelectorAll(".home-steps li").length')) === 3, 'three steps rendered');
  check((await evaluate('document.querySelector(".home-start-command .command-box code")?.textContent || ""')).includes(' -o '), 'home start command built');
  const fee = await evaluate('[...document.querySelectorAll(".fee-item")].map(e => e.textContent).join(" | ")');
  check(fee.includes('NTMminer') && fee.includes('3%'), `fee disclosure: ${fee.replace(/\s+/g, ' ').slice(0, 160)}`);
  // 头部：币种切换器 + 地址搜索
  check(Number(await evaluate('document.querySelectorAll(".coin-switcher .coin-chip").length')) === coinCount, 'coin switcher chips');
  check(await evaluate('!!document.getElementById("header-search-input")'), 'header search present');
  // 搜索框端到端：填一个格式合法但不存在的地址 → 三个池都查不到 → 提示未找到（验证真的发起了查询）
  await evaluate('window.__ntmToast = ""; new MutationObserver(() => { const el = document.querySelector(".toast"); if (el) window.__ntmToast = el.textContent; }).observe(document.getElementById("toast-region"), { childList: true }); true');
  await evaluate('const i = document.getElementById("header-search-input"); i.value = "NTME2ENOSUCHADDRESS0000000000"; i.dispatchEvent(new Event("input", { bubbles: true })); document.getElementById("header-search").requestSubmit(); true');
  let toast = '';
  for (let attempt = 0; attempt < 15 && !toast; attempt += 1) { await sleep(500); toast = await evaluate('window.__ntmToast || ""'); }
  check(toast.length > 0 && !toast.includes('{'), `search miss toast: ${toast.slice(0, 60)}`);
  check(await evaluate('location.pathname') === '/', 'search miss stays on home');
  // 币页仪表盘：状态条 + 4 格 band + bento
  await goto(`${ORIGIN}/midstate`, 4000);
  check(Number(await evaluate('document.querySelectorAll(".stat-band .band-cell").length')) === 4, 'dashboard stat band = 4 cells');
  check(Number(await evaluate('document.querySelectorAll(".coin-strip .strip-metric").length')) > 0, 'dashboard strip metrics');
  check(Number(await evaluate('document.querySelectorAll(".bento > *").length')) === 4, 'dashboard bento blocks = 4');
  check(Number(await evaluate('document.querySelectorAll(".bento .table-flat tbody tr").length')) > 0, 'dashboard recent-blocks table rows');
  check(!(await evaluate('document.querySelector(".kv-card")?.textContent || ""')).includes('undefined'), 'kv cards clean');
  await goto(`${ORIGIN}/`, 3000);
  // 英文切换
  await evaluate('document.getElementById("language-button").click(); true');
  await sleep(800);
  await goto(`${ORIGIN}/download/noid`, 2500);
  check(await evaluate('document.querySelector("h1")?.textContent') === 'NTMminer-noid' && (await evaluate('document.querySelector(".miner-tagline")?.textContent')).includes('dedicated'), 'english renders on miner page');
  check(Number(await evaluate('document.querySelectorAll("#mb-output .command-box").length')) === 4, 'builder works in english');
  await evaluate('localStorage.setItem("ntm_lang","zh"); true');

  console.log(`\n${failures ? `FAILURES: ${failures}` : 'ALL PASS'}`);
  chrome.kill();
  process.exit(failures ? 1 : 0);
}
main().catch((e) => { console.error(e); chrome.kill(); process.exit(1); });
