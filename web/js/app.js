/* NTM pool frontend — 路由 + 渲染 + 图表。纯静态，数据全部来自 /api（NTMPool 公共 API）。 */
(() => {
  'use strict';
  const CFG = window.NTM_CONFIG;
  const I18N = window.NTM_I18N;

  // ---------- i18n ----------
  let lang = localStorage.getItem('ntm_lang');
  if (lang !== 'zh' && lang !== 'en') {
    lang = ((navigator.language || '').toLowerCase().startsWith('zh')) ? 'zh' : 'en';
  }
  const t = (k) => (I18N[lang] && I18N[lang][k]) || I18N.en[k] || k;

  // ---------- helpers ----------
  const $ = (sel, root) => (root || document).querySelector(sel);
  const esc = (s) => String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

  const HR_UNITS = ['H/s', 'KH/s', 'MH/s', 'GH/s', 'TH/s', 'PH/s'];
  function fmtHR(v) {
    v = Number(v) || 0;
    let i = 0;
    while (v >= 1000 && i < HR_UNITS.length - 1) { v /= 1000; i++; }
    const digits = v >= 100 ? 1 : 2;
    return `<span>${v.toFixed(digits)}</span><small>${HR_UNITS[i]}</small>`;
  }
  function fmtHRText(v) {
    v = Number(v) || 0;
    let i = 0;
    while (v >= 1000 && i < HR_UNITS.length - 1) { v /= 1000; i++; }
    return `${v.toFixed(v >= 100 ? 1 : 2)} ${HR_UNITS[i]}`;
  }
  function fmtBig(v) {
    v = Number(v) || 0;
    const u = ['', 'K', 'M', 'G', 'T', 'P'];
    let i = 0;
    while (v >= 1000 && i < u.length - 1) { v /= 1000; i++; }
    return `${v.toFixed(v >= 100 || i === 0 ? 0 : 2)}${u[i] ? ' ' + u[i] : ''}`;
  }
  function fmtAmt(v) {
    const n = Number(v);
    if (!isFinite(n)) return '0';
    const s = n >= 1000 ? n.toFixed(2) : n.toFixed(4);
    return s.replace(/\.?0+$/, '');
  }
  const fmtInt = (v) => (Number(v) || 0).toLocaleString('en-US');
  function fmtTime(iso) {
    const d = new Date(iso);
    if (isNaN(d)) return '—';
    const p = (x) => String(x).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  function timeAgo(iso) {
    const s = (Date.now() - new Date(iso).getTime()) / 1000;
    if (!isFinite(s) || s < 0) return fmtTime(iso);
    if (s < 90) return t('just_now');
    if (s < 5400) return `${Math.round(s / 60)} ${t('min_ago')}`;
    if (s < 129600) return `${Math.round(s / 3600)} ${t('hr_ago')}`;
    return `${Math.round(s / 86400)} ${t('day_ago')}`;
  }
  function shortHex(h, copyable) {
    if (!h) return '—';
    const short = h.length > 18 ? `${h.slice(0, 8)}…${h.slice(-6)}` : h;
    if (!copyable) return `<span class="mono">${esc(short)}</span>`;
    return `<span class="mono" title="${esc(h)}">${esc(short)}</span> <button class="copy-btn" style="position:static;padding:2px 7px" data-copy="${esc(h)}">⧉</button>`;
  }
  function bindCopy(root) {
    (root || document).querySelectorAll('[data-copy]').forEach((b) => {
      b.addEventListener('click', () => {
        navigator.clipboard.writeText(b.getAttribute('data-copy')).then(() => {
          const old = b.textContent;
          b.textContent = t('copied');
          setTimeout(() => { b.textContent = old; }, 1200);
        });
      });
    });
  }

  // ---------- api ----------
  // 多实例：币可在 config.js 里声明独立 apiBase（如 btc09 走 /api-btc09），
  // 未声明的走全局 CFG.api.base。
  function apiBaseFor(coinId) {
    const c = coinId && CFG.coins && CFG.coins[coinId];
    return (c && c.apiBase) || CFG.api.base;
  }
  async function api(path, coinId) {
    const r = await fetch(apiBaseFor(coinId) + path, { headers: { Accept: 'application/json' } });
    if (!r.ok) { const e = new Error(`HTTP ${r.status}`); e.status = r.status; throw e; }
    const total = parseInt(r.headers.get('X-Total-Count') || '', 10);
    const data = await r.json();
    return { data, total: isNaN(total) ? null : total };
  }

  // ---------- state ----------
  const state = { route: null, coin: null, tab: 'dashboard', pools: null, timer: null, charts: [] };
  function destroyCharts() {
    state.charts.forEach((c) => { try { c.destroy(); } catch (_) {} });
    state.charts = [];
  }

  // ---------- charts ----------
  function hashrateChart(elId, samples, seriesName, color) {
    let pts = (samples || []).map((s) => [new Date(s.created).getTime(), Math.round(s.hashrate || s.poolHashrate || 0)]);
    // 丢掉当前未采满的 10 分钟桶（否则曲线末尾会假跌到 0）
    const BUCKET = 10 * 60 * 1000;
    while (pts.length && Date.now() - pts[pts.length - 1][0] < BUCKET) pts = pts.slice(0, -1);
    const nonzero = pts.filter((p) => p[1] > 0);
    const box = document.getElementById(elId);
    if (!box) return;
    if (nonzero.length < 2) {
      box.innerHTML = `<div class="chart-empty">${esc(t('chart_no_data'))}</div>`;
      return;
    }
    const chart = new ApexCharts(box, {
      chart: { type: 'area', height: 290, background: 'transparent', foreColor: '#898781',
        toolbar: { show: false }, zoom: { enabled: false }, animations: { enabled: false } },
      series: [{ name: seriesName, data: pts }],
      colors: [color || '#3987e5'],
      stroke: { curve: 'smooth', width: 2 },
      fill: { type: 'gradient', gradient: { opacityFrom: 0.28, opacityTo: 0.02 } },
      dataLabels: { enabled: false },
      grid: { borderColor: '#2c2c2a', strokeDashArray: 0, xaxis: { lines: { show: false } } },
      markers: { size: 0, hover: { size: 4 } },
      xaxis: { type: 'datetime', labels: { datetimeUTC: false, style: { colors: '#898781' } },
        axisBorder: { color: '#383835' }, axisTicks: { color: '#383835' }, tooltip: { enabled: false } },
      yaxis: { labels: { formatter: (v) => fmtHRText(v), style: { colors: '#898781' } }, min: 0 },
      tooltip: { theme: 'dark', x: { format: 'MM-dd HH:mm' }, y: { formatter: (v) => fmtHRText(v) } },
    });
    chart.render();
    state.charts.push(chart);
  }

  // ---------- shared bits ----------
  const GH_ICON = '<svg viewBox="0 0 16 16"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>';
  const DL_ICON = '<svg viewBox="0 0 16 16"><path d="M8 12 3 7l1.4-1.4L7 8.2V0h2v8.2l2.6-2.6L13 7l-5 5zm-6 2h12v2H2v-2z"/></svg>';

  function coinMeta(id) { return CFG.coins[id] || null; }
  function coinLogo(id, meta, size) {
    const s = size || 40;
    if (meta && meta.logo) {
      return `<span class="coin-logo" style="background:#0d0d0d;border:1px solid rgba(255,255,255,0.10);width:${s}px;height:${s}px"><img src="${esc(meta.logo)}" alt="${esc(meta.symbol || id)}" style="width:72%;height:72%;object-fit:contain"></span>`;
    }
    const sym = meta ? meta.symbol : id.toUpperCase();
    const color = (meta && meta.color) || '#3987e5';
    return `<span class="coin-logo" style="background:${esc(color)};width:${s}px;height:${s}px;font-size:${Math.round(s * 0.36)}px">${esc(sym.slice(0, 4))}</span>`;
  }
  function coinDesc(meta) { return lang === 'zh' ? meta.descZh : meta.descEn; }

  // ---------- nav ----------
  function renderNav() {
    $('#nav-home').textContent = t('nav_home');
    $('#lang-btn').textContent = lang === 'zh' ? 'EN' : '中文';
  }

  // ---------- home ----------
  async function renderHome() {
    document.title = lang === 'zh' ? 'NTM 矿池 — 高效透明的多币种矿池' : 'NTM Pools — efficient, transparent multi-coin mining';
    const app = $('#app');
    app.innerHTML = `
      <section class="hero wrap">
        <a class="gh-banner" href="${esc(CFG.brand.githubMiner)}" target="_blank" rel="noopener">
          ${GH_ICON}<span>${esc(t('gh_banner'))}</span><span class="url">${esc(CFG.brand.githubMinerLabel)}</span>
        </a>
        <h1>${esc(t('hero_title_pre'))} <span class="expansion">· ${esc(CFG.brand.expansionEn)}</span><br>${esc(t('hero_title_post'))}</h1>
        <p class="sub">${esc(t('hero_sub'))}</p>
        <div class="hero-cta">
          <a class="btn primary" href="${esc(CFG.brand.githubMiner)}/releases/latest" target="_blank" rel="noopener">${DL_ICON}${esc(t('hero_cta_download'))}</a>
          <a class="btn" href="/dragonx" data-nav>${esc(t('hero_cta_start'))}</a>
        </div>
        <div class="stat-strip" id="home-strip">
          ${['strip_coins', 'strip_miners', 'strip_blocks'].map((k) =>
            `<div class="tile"><div class="k">${esc(t(k))}</div><div class="v skeleton loading">—</div></div>`).join('')}
        </div>
        <h2 class="section-title">${esc(t('pools_title'))}</h2>
        <div class="cards" id="home-cards"><div class="tile skeleton loading">…</div></div>
        <div class="features">
          <div class="feature"><div class="t">${esc(t('feat_1_t'))}</div><div class="d">${esc(t('feat_1_d'))}</div></div>
          <div class="feature"><div class="t">${esc(t('feat_2_t'))}</div><div class="d">${esc(t('feat_2_d'))}</div></div>
          <div class="feature"><div class="t">${esc(t('feat_3_t'))}</div><div class="d">${esc(t('feat_3_d'))}</div></div>
        </div>
      </section>`;
    await refreshHome();
  }

  async function refreshHome() {
    // 跨实例聚合：全局 base + 各币独立 apiBase 各拉一次 /pools，按 id 去重合并。
    const bases = [...new Set([CFG.api.base]
      .concat(Object.values(CFG.coins || {}).map((c) => c.apiBase).filter(Boolean)))];
    const results = await Promise.allSettled(bases.map(async (b) => {
      const r = await fetch(b + '/pools', { headers: { Accept: 'application/json' } });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = await r.json();
      return (d && d.pools) || [];
    }));
    const seen = new Set();
    const pools = [];
    for (const res of results) {
      if (res.status !== 'fulfilled') continue;
      for (const p of res.value) if (p && p.id && !seen.has(p.id)) { seen.add(p.id); pools.push(p); }
    }
    if (!pools.length && results.every((r) => r.status === 'rejected')) {
      const cards = $('#home-cards');
      if (cards) cards.innerHTML = `<div class="err-box">${esc(t('err_api'))}</div>`;
      return;
    }
    state.pools = pools;
    const strip = $('#home-strip');
    if (strip) {
      const miners = pools.reduce((a, p) => a + ((p.poolStats && p.poolStats.connectedMiners) || 0), 0);
      const blocks = pools.reduce((a, p) => a + (p.totalBlocks || 0), 0);
      const vals = [pools.length, miners, blocks];
      strip.querySelectorAll('.tile .v').forEach((el, i) => { el.classList.remove('skeleton', 'loading'); el.textContent = fmtInt(vals[i]); });
    }
    const cards = $('#home-cards');
    if (!cards) return;
    let html = '';
    for (const p of pools) {
      const meta = coinMeta(p.id) || { symbol: (p.coin && p.coin.symbol) || p.id.toUpperCase(), name: (p.coin && p.coin.name) || p.id, algo: (p.coin && p.coin.algorithm) || '', color: '#3987e5' };
      const ps = p.poolStats || {}, ns = p.networkStats || {};
      html += `
        <a class="coin-card" href="/${esc(p.id)}" data-nav>
          <div class="head">${coinLogo(p.id, meta)}
            <div><div class="n">${esc(meta.name)} <span style="color:var(--muted);font-weight:500">${esc(meta.symbol)}</span></div>
            <div class="a">${esc(meta.algo)}</div></div>
            <span class="chip live" style="margin-left:auto">${esc(t('live'))}</span>
          </div>
          <div class="grid">
            <div><div class="k">${esc(t('card_hashrate'))}</div><div class="v">${fmtHR(ps.poolHashrate)}</div></div>
            <div><div class="k">${esc(t('card_miners_rigs'))}</div><div class="v">${fmtInt(ps.connectedMiners)}<span style="color:var(--muted);font-weight:500"> / ${fmtInt(ps.connectedWorkers)}</span></div></div>
            <div><div class="k">${esc(t('card_fee'))}</div><div class="v">${esc(String(p.poolFeePercent))}%</div></div>
            <div><div class="k">${esc(t('card_height'))}</div><div class="v">${fmtInt(ns.blockHeight)}</div></div>
          </div>
          <div class="foot"><span>
            <span class="chip">${esc((p.paymentProcessing && p.paymentProcessing.payoutScheme) || 'PPLNS')} ${esc(String(p.poolFeePercent))}%</span>${Object.values(p.ports || {}).some((x) => x && x.solo) ? ` <span class="chip">SOLO ${esc(String(p.soloFeePercent != null ? p.soloFeePercent : p.poolFeePercent))}%</span>` : ''}
          </span><span class="enter">${esc(t('card_enter'))}</span></div>
        </a>`;
    }
    html += `
      <div class="coin-card" style="opacity:.75">
        <div class="head"><span class="coin-logo" style="background:var(--surface-2);color:var(--muted)">+</span>
          <div><div class="n">${esc(t('card_soon_t'))}</div><div class="a">${esc(t('card_soon_d'))}</div></div>
          <span class="chip soon" style="margin-left:auto">${esc(t('soon'))}</span>
        </div>
      </div>`;
    cards.innerHTML = html;
  }

  // ---------- coin page ----------
  const TABS = ['dashboard', 'blocks', 'payments', 'miners', 'lookup', 'connect'];

  async function renderCoin(coinId) {
    const meta = coinMeta(coinId);
    document.title = `${meta ? meta.name : coinId} — NTM ${lang === 'zh' ? '矿池' : 'Pool'}`;
    const app = $('#app');
    const links = meta && meta.links ? `
      <div class="links">
        ${meta.links.site ? `<a class="btn ghost" href="${esc(meta.links.site)}" target="_blank" rel="noopener">${esc(t('coin_links_site'))} ↗</a>` : ''}
        ${meta.links.git ? `<a class="btn ghost" href="${esc(meta.links.git)}" target="_blank" rel="noopener">${esc(t('coin_links_git'))} ↗</a>` : ''}
      </div>` : '';
    app.innerHTML = `
      <div class="wrap">
        <div class="coin-head">
          ${coinLogo(coinId, meta, 46)}
          <div>
            <h1>${esc(meta ? meta.name : coinId)} <span style="color:var(--muted);font-size:18px;font-weight:500">${esc(meta ? meta.symbol : '')}</span></h1>
            <span class="chip">${esc(meta ? meta.algo : '')}</span> <span class="chip live">${esc(t('live'))}</span>
          </div>
          ${links}
        </div>
        <div class="tabs" id="tabs">
          ${TABS.map((tb) => `<button data-tab="${tb}" class="${tb === state.tab ? 'active' : ''}">${esc(t('tab_' + tb))}</button>`).join('')}
        </div>
        <div id="tab-body"></div>
      </div>`;
    $('#tabs').addEventListener('click', (e) => {
      const b = e.target.closest('button[data-tab]');
      if (!b) return;
      state.tab = b.getAttribute('data-tab');
      history.replaceState(null, '', `/${coinId}#${state.tab}`);
      $('#tabs').querySelectorAll('button').forEach((x) => x.classList.toggle('active', x === b));
      renderTab(coinId);
    });
    await renderTab(coinId);
  }

  async function renderTab(coinId) {
    destroyCharts();
    const body = $('#tab-body');
    body.innerHTML = `<div class="panel skeleton loading">…</div>`;
    try {
      if (state.tab === 'dashboard') await tabDashboard(coinId, body);
      else if (state.tab === 'blocks') await tabPaged(coinId, body, 'blocks');
      else if (state.tab === 'payments') await tabPaged(coinId, body, 'payments');
      else if (state.tab === 'miners') await tabMiners(coinId, body);
      else if (state.tab === 'lookup') tabLookup(coinId, body);
      else if (state.tab === 'connect') await tabConnect(coinId, body);
    } catch (_) {
      body.innerHTML = `<div class="err-box">${esc(t('err_api'))}</div>`;
    }
  }

  async function tabDashboard(coinId, body) {
    const [poolR, perfR] = await Promise.all([
      api(`/pools/${encodeURIComponent(coinId)}`, coinId),
      api(`/pools/${encodeURIComponent(coinId)}/performance`, coinId),
    ]);
    const p = poolR.data.pool || poolR.data;
    const meta = coinMeta(coinId) || {};
    const ps = p.poolStats || {}, ns = p.networkStats || {}, pay = p.paymentProcessing || {};
    const share = ns.networkHashrate > 0 ? (ps.poolHashrate / ns.networkHashrate * 100) : 0;
    const sym = (p.coin && p.coin.symbol) || meta.symbol || '';
    const tiles = [
      [t('stat_pool_hashrate'), fmtHR(ps.poolHashrate)],
      [t('stat_net_hashrate'), fmtHR(ns.networkHashrate)],
      [t('stat_pool_share'), `<span>${share.toFixed(2)}</span><small>%</small>`],
      [t('stat_miners_online'), `<span>${fmtInt(ps.connectedMiners)}</span>`],
      [t('stat_rigs_online'), `<span>${fmtInt(ps.connectedWorkers)}</span>`],
      [t('stat_height'), `<span>${fmtInt(ns.blockHeight)}</span>`],
      [t('stat_net_diff'), `<span>${esc(fmtBig(ns.networkDifficulty))}</span>`],
      [t('stat_blocks_found'), `<span>${fmtInt(p.totalBlocks)}</span>`, `${t('stat_confirmed')}: ${fmtInt(p.totalConfirmedBlocks)}`],
      [t('stat_total_paid'), `<span>${esc(fmtAmt(p.totalPaid))}</span><small>${esc(sym)}</small>`],
      [t('stat_fee'), `<span>${esc(String(p.poolFeePercent))}</span><small>%</small>`,
        `${t('stat_scheme')}: ${esc(pay.payoutScheme || 'PPLNS')}${p.soloFeePercent != null ? ` · SOLO ${p.soloFeePercent}%` : ''}`],
      [t('stat_min_payout'), `<span>${esc(fmtAmt(pay.minimumPayment))}</span><small>${esc(sym)}</small>`],
    ];
    body.innerHTML = `
      <div class="stat-strip">
        ${tiles.map(([k, v, hint]) => `<div class="tile"><div class="k">${esc(k)}</div><div class="v">${v}</div>${hint ? `<div class="hint">${esc(hint)}</div>` : ''}</div>`).join('')}
      </div>
      <div class="panel">
        <h3>${esc(t('chart_pool_hashrate'))}</h3>
        <div class="chart-box" id="pool-chart"></div>
      </div>
      ${coinMeta(coinId) ? `<div class="panel"><h3>${esc(t('coin_about'))} ${esc(meta.name)}</h3><div class="note" style="font-size:14px;color:var(--ink-2)">${esc(coinDesc(meta))}</div></div>` : ''}`;
    hashrateChart('pool-chart', (perfR.data && perfR.data.stats) || [], t('stat_pool_hashrate'), '#3987e5');
  }

  async function tabPaged(coinId, body, kind) {
    const pageSize = 15;
    let page = 0, total = null;
    const load = async () => {
      const r = await api(`/pools/${encodeURIComponent(coinId)}/${kind}?page=${page}&pageSize=${pageSize}`, coinId);
      total = r.total;
      const rows = Array.isArray(r.data) ? r.data : [];
      const meta = coinMeta(coinId) || {};
      const sym = meta.symbol || '';
      let table;
      if (kind === 'blocks') {
        table = rows.length ? `
          <div class="tbl-wrap"><table class="tbl">
            <tr><th>${esc(t('col_height'))}</th><th>${esc(t('col_status'))}</th><th class="num">${esc(t('col_reward'))}</th><th>${esc(t('col_miner'))}</th><th>${esc(t('col_time'))}</th><th>${esc(t('col_hash'))}</th></tr>
            ${rows.map((b) => {
              const st = b.status || 'pending';
              const stLabel = t('status_' + st) || st;
              const prog = st === 'pending' ? ` ${Math.round((b.confirmationProgress || 0) * 100)}%` : '';
              const soloTag = b.solo ? ' <span class="chip" style="font-size:11px;padding:1px 7px">SOLO</span>' : '';
              return `<tr>
                <td class="mono">${fmtInt(b.blockHeight)}</td>
                <td><span class="status ${esc(st)}">${esc(stLabel)}${prog}</span>${soloTag}</td>
                <td class="num">${esc(fmtAmt(b.reward))} ${esc(sym)}</td>
                <td class="mono">${esc(b.miner || '—')}</td>
                <td title="${esc(fmtTime(b.created))}">${esc(timeAgo(b.created))}</td>
                <td>${shortHex(b.hash, true)}</td></tr>`;
            }).join('')}
          </table></div>` : `<div class="chart-empty">${esc(t('empty_blocks'))}</div>`;
      } else {
        table = rows.length ? `
          <div class="tbl-wrap"><table class="tbl">
            <tr><th>${esc(t('col_time'))}</th><th>${esc(t('col_address'))}</th><th class="num">${esc(t('col_amount'))}</th><th>${esc(t('col_status'))}</th><th>${esc(t('col_txid'))}</th></tr>
            ${rows.map((x) => {
              const st = x.status || 'sent';
              return `<tr>
                <td title="${esc(fmtTime(x.created))}">${esc(timeAgo(x.created))}</td>
                <td class="mono">${esc(x.address || '—')}</td>
                <td class="num">${esc(fmtAmt(x.amount))} ${esc(sym)}</td>
                <td><span class="status ${esc(st)}">${esc(t('status_' + st) !== 'status_' + st ? t('status_' + st) : st)}</span></td>
                <td>${shortHex(x.transactionConfirmationData, true)}</td></tr>`;
            }).join('')}
          </table></div>` : `<div class="chart-empty">${esc(t('empty_payments'))}</div>`;
      }
      const pages = total != null ? Math.max(1, Math.ceil(total / pageSize)) : null;
      body.innerHTML = `
        <div class="panel">
          <h3>${esc(t('tab_' + kind))}${total != null ? ` <span style="color:var(--muted);font-weight:500;font-size:13px">(${fmtInt(total)})</span>` : ''}</h3>
          ${table}
          <div class="pager">
            <button id="pg-prev" ${page <= 0 ? 'disabled' : ''}>${esc(t('pager_prev'))}</button>
            <span class="pg">${page + 1}${pages ? ` / ${pages}` : ''}</span>
            <button id="pg-next" ${(pages && page >= pages - 1) || rows.length < pageSize ? 'disabled' : ''}>${esc(t('pager_next'))}</button>
          </div>
        </div>`;
      bindCopy(body);
      $('#pg-prev').addEventListener('click', () => { if (page > 0) { page--; load(); } });
      $('#pg-next').addEventListener('click', () => { page++; load(); });
    };
    await load();
  }

  async function tabMiners(coinId, body) {
    const r = await api(`/pools/${encodeURIComponent(coinId)}/miners`, coinId);
    const rows = Array.isArray(r.data) ? r.data : [];
    body.innerHTML = `
      <div class="panel">
        <h3>${esc(t('tab_miners'))}</h3>
        ${rows.length ? `
        <div class="tbl-wrap"><table class="tbl">
          <tr><th>${esc(t('col_rank'))}</th><th>${esc(t('col_miner'))}</th><th class="num">${esc(t('col_hashrate'))}</th><th class="num">${esc(t('col_shares'))}</th></tr>
          ${rows.map((m, i) => `<tr>
            <td>${i + 1}</td><td class="mono">${esc(m.miner)}</td>
            <td class="num">${esc(fmtHRText(m.hashrate))}</td>
            <td class="num">${(Number(m.sharesPerSecond) || 0).toFixed(3)}</td></tr>`).join('')}
        </table></div>` : `<div class="chart-empty">${esc(t('empty_miners'))}</div>`}
        <div class="note" style="margin-top:10px">${lang === 'zh' ? '地址已脱敏显示；在「我的矿机」输入完整地址可查看自己的详细数据。' : 'Addresses are masked. Use “My rigs” with your full address to see your own details.'}</div>
      </div>`;
  }

  function tabLookup(coinId, body) {
    const saved = localStorage.getItem('ntm_addr_' + coinId) || '';
    const meta = coinMeta(coinId) || {};
    body.innerHTML = `
      <div class="panel">
        <h3>${esc(t('tab_lookup'))}</h3>
        <div class="lookup-bar">
          <input id="lk-input" spellcheck="false" placeholder="${esc(meta.addressExample || t('lookup_ph'))}" value="${esc(saved)}">
          <button class="btn primary" id="lk-btn">${esc(t('lookup_btn'))}</button>
        </div>
        <div id="lk-result"></div>
      </div>`;
    const run = async () => {
      const addr = $('#lk-input').value.trim();
      if (!addr) return;
      localStorage.setItem('ntm_addr_' + coinId, addr);
      const box = $('#lk-result');
      box.innerHTML = `<div class="skeleton loading">…</div>`;
      destroyCharts();
      try {
        const r = await api(`/pools/${encodeURIComponent(coinId)}/miners/${encodeURIComponent(addr)}`, coinId);
        const d = r.data;
        const sym = meta.symbol || '';
        const perf = d.performance || {};
        const workers = perf.workers || {};
        box.innerHTML = `
          <div class="stat-strip">
            <div class="tile"><div class="k">${esc(t('lookup_cur_hashrate'))}</div><div class="v">${fmtHR(perf.hashrate)}</div></div>
            <div class="tile"><div class="k">${esc(t('lookup_pending'))}</div><div class="v"><span>${esc(fmtAmt(d.pendingBalance))}</span><small>${esc(sym)}</small></div></div>
            <div class="tile"><div class="k">${esc(t('lookup_paid'))}</div><div class="v"><span>${esc(fmtAmt(d.totalPaid))}</span><small>${esc(sym)}</small></div></div>
            <div class="tile"><div class="k">${esc(t('lookup_last_pay'))}</div>
              <div class="v"><span>${d.lastPaymentAmount != null ? esc(fmtAmt(d.lastPaymentAmount)) : esc(t('lookup_none_yet'))}</span>${d.lastPaymentAmount != null ? `<small>${esc(sym)}</small>` : ''}</div>
              ${d.lastPayment ? `<div class="hint">${esc(timeAgo(d.lastPayment))}${d.lastPaymentTxid ? ' · txid ' + esc(d.lastPaymentTxid.slice(0, 10)) + '…' : ''}</div>` : ''}</div>
          </div>
          <div class="panel" style="margin-top:6px"><h3>${esc(t('chart_miner_hashrate'))}</h3><div class="chart-box" id="miner-chart"></div></div>
          <div class="panel"><h3>${esc(t('lookup_workers'))}</h3>
            <div class="tbl-wrap"><table class="tbl">
              <tr><th>${esc(t('col_worker'))}</th><th class="num">${esc(t('col_hashrate'))}</th><th class="num">${esc(t('col_shares'))}</th><th>${esc(t('col_lastshare'))}</th></tr>
              ${Object.keys(workers).length ? Object.entries(workers).sort((a, b) => a[0].localeCompare(b[0])).map(([w, x]) => `<tr>
                <td class="mono">${esc(w || 'default')}</td>
                <td class="num">${esc(fmtHRText(x.hashrate))}</td>
                <td class="num">${(Number(x.sharesPerSecond) || 0).toFixed(3)}</td>
                <td${x.lastSeen ? ` title="${esc(fmtTime(x.lastSeen))}"` : ''}>${x.lastSeen ? esc(timeAgo(x.lastSeen)) : '—'}</td></tr>`).join('')
              : `<tr><td colspan="4" style="color:var(--muted)">${esc(t('empty_miners'))}</td></tr>`}
            </table></div>
          </div>
          <div class="panel"><h3>${esc(t('lookup_payments'))}</h3>
            ${(d.recentPayments || []).length ? `<div class="tbl-wrap"><table class="tbl">
              <tr><th>${esc(t('col_time'))}</th><th class="num">${esc(t('col_amount'))}</th><th>${esc(t('col_status'))}</th><th>${esc(t('col_txid'))}</th></tr>
              ${d.recentPayments.map((x) => {
                const st = x.status || 'sent';
                return `<tr>
                  <td title="${esc(fmtTime(x.created))}">${esc(timeAgo(x.created))}</td>
                  <td class="num">${esc(fmtAmt(x.amount))} ${esc(sym)}</td>
                  <td><span class="status ${esc(st)}">${esc(t('status_' + st) !== 'status_' + st ? t('status_' + st) : st)}</span></td>
                  <td>${shortHex(x.transactionConfirmationData, true)}</td></tr>`;
              }).join('')}
            </table></div>` : `<div class="chart-empty">${esc(t('empty_payments'))}</div>`}
          </div>`;
        hashrateChart('miner-chart', d.performanceSamples || [], t('lookup_cur_hashrate'), '#199e70');
      } catch (e) {
        box.innerHTML = `<div class="err-box">${esc(e.status === 429 ? t('lookup_err_rate') : t('lookup_err_notfound'))}</div>`;
      }
    };
    $('#lk-btn').addEventListener('click', run);
    $('#lk-input').addEventListener('keydown', (e) => { if (e.key === 'Enter') run(); });
    if (saved) run();
  }

  async function tabConnect(coinId, body) {
    const meta = coinMeta(coinId) || {};
    let pool = null;
    try { const r = await api(`/pools/${encodeURIComponent(coinId)}`, coinId); pool = r.data.pool || r.data; } catch (_) {}
    const pay = (pool && pool.paymentProcessing) || {};
    const sym = meta.symbol || '';
    const ep = (meta.stratum && meta.stratum[0]) || { host: '', port: 0 };
    const addrSaved = localStorage.getItem('ntm_addr_' + coinId) || '';
    const addrPh = meta.addressExample || 'YOUR_WALLET_ADDRESS';
    const gh = CFG.brand.githubMiner;
    const dl = (f) => `${gh}/releases/latest/download/${f}`;
    const zh = lang === 'zh';

    const cmd = (bin, addr) =>
      `${bin} -a ${meta.ntmAlgoFlag} -o ${ep.host}:${ep.port} -u ${addr || addrPh}`;
    const xmrigCmd = (addr) =>
      `./xmrig -a ${meta.ntmAlgoFlag} -o ${ep.host}:${ep.port} -u ${addr || addrPh} -p x`;

    body.innerHTML = `
      <div class="panel">
        <h3>${esc(t('conn_intro_t'))}</h3>
        <ol class="steps">
          <li><div class="t">${esc(t('conn_step1_t'))}</div>
            <div class="d">${zh
              ? `准备一个 ${esc(meta.name)} 钱包地址${meta.addressPrefix ? `（以 <span class="mono">${esc(meta.addressPrefix)}</span> 开头）` : ''}。钱包与地址生成请见 <a href="${esc(meta.links.site)}" target="_blank" rel="noopener">${esc(t('coin_links_site'))}</a>。`
              : `Get a ${esc(meta.name)} wallet address${meta.addressPrefix ? ` (starts with <span class="mono">${esc(meta.addressPrefix)}</span>)` : ''}. See the <a href="${esc(meta.links.site)}" target="_blank" rel="noopener">official site</a> for wallets.`}</div></li>
          <li><div class="t">${esc(t('conn_step2_t'))}</div>
            <div class="d">${esc(t('conn_step2_d'))} <b id="miner-ver">${esc(CFG.brand.minerVersionFallback)}</b></div>
            <div class="dl-grid">
              <a class="dl-item" href="${esc(dl('NTMminer-windows-x64.exe'))}"> ${DL_ICON}<span><span class="p">Windows x64</span><br><span class="f">NTMminer-windows-x64.exe</span></span></a>
              <a class="dl-item" href="${esc(dl('NTMminer-linux-x64'))}">${DL_ICON}<span><span class="p">Linux x64 / HiveOS</span><br><span class="f">NTMminer-linux-x64</span></span></a>
              <a class="dl-item" href="${esc(dl('NTMminer-linux-arm64'))}">${DL_ICON}<span><span class="p">Linux ARM64</span><br><span class="f">NTMminer-linux-arm64</span></span></a>
              <a class="dl-item" href="${esc(dl('NTMminer-macos-arm64'))}">${DL_ICON}<span><span class="p">macOS (Apple Silicon)</span><br><span class="f">NTMminer-macos-arm64</span></span></a>
            </div></li>
          <li><div class="t">${esc(t('conn_step3_t'))}</div>
            <div class="d">${esc(t('conn_step3_d'))}</div>
            <div class="lookup-bar" style="margin-top:10px">
              <input id="conn-addr" spellcheck="false" placeholder="${esc(t('conn_addr_fill'))} ${esc(addrPh)}" value="${esc(addrSaved)}">
            </div>
            <div style="color:var(--muted);font-size:12.5px;margin:6px 0 2px">Windows</div>
            <div class="codeblock" id="cmd-win"></div>
            <div style="color:var(--muted);font-size:12.5px;margin:6px 0 2px">Linux / HiveOS</div>
            <div class="codeblock" id="cmd-lin"></div>
            <div class="hint" style="margin-top:8px"><b>${esc(t('conn_worker_hint_t'))}</b>${esc(t('conn_worker_hint'))}</div></li>
        </ol>
      </div>
      <div class="panel">
        <h3>${esc(t('conn_endpoints_t'))}</h3>
        <div class="tbl-wrap"><table class="tbl">
          <tr><th>${esc(t('conn_endpoint_col'))}</th><th>${esc(t('conn_port_col'))}</th><th>${esc(t('conn_mode_col'))}</th><th>${esc(t('conn_diff_col'))}</th></tr>
          ${(meta.stratum || []).map((s) => `<tr><td class="mono">${esc(s.host)}</td><td class="mono">${s.port}</td><td><span class="chip" style="font-size:11px;padding:1px 8px">${esc((s.mode || 'PPLNS').toUpperCase())}</span></td><td>${esc(t('conn_vardiff'))}</td></tr>`).join('')}
        </table></div>
        ${(meta.stratum || []).some((s) => (s.mode || '').toLowerCase() === 'solo') ? `<div class="note" style="margin-top:10px">${esc(t('conn_solo_note'))}</div>` : ''}
      </div>
      ${meta.xmrigCompatible ? `
      <div class="panel">
        <h3>${esc(t('conn_xmrig_t'))}</h3>
        <div class="note" style="font-size:14px;color:var(--ink-2)">${esc(t('conn_xmrig_d'))}</div>
        <div class="codeblock" id="cmd-xmrig"></div>
      </div>` : ''}
      <div class="panel">
        <h3>${esc(t('conn_payout_t'))}</h3>
        <div class="note" style="font-size:14px;color:var(--ink-2)">
          <ul style="margin:6px 0;padding-left:20px">
            <li>${zh ? `分配方式 <b>${esc(pay.payoutScheme || 'PPLNS')}</b>，池费率 <b>${pool ? esc(String(pool.poolFeePercent)) : '—'}%</b>` : `Reward scheme <b>${esc(pay.payoutScheme || 'PPLNS')}</b>, pool fee <b>${pool ? esc(String(pool.poolFeePercent)) : '—'}%</b>`}</li>
            ${pool && pool.soloFeePercent != null ? `<li>${zh ? `SOLO 端口费率 <b>${esc(String(pool.soloFeePercent))}%</b>（爆块奖励扣费后全归爆块者本人）` : `SOLO port fee <b>${esc(String(pool.soloFeePercent))}%</b> (block reward minus fee goes entirely to the finder)`}</li>` : ''}
            <li>${zh ? `起付额 <b>${esc(fmtAmt(pay.minimumPayment))} ${esc(sym)}</b>，达到后自动打款到你的挖矿地址` : `Minimum payout <b>${esc(fmtAmt(pay.minimumPayment))} ${esc(sym)}</b>, paid automatically to your mining address`}</li>
            <li>${zh ? `爆块 <b>${meta.confirmations || 10} 个确认</b>后计入余额` : `Blocks credit after <b>${meta.confirmations || 10} confirmations</b>`}</li>
            <li>${zh ? `NTMminer 当前 <b>0% 开发者抽水</b>` : `NTMminer currently has a <b>0% dev fee</b>`}</li>
          </ul>
        </div>
      </div>`;

    const syncCmds = () => {
      const a = $('#conn-addr').value.trim();
      const put = (id, text) => {
        const el2 = $(id);
        if (!el2) return;
        el2.innerHTML = `${esc(text)}<button class="copy-btn" data-copy="${esc(text)}">${esc(t('copy'))}</button>`;
      };
      put('#cmd-win', cmd('NTMminer-windows-x64.exe', a));
      put('#cmd-lin', cmd('./NTMminer-linux-x64', a));
      if (meta.xmrigCompatible) put('#cmd-xmrig', xmrigCmd(a));
      bindCopy(body);
    };
    $('#conn-addr').addEventListener('input', syncCmds);
    syncCmds();

    fetch(CFG.brand.minerReleaseApi).then((r) => (r.ok ? r.json() : null)).then((j) => {
      if (j && j.tag_name && $('#miner-ver')) $('#miner-ver').textContent = j.tag_name;
    }).catch(() => {});
  }

  // ---------- router ----------
  function route() {
    destroyCharts();
    if (state.timer) { clearInterval(state.timer); state.timer = null; }
    const path = location.pathname.replace(/\/+$/, '') || '/';
    renderNav();
    if (path === '/' || path === '/index.html') {
      state.route = 'home'; state.coin = null;
      renderHome();
      state.timer = setInterval(refreshHome, CFG.api.refreshMs);
      return;
    }
    const coinId = path.slice(1).split('/')[0].toLowerCase();
    if (CFG.coins[coinId]) {
      state.route = 'coin'; state.coin = coinId;
      const h = (location.hash || '').replace('#', '');
      state.tab = TABS.includes(h) ? h : 'dashboard';
      renderCoin(coinId);
      state.timer = setInterval(() => {
        if (state.tab === 'dashboard' || state.tab === 'miners') renderTab(coinId);
      }, CFG.api.refreshMs);
      return;
    }
    history.replaceState(null, '', '/');
    route();
  }

  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[data-nav]');
    if (!a) return;
    e.preventDefault();
    history.pushState(null, '', a.getAttribute('href'));
    route();
  });
  window.addEventListener('popstate', route);

  $('#lang-btn').addEventListener('click', () => {
    lang = lang === 'zh' ? 'en' : 'zh';
    localStorage.setItem('ntm_lang', lang);
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    route();
  });

  document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
  route();
})();
