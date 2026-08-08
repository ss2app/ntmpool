import {
  ApiError, apiBaseFor, fetchJson, getBlocks, getMiner, getMiners, getPayments,
  getPerformance, getPool, getPools, normalizeList, normalizePerformance,
  normalizePoolDetail, normalizePools, validateRuntimeConfig, withSessionStale,
} from './api.20260720d.js';
import {
  formatRelative, getLanguage, getLocale, localize, setLanguage, t, translateStatus,
} from './i18n.20260808a.js';
import { preparePerformance, renderBlocksTimeline, renderDailyPayoutChart, renderHashrateChart } from './charts.20260808a.js';
import { cleanup as cleanupDOMStorage } from './dom-storage-cleanup.20260720d.js';
import { isUnfinishedDOMCandidate } from './block-display.20260720g.js?release=20260720h';

const RELEASE = '20260802a';
const BASE_TABS = ['dashboard', 'blocks', 'payments', 'miners', 'lookup', 'connect'];
const DOM_ACCOUNT_TYPE = 'dom-slate-v4';
const DOM_PUBLIC_ID = /^domh_[0-9a-f]{64}$/;
const DOM_RESERVED_INPUT = /(?:dom[hwcp]_[0-9a-f]{64}|DOMSLATE4\.|DOM-MAINNET-RECOVERY:)/i;
const CLI_REFERENCE = [
  {
    id: 'connection',
    titleZh: '连接',
    titleEn: 'Connection',
    options: [
      {
        aliases: ['-o', '--url', '--pool-url'], value: '<host:port>',
        descriptionZh: '矿池地址（可带 stratum+tcp:// 前缀）',
        descriptionEn: 'Pool address (may include the stratum+tcp:// prefix).',
      },
      {
        aliases: ['-u', '--user', '--address'], value: '<wallet>',
        descriptionZh: '钱包/登录地址',
        descriptionEn: 'Wallet/login address.',
      },
      {
        aliases: ['--worker', '--rig-id'], value: '<名>',
        descriptionZh: 'worker 名（可选）',
        descriptionEn: 'Worker name (optional).',
      },
      {
        aliases: ['-p', '--pass'], value: '<pass>', defaultZh: 'x', defaultEn: 'x',
        descriptionZh: '登录密码（默认 x）',
        descriptionEn: 'Login password (default: x).',
      },
      {
        aliases: ['--socks5', '-x'], value: '[user:pass@]host:port',
        descriptionZh: '经 SOCKS5 代理挖矿',
        descriptionEn: 'Mine through a SOCKS5 proxy.',
      },
    ],
  },
  {
    id: 'algorithm-hardware',
    titleZh: '算法与硬件',
    titleEn: 'Algorithms and hardware',
    options: [
      {
        aliases: ['-a', '--algo', '--coin'], value: '<名>', defaultZh: 'neuromorph', defaultEn: 'neuromorph',
        descriptionZh: '算法/币：neuromorph | argon2id-blocknet | midstate | rx/dragonx | zoka | rx/brva | rx/tar | rx/zeph | rx/scash | btx | qpow | btc09 | quark | velkarhash（默认 neuromorph）',
        descriptionEn: 'Algorithm/coin: neuromorph | argon2id-blocknet | midstate | rx/dragonx | zoka | rx/brva | rx/tar | rx/zeph | rx/scash | btx | qpow | btc09 | quark | velkarhash (default: neuromorph).',
      },
      {
        aliases: ['-t', '--threads'], value: '<n>',
        defaultZh: '物理核数（关 SMT 配 MLP）', defaultEn: 'Physical core count (SMT off with MLP)',
        descriptionZh: '线程数（默认=物理核数，关 SMT 配 MLP；上限 256）',
        descriptionEn: 'Number of threads (default = physical core count, with SMT off and MLP; maximum 256).',
      },
      {
        aliases: ['--smt'], value: null, defaultZh: '关', defaultEn: 'Off',
        descriptionZh: '改用全部逻辑核（SMT 全开）。大核机通常物理核+MLP 更快，故默认关',
        descriptionEn: 'Use all logical cores instead (SMT fully enabled). On high-core-count machines, physical cores + MLP are usually faster, so this is off by default.',
      },
      {
        aliases: ['--gpu'], value: null,
        descriptionZh: '开 CUDA GPU 后端（midstate=纯 GPU；btx/qpow=CPU+GPU 双挖）。单二进制内置，需 NVIDIA 驱动',
        descriptionEn: 'Enable the CUDA GPU backend (midstate = GPU only; btx/qpow = simultaneous CPU+GPU mining). Built into the single binary; requires an NVIDIA driver.',
        scopeZh: 'CUDA GPU 后端；help 明示 midstate、btx、qpow',
        scopeEn: 'CUDA GPU backend; help explicitly names midstate, btx, and qpow',
      },
      {
        aliases: ['--gpu-only', '--no-cpu'], value: null,
        descriptionZh: '仅用 GPU 挖、不起 CPU worker（btx/qpow 关掉双挖的 CPU 边；隐含 --gpu）。GPU 矿机用这个',
        descriptionEn: 'Mine with GPU only and do not start CPU workers (for btx/qpow, disables the CPU side of dual mining; implies --gpu). Use this for GPU mining rigs.',
        scopeZh: 'GPU 挖矿；btx/qpow 会关闭双挖的 CPU 边',
        scopeEn: 'GPU mining; for btx/qpow, disables the CPU side of dual mining',
      },
      {
        aliases: ['--gpu-devices'], value: '<列表>', defaultZh: '全部卡', defaultEn: 'All cards',
        descriptionZh: '多显卡：指定用哪几张卡（逗号列表如 0,1,3；缺省=全部卡）。隐含 --gpu',
        descriptionEn: 'Multiple GPUs: choose which cards to use (comma-separated list such as 0,1,3; default = all cards). Implies --gpu.',
        scopeZh: '多显卡；隐含 --gpu',
        scopeEn: 'Multiple GPUs; implies --gpu',
      },
      {
        aliases: ['--stats-demo'], value: null,
        descriptionZh: '不连矿池，只刷新真实 NVML 状态（算力无 worker，显示 n/a）',
        descriptionEn: 'Do not connect to a pool; only refresh actual NVML status (no hashrate worker, shown as n/a).',
        scopeZh: 'NVML 状态演示',
        scopeEn: 'NVML status demonstration',
      },
      {
        aliases: ['--huge-pages'], value: '<2m|1g|off>', defaultZh: '自动 2m', defaultEn: 'Automatic 2m',
        descriptionZh: '大页（默认自动 2m，v1.11.1 起含 RandomX 系 dragonx/zoka，rx 实测 +47）。对标 xmrig：Win 自动授予锁页特权、Linux(root) 自预留；1g 仅 dragonx dataset(实测 wash)',
        descriptionEn: 'Huge pages (default: automatic 2m; since v1.11.1 this includes the RandomX-family dragonx/zoka, with rx measured at +47). To compare against xmrig: Windows automatically grants the lock-pages privilege; Linux (root) self-reserves; 1g is only for the dragonx dataset (measured wash).',
        scopeZh: '2m：help 未注明算法限制；1g：仅 dragonx dataset',
        scopeEn: '2m: no algorithm limit stated in help; 1g: dragonx dataset only',
      },
      {
        aliases: ['--no-huge-pages'], value: null,
        descriptionZh: '= --huge-pages off（A/B 对照）',
        descriptionEn: '= --huge-pages off (for A/B comparison).',
        scopeZh: '与 --huge-pages 相同',
        scopeEn: 'Same as --huge-pages',
      },
      {
        aliases: ['--msr'], value: null, defaultZh: '关', defaultEn: 'Off',
        descriptionZh: 'RandomX(dragonx/zoka) 启用 AMD Zen/Intel MSR 预取器调优（需 root+msr 模块；默认关，root-only 且机器相关；对拍 xmrigCC〔默认自写 MSR〕时用它对齐口径）',
        descriptionEn: 'For RandomX (dragonx/zoka), enable AMD Zen/Intel MSR prefetcher tuning (requires root + the msr module; off by default, root-only, and machine-dependent; use it to align the comparison basis with xmrigCC, which writes MSRs by default).',
        scopeZh: '仅 RandomX（dragonx/zoka）',
        scopeEn: 'RandomX only (dragonx/zoka)',
      },
      {
        aliases: ['--scash-epoch-duration'], value: '<秒>', defaultZh: '604800', defaultEn: '604800',
        descriptionZh: 'SCASH epoch 时长（默认 604800；regtest 用 86400）',
        descriptionEn: 'SCASH epoch duration (default: 604800; use 86400 for regtest).',
        scopeZh: '仅 rx/scash',
        scopeEn: 'rx/scash only',
      },
      {
        aliases: ['--lanes'], value: '<n|auto>',
        defaultZh: 'auto（启动满载自调）', defaultEn: 'auto (self-tunes under full load at startup)',
        descriptionZh: 'MLP 交错 lane 数（NeuroMorph 专属）。默认 auto=启动满载自调；大核 EPYC 关键提速（单链喂不饱内存通道，K=2~4 倍增吞吐）。--lanes 1 关',
        descriptionEn: 'Number of interleaved MLP lanes (NeuroMorph only). Default auto = self-tune under full load at startup; a critical speedup on high-core-count EPYC (a single chain cannot saturate the memory channels; K=2–4 multiplies throughput). --lanes 1 turns it off.',
        scopeZh: '仅 NeuroMorph',
        scopeEn: 'NeuroMorph only',
      },
    ],
  },
  {
    id: 'other',
    titleZh: '其它',
    titleEn: 'Other',
    options: [
      {
        aliases: ['--bench'], value: '<秒>',
        descriptionZh: '跑 N 秒自动退出（测试用）',
        descriptionEn: 'Run for N seconds and exit automatically (for testing).',
      },
      {
        aliases: ['-h', '--help'], value: null,
        descriptionZh: '本帮助',
        descriptionEn: 'Show this help.',
      },
      {
        aliases: ['-V', '--version'], value: null,
        descriptionZh: '版本',
        descriptionEn: 'Show the version.',
      },
    ],
  },
];
const MINING_PARAMETER_ALIASES = {
  threads: '-t',
  smt: '--smt',
  gpu: '--gpu',
  gpuOnly: '--gpu-only',
  gpuDevices: '--gpu-devices',
  hugePages: '--huge-pages',
  noHugePages: '--no-huge-pages',
  msr: '--msr',
};
const MINING_PARAMETER_OPTIONS = new Map(Object.entries(MINING_PARAMETER_ALIASES).map(([id, alias]) => [
  id,
  CLI_REFERENCE.flatMap((group) => group.options).find((option) => option.aliases.includes(alias)),
]));
const state = {
  config: null,
  route: null,
  controller: null,
  timer: null,
  chartHandles: [],
  routeHandle: null,
  renderQueued: false,
  lookup: new Map(),
  lookupAutoQueried: new Set(),
  addresses: new Map(),
  remember: new Map(),
  connectEndpoint: new Map(),
};

const main = document.getElementById('main-content');
const header = document.getElementById('site-header');
const footer = document.getElementById('site-footer');
const toastRegion = document.getElementById('toast-region');

function escapeHtml(value) {
  return String(value ?? '').replace(/[&<>'"]/g, (character) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;',
  }[character]));
}

function escapeAttribute(value) {
  return escapeHtml(value).replace(/`/g, '&#96;');
}

function hasValue(value) {
  return value !== null && value !== undefined && value !== '';
}

function enabledCoins() {
  return Object.values(state.config.coins)
    .filter((coin) => coin.enabled)
    .sort((a, b) => a.order - b.order);
}

function formatInteger(value) {
  return typeof value === 'number' && Number.isFinite(value)
    ? new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 0 }).format(value)
    : t('noData');
}

function formatDecimal(value, minimum = 0, maximum = 2) {
  return typeof value === 'number' && Number.isFinite(value)
    ? new Intl.NumberFormat(getLocale(), { minimumFractionDigits: minimum, maximumFractionDigits: maximum }).format(value)
    : t('noData');
}

function formatHashrate(value, unit = 'H/s') {
  if (typeof value !== 'number' || !Number.isFinite(value)) return t('noData');
  const prefixes = ['', 'K', 'M', 'G', 'T', 'P'];
  let scaled = value;
  let index = 0;
  while (Math.abs(scaled) >= 1000 && index < prefixes.length - 1) { scaled /= 1000; index += 1; }
  const digits = Math.abs(scaled) >= 100 ? 0 : Math.abs(scaled) >= 10 ? 1 : 2;
  return `${formatDecimal(scaled, 0, digits)} ${prefixes[index]}${unit}`;
}

function formatAmount(value, coin, includeSymbol = true) {
  if (typeof value !== 'number' || !Number.isFinite(value)) return t('noData');
  const scaled = value * coin.amountScale;
  if (Array.isArray(coin.binaryUnits) && coin.binaryUnits.length) {
    const selected = coin.binaryUnits.find((unit) => Math.abs(scaled) >= unit.factor) || coin.binaryUnits[coin.binaryUnits.length - 1];
    const number = scaled / selected.factor;
    return `${formatDecimal(number, coin.amountDecimals.min, coin.amountDecimals.max)} ${selected.prefix}${includeSymbol ? coin.symbol : ''}`.trim();
  }
  const number = formatDecimal(scaled, coin.amountDecimals.min, coin.amountDecimals.max);
  return includeSymbol ? `${number} ${coin.symbol}` : number;
}

function formatBlockReward(block, coin) {
  return isUnfinishedDOMCandidate(block, coin)
    ? t('domCandidateRewardIncomplete')
    : formatAmount(block?.reward, coin);
}

function formatPercent(value) {
  return typeof value === 'number' && Number.isFinite(value) ? `${formatDecimal(value, 0, 4)}%` : t('noData');
}

function dateParts(date) {
  const pad = (value) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function formatDate(value) {
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? dateParts(date) : t('noData');
}

function timeMarkup(value) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return escapeHtml(t('noData'));
  const title = `${dateParts(date)} · ${date.toISOString()}`;
  return `<time datetime="${escapeAttribute(date.toISOString())}" title="${escapeAttribute(title)}"><span>${escapeHtml(formatRelative(date))}</span><small>${escapeHtml(dateParts(date))}</small></time>`;
}

function shortValue(value, start = 10, end = 8) {
  const text = String(value ?? '');
  return text.length > start + end + 3 ? `${text.slice(0, start)}…${text.slice(-end)}` : text;
}

function maskAddress(value) {
  const text = String(value ?? '');
  if (text.length <= 14) return text;
  return `${text.slice(0, 8)}…${text.slice(-6)}`;
}

function apiMessage(error) {
  if (error?.code === 'timeout') return t('errorTimeout');
  if (error?.status === 400) return t('error400');
  if (error?.status === 404) return t('error404');
  if (error?.status === 429) return t('error429');
  if (error?.status >= 500) return t('error5xx');
  return t('errorNetwork');
}

function statusBadge(kind, label, extraClass = '') {
  const icons = { online: '●', offline: '○', unavailable: '⚠', stale: '◷', critical: '⚠', unknown: '◆', info: 'ⓘ' };
  return `<span class="status-badge status-${escapeAttribute(kind)} ${escapeAttribute(extraClass)}"><span aria-hidden="true">${icons[kind] || '◆'}</span>${escapeHtml(label)}</span>`;
}

function coinLogo(coin, eager = false) {
  if (coin.logo) {
    return `<span class="coin-logo"><img src="${escapeAttribute(coin.logo)}" alt="${escapeAttribute(coin.symbol)}" width="48" height="48" ${eager ? 'fetchpriority="high"' : 'loading="lazy"'}></span>`;
  }
  return `<span class="coin-logo coin-logo-fallback" role="img" aria-label="${escapeAttribute(coin.name)}"><svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="22" fill="${escapeAttribute(coin.color)}"/><circle cx="24" cy="24" r="18" fill="#020207"/><text x="24" y="28" text-anchor="middle" fill="#DEFBFC">${escapeHtml(coin.symbol.slice(0, 4))}</text></svg></span>`;
}

function coinColorLine(coin) {
  return `<svg class="coin-color-line" viewBox="0 0 100 3" preserveAspectRatio="none" aria-hidden="true"><rect width="100" height="3" fill="${escapeAttribute(coin.color)}"/></svg>`;
}

function errorBox(error, retryId = '') {
  console.error(error);
  return `<div class="state-box state-error" role="status"><span aria-hidden="true">⚠</span><p>${escapeHtml(apiMessage(error))}</p>${retryId ? `<button class="button secondary" type="button" id="${escapeAttribute(retryId)}">${escapeHtml(t('retry'))}</button>` : ''}</div>`;
}

function emptyBox(text) {
  return `<div class="state-box state-empty"><span aria-hidden="true">◇</span><p>${escapeHtml(text)}</p></div>`;
}

function showToast(message) {
  const toast = document.createElement('div');
  toast.className = 'toast';
  toast.textContent = message;
  toastRegion.replaceChildren(toast);
  window.setTimeout(() => toast.remove(), 2200);
}

async function copyText(text, button) {
  try {
    if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
    await navigator.clipboard.writeText(text);
    const original = button.textContent;
    button.textContent = `✓ ${t('copied')}`;
    window.setTimeout(() => { if (button.isConnected) button.textContent = original; }, 2000);
  } catch (_) {
    const target = button.matches('.cli-alias-button')
      ? button
      : button.closest('.command-box, .copy-cell')?.querySelector('code, pre, .copy-source');
    if (target) {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(target);
      selection.removeAllRanges();
      selection.addRange(range);
    }
    showToast(t('copyFailed'));
  }
}

function wireCopyButtons(root = main) {
  root.querySelectorAll('[data-copy-text]').forEach((button) => {
    button.addEventListener('click', () => copyText(button.getAttribute('data-copy-text') || '', button));
  });
}

function clearChartHandles() {
  state.chartHandles.forEach((handle) => handle?.destroy?.());
  state.chartHandles = [];
}

function destroyRouteResources() {
  state.routeHandle?.destroy?.();
  state.routeHandle = null;
  state.controller?.abort();
  state.controller = null;
  window.clearInterval(state.timer);
  state.timer = null;
  clearChartHandles();
}

function setAutoRefresh(callback) {
  window.clearInterval(state.timer);
  const start = () => {
    window.clearInterval(state.timer);
    if (!document.hidden) state.timer = window.setInterval(callback, state.config.api.refreshMs);
  };
  start();
  state.route.refreshVisible = callback;
}

function queueRender(options = {}) {
  if (state.renderQueued) return;
  state.renderQueued = true;
  window.requestAnimationFrame(() => {
    state.renderQueued = false;
    renderRoute(options);
  });
}

function navigate(href, replace = false) {
  const url = new URL(href, window.location.href);
  const method = replace ? 'replaceState' : 'pushState';
  history[method]({}, '', `${url.pathname}${url.search}${url.hash}`);
  renderRoute();
}

function renderShell() {
  const config = state.config;
  const discord = config.brand.links.discord;
  header.innerHTML = `
    <div class="header-inner container">
      <a class="brand-link" href="/" data-route aria-label="${escapeAttribute(config.brand.siteName)}">
        <img class="brand-lockup" src="${escapeAttribute(config.brand.assets.lockup)}" alt="" width="220" height="36">
        <img class="brand-mark" src="${escapeAttribute(config.brand.assets.mark)}" alt="" width="40" height="40">
      </a>
      <nav class="desktop-nav" aria-label="Primary">
        <a href="/" data-route data-nav="home">${escapeHtml(t('navHome'))}</a>
        <a href="/download" data-route data-nav="download">${escapeHtml(t('navDownload'))}</a>
      </nav>
      <div class="header-actions">
        ${discord ? `<a class="header-discord" href="${escapeAttribute(discord)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('discord'))}<span aria-hidden="true">↗</span></a>` : ''}
        <button class="button ghost header-language" type="button" id="language-button">${escapeHtml(t('switchLanguage'))}</button>
        <a class="button primary header-cta" href="/download" data-route><span class="full-label">${escapeHtml(t('downloadMiner'))}</span><span class="short-label">${escapeHtml(t('navDownload'))}</span></a>
        <button class="menu-button" type="button" id="menu-button" aria-expanded="false" aria-controls="mobile-menu" aria-label="${escapeAttribute(t('menu'))}"><span aria-hidden="true">☰</span></button>
      </div>
    </div>
    <nav class="mobile-menu" id="mobile-menu" aria-label="Mobile" hidden>
      <div class="container">
        <a href="/" data-route>${escapeHtml(t('navHome'))}</a>
        <a href="/download" data-route>${escapeHtml(t('navDownload'))}</a>
        ${discord ? `<a href="${escapeAttribute(discord)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('discord'))} ↗</a>` : ''}
      </div>
    </nav>`;
  footer.innerHTML = `
    <div class="footer-accent" aria-hidden="true"></div>
    <div class="container footer-grid">
      <div class="footer-brand">
        <img src="${escapeAttribute(config.brand.assets.lockup)}" alt="${escapeAttribute(config.brand.siteName)}" width="200" height="32" loading="lazy">
        <p>${escapeHtml(config.brand.expansionEn)}</p>
        <p class="footer-risk">${escapeHtml(t('footerRisk'))}</p>
      </div>
      <nav aria-label="Footer">
        <h2>${escapeHtml(config.brand.siteName)}</h2>
        <a href="/" data-route>${escapeHtml(t('navHome'))}</a>
        <a href="/download" data-route>${escapeHtml(t('navDownload'))}</a>
      </nav>
      <div class="footer-links">
        <h2>${escapeHtml(t('discord'))}</h2>
        ${discord ? `<a href="${escapeAttribute(discord)}" target="_blank" rel="noopener noreferrer">Discord ↗</a>` : `<span>${escapeHtml(t('noData'))}</span>`}
      </div>
    </div>
    <div class="container copyright">© ${new Date().getFullYear()} ${escapeHtml(config.brand.siteName)}. ${escapeHtml(t('allRights'))}</div>`;
  document.querySelector('.skip-link').textContent = t('skip');
  document.getElementById('language-button').addEventListener('click', () => setLanguage(getLanguage() === 'zh' ? 'en' : 'zh'));
  const menuButton = document.getElementById('menu-button');
  const mobileMenu = document.getElementById('mobile-menu');
  menuButton.addEventListener('click', () => {
    const open = menuButton.getAttribute('aria-expanded') === 'true';
    menuButton.setAttribute('aria-expanded', String(!open));
    menuButton.setAttribute('aria-label', open ? t('menu') : t('closeMenu'));
    menuButton.querySelector('span').textContent = open ? '☰' : '×';
    mobileMenu.hidden = open;
  });
}

function updateActiveNav(route) {
  header.querySelectorAll('[data-nav]').forEach((link) => {
    const active = link.getAttribute('data-nav') === route;
    link.classList.toggle('active', active);
    if (active) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
  });
}

function parseRoute() {
  const path = window.location.pathname.replace(/\/+$/, '') || '/';
  if (path === '/' || path === '/index.html') return { kind: 'home', name: 'home' };
  if (path === '/download') return { kind: 'download', name: 'download' };
  const parts = path.split('/').filter(Boolean);
  if (parts.length === 1) {
    const coin = state.config.coins[decodeURIComponent(parts[0])];
    if (coin?.enabled) {
      let tab = window.location.hash.replace(/^#/, '').toLowerCase();
      if (!tabsForCoin(coin).includes(tab)) tab = 'dashboard';
      const rawPage = Number.parseInt(new URLSearchParams(window.location.search).get('page') || '1', 10);
      const page = Number.isInteger(rawPage) && rawPage > 0 ? rawPage : 1;
      return { kind: 'coin', name: coin.id, coin, tab, page };
    }
  }
  return { kind: 'notFound', name: '404' };
}

function updateMetadata(route) {
  const config = state.config;
  const language = getLanguage();
  let title = `${config.brand.siteName} — ${config.brand.expansionEn}`;
  let description = localize(config.brand.hero.subtitle);
  if (route.kind === 'download') {
    title = `${t('downloadTitle')} ${config.downloads.version} — ${config.brand.siteName}`;
    description = t('downloadSubtitle');
  } else if (route.kind === 'coin') {
    title = `${route.coin.name} (${route.coin.symbol}) ${t(`tab${route.tab[0].toUpperCase()}${route.tab.slice(1)}`)} — ${config.brand.siteName}`;
    description = localize(route.coin.description);
  } else if (route.kind === 'notFound') {
    title = `${t('pageNotFound')} — ${config.brand.siteName}`;
    description = t('pageNotFoundBody');
  }
  document.title = title;
  document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
  document.querySelector('meta[name="description"]').setAttribute('content', description);
  document.querySelector('meta[property="og:title"]').setAttribute('content', title);
  document.querySelector('meta[property="og:description"]').setAttribute('content', description);
  const canonicalUrl = `${config.brand.canonicalOrigin}${window.location.pathname === '/index.html' ? '/' : window.location.pathname}`;
  document.querySelector('link[rel="canonical"]').setAttribute('href', canonicalUrl);
  document.querySelector('meta[property="og:url"]').setAttribute('content', canonicalUrl);
}

async function renderRoute(options = {}) {
  const scrollY = options.preserveScroll ? window.scrollY : 0;
  destroyRouteResources();
  const route = parseRoute();
  state.route = route;
  state.controller = new AbortController();
  renderShell();
  updateActiveNav(route.kind === 'home' ? 'home' : route.kind === 'download' ? 'download' : '');
  updateMetadata(route);
  if (route.kind === 'home') await renderHome(route);
  else if (route.kind === 'download') renderDownload(route);
  else if (route.kind === 'coin') await renderCoinPage(route);
  else renderNotFound();
  if (state.route !== route) return;
  if (options.preserveScroll) window.requestAnimationFrame(() => window.scrollTo(0, scrollY));
  else if (route.kind === 'home' && window.location.hash === '#pools') window.requestAnimationFrame(() => document.getElementById('pools')?.scrollIntoView());
  else window.scrollTo(0, 0);
}

function renderHomeScaffold() {
  const config = state.config;
  const promotions = config.promotions.filter((promotion) => promotion.enabled && promotion.placement === 'home');
  main.innerHTML = `
    <section class="hero-section">
      <div class="container hero-grid">
        <div class="hero-copy">
          <p class="eyebrow">${escapeHtml(t('heroEyebrow'))}</p>
          <h1>${escapeHtml(localize(config.brand.hero.title))}</h1>
          <p class="hero-expansion">${escapeHtml(config.brand.expansionEn)}</p>
          <p class="hero-subtitle">${escapeHtml(localize(config.brand.hero.subtitle))}</p>
          <div class="hero-actions">
            <a class="button primary large" href="${escapeAttribute(config.downloads.pagePath)}" data-route>${escapeHtml(t('downloadMiner'))}</a>
            <a class="button secondary large" href="/#pools" data-route>${escapeHtml(t('startMining'))} <span aria-hidden="true">↓</span></a>
          </div>
        </div>
        <div class="hero-mark-panel" aria-hidden="true"><img src="${escapeAttribute(config.brand.assets.mark)}" alt="" width="512" height="384" fetchpriority="high"></div>
      </div>
    </section>
    ${promotions.length ? `<section class="promotion-section container" aria-label="${escapeAttribute(t('promotion'))}">${promotions.map((promotion) => `
      <a class="promotion-card" href="${escapeAttribute(promotion.url)}" target="_blank" rel="noopener noreferrer sponsored">
        <span class="promotion-badge">${escapeHtml(localize(promotion.badge))}</span>
        <strong>${escapeHtml(localize(promotion.title))}</strong>
        <span>${escapeHtml(localize(promotion.subtitle))}</span><span aria-hidden="true">↗</span>
      </a>`).join('')}</section>` : ''}
    <section class="section container" aria-labelledby="overview-title">
      <div class="section-heading"><div><p class="section-kicker">API</p><h2 id="overview-title">${escapeHtml(t('overview'))}</h2></div><p class="section-status" id="home-summary-status">${escapeHtml(t('loading'))}</p></div>
      <div class="overview-grid" id="home-overview">
        ${[t('onlinePools'), t('minerConnections'), t('totalBlocks')].map((label) => `<article class="metric-card"><p>${escapeHtml(label)}</p><strong class="skeleton-text">${escapeHtml(t('loading'))}</strong></article>`).join('')}
      </div>
    </section>
    <section class="section container" id="pools" aria-labelledby="pools-title">
      <div class="section-heading"><div><p class="section-kicker">POOLS</p><h2 id="pools-title">${escapeHtml(t('choosePool'))}</h2></div></div>
      <div class="coin-grid" id="home-coins">${enabledCoins().map(() => '<div class="coin-card coin-card-loading" aria-hidden="true"></div>').join('')}</div>
    </section>
    <section class="section container" aria-labelledby="benefits-title">
      <div class="section-heading"><div><p class="section-kicker">NTM</p><h2 id="benefits-title">${escapeHtml(t('why'))}</h2></div></div>
      <div class="benefit-grid">
        ${[['01', 'benefitFeeTitle', 'benefitFeeBody'], ['02', 'benefitApiTitle', 'benefitApiBody'], ['03', 'benefitTransparentTitle', 'benefitTransparentBody']].map(([number, title, body]) => `<article class="benefit-card"><span>${number}</span><h3>${escapeHtml(t(title))}</h3><p>${escapeHtml(t(body))}</p></article>`).join('')}
      </div>
    </section>`;
}

async function renderHome() {
  const route = state.route;
  renderHomeScaffold();
  await refreshHome();
  if (state.route !== route) return;
  setAutoRefresh(refreshHome);
}

async function refreshHome() {
  if (state.route.kind !== 'home') return;
  const signal = state.controller.signal;
  const coins = enabledCoins();
  const baseGroups = new Map();
  coins.forEach((coin) => {
    const base = apiBaseFor(state.config, coin);
    if (!baseGroups.has(base)) baseGroups.set(base, []);
    baseGroups.get(base).push(coin);
  });
  const settled = await Promise.allSettled([...baseGroups.keys()].map(async (base) => {
    const record = await withSessionStale(`home:pools:${base}`, () => getPools(state.config, base, signal));
    return { base, record, pools: normalizePools(record.data) };
  }));
  if (state.route.kind !== 'home' || signal.aborted) return;
  const byBase = new Map();
  const errors = new Map();
  settled.forEach((result, index) => {
    const base = [...baseGroups.keys()][index];
    if (result.status === 'fulfilled') byBase.set(base, result.value);
    else errors.set(base, result.reason);
  });
  let onlinePoolCount = 0;
  let connectedMiners = 0;
  let totalBlocks = 0;
  for (const [base, value] of byBase) {
    const registered = new Set(baseGroups.get(base).map((coin) => coin.id));
    value.pools.forEach((pool) => { if (!registered.has(pool.id)) console.warn(`Ignoring unregistered pool ${pool.id} from ${base}`); });
    if (value.record.stale) continue;
    value.pools.forEach((pool) => {
      if (!registered.has(pool.id)) return;
      onlinePoolCount += 1;
      if (typeof pool.poolStats?.connectedMiners === 'number') connectedMiners += pool.poolStats.connectedMiners;
      if (typeof pool.totalBlocks === 'number') totalBlocks += pool.totalBlocks;
    });
  }
  const partial = errors.size > 0 || [...byBase.values()].some((value) => value.record.stale);
  document.getElementById('home-overview').innerHTML = [
    [t('onlinePools'), formatInteger(onlinePoolCount)], [t('minerConnections'), formatInteger(connectedMiners)], [t('totalBlocks'), formatInteger(totalBlocks)],
  ].map(([label, value]) => `<article class="metric-card"><p>${escapeHtml(label)}</p><strong>${escapeHtml(value)}</strong></article>`).join('');
  const summary = document.getElementById('home-summary-status');
  summary.innerHTML = partial ? statusBadge('unavailable', t('partialUnavailable')) : `${escapeHtml(t('lastUpdated'))}: ${escapeHtml(formatDate(new Date()))}`;
  const cards = rankHomeCoinCards(coins, byBase).map(({ coin, pool, status, lastSuccess }) => renderCoinCard(coin, pool, status, lastSuccess));
  document.getElementById('home-coins').innerHTML = cards.join('');
}

function rankHomeCoinCards(coins, byBase) {
  return coins.map((coin, originalIndex) => {
    const base = apiBaseFor(state.config, coin);
    const value = byBase.get(base);
    if (!value) return { coin, pool: null, status: 'unavailable', lastSuccess: null, connectedMiners: null, originalIndex };
    const pool = value.pools.find((candidate) => candidate.id === coin.id) || null;
    const status = value.record.stale && pool ? 'stale' : pool ? 'online' : 'offline';
    const connectedMiners = pool && Number.isFinite(pool.poolStats?.connectedMiners)
      ? pool.poolStats.connectedMiners
      : null;
    return { coin, pool, status, lastSuccess: value.record.lastSuccess, connectedMiners, originalIndex };
  }).sort((left, right) => {
    const leftHasMiners = Number.isFinite(left.connectedMiners);
    const rightHasMiners = Number.isFinite(right.connectedMiners);
    if (leftHasMiners !== rightHasMiners) return leftHasMiners ? -1 : 1;
    if (leftHasMiners && left.connectedMiners !== right.connectedMiners) return right.connectedMiners - left.connectedMiners;
    if (left.coin.order !== right.coin.order) return left.coin.order - right.coin.order;
    return left.originalIndex - right.originalIndex;
  });
}

function renderCoinCard(coin, pool, status, lastSuccess) {
  const dataStatus = coin.settlement.noPayout ? 'critical' : status;
  const label = coin.settlement.noPayout ? t('statusNoPayout') : t(`status${status[0].toUpperCase()}${status.slice(1)}`);
  const metrics = [];
  if (pool && (status === 'online' || status === 'stale')) {
    const stats = pool.poolStats || {};
    const network = pool.networkStats || {};
    if (hasValue(stats.poolHashrate)) metrics.push([t('poolHashrate'), formatHashrate(stats.poolHashrate, coin.hashUnit), true]);
    if (hasValue(stats.connectedMiners)) metrics.push([t('onlineMiners'), formatInteger(stats.connectedMiners)]);
    if (hasValue(stats.connectedWorkers)) metrics.push([t('onlineWorkers'), formatInteger(stats.connectedWorkers)]);
    if (!coin.settlement.noPayout && hasValue(pool.poolFeePercent)) metrics.push([t('poolFee'), formatPercent(pool.poolFeePercent)]);
    if (hasValue(network.blockHeight)) metrics.push([t('blockHeight'), formatInteger(network.blockHeight)]);
    if (!coin.settlement.noPayout && hasValue(pool.paymentProcessing?.payoutScheme)) metrics.push([t('payoutScheme'), String(pool.paymentProcessing.payoutScheme)]);
    if (!coin.settlement.noPayout && hasValue(pool.soloFeePercent)) metrics.push([t('soloFee'), formatPercent(pool.soloFeePercent)]);
  }
  return `<article class="coin-card ${coin.settlement.noPayout ? 'coin-card-critical' : ''}">
    ${coinColorLine(coin)}
    <div class="coin-card-head">${coinLogo(coin)}<div class="coin-card-title"><h3>${escapeHtml(coin.name)}</h3><p>${escapeHtml(coin.symbol)}</p></div>${statusBadge(dataStatus, label)}</div>
    ${coin.settlement.noPayout && status !== 'unavailable' ? `<p class="coin-card-data-status">${escapeHtml(t(`status${status[0].toUpperCase()}${status.slice(1)}`))}</p>` : ''}
    <p class="coin-algo">${escapeHtml(coin.algo)}</p>
    ${status === 'stale' ? `<p class="stale-line">${escapeHtml(t('staleAt', { time: formatDate(lastSuccess) }))}</p>` : ''}
    ${metrics.length ? `<dl class="coin-metrics">${metrics.map(([key, value, primary]) => `<div class="${primary ? 'primary-metric' : ''}"><dt>${escapeHtml(key)}</dt><dd>${escapeHtml(value)}</dd></div>`).join('')}</dl>` : `<div class="coin-static-state">${status === 'offline' ? escapeHtml(t('statusOffline')) : status === 'unavailable' ? escapeHtml(t('statusUnavailable')) : ''}</div>`}
    <a class="coin-card-link" href="/${encodeURIComponent(coin.id)}" data-route><span>${escapeHtml(t('viewPool'))}</span><span aria-hidden="true">→</span></a>
  </article>`;
}

function renderDownload() {
  const config = state.config;
  const coins = enabledCoins();
  main.innerHTML = `
    <section class="download-hero section">
      <div class="container download-hero-grid"><div><p class="eyebrow">${escapeHtml(config.brand.expansionEn)}</p><h1>${escapeHtml(t('downloadTitle'))}</h1><p class="download-version">${escapeHtml(config.downloads.version)}</p><p class="hero-subtitle">${escapeHtml(t('downloadSubtitle'))}</p></div><img src="${escapeAttribute(config.brand.assets.mark)}" alt="" width="300" height="225"></div>
    </section>
    <section class="section container" aria-labelledby="downloads-title"><div class="section-heading"><div><p class="section-kicker">BINARIES</p><h2 id="downloads-title">${escapeHtml(t('downloads'))}</h2></div></div><div class="download-grid">
      ${config.downloads.files.map(renderDownloadCard).join('')}
    </div></section>
    <section class="section container" aria-labelledby="verify-title"><div class="section-heading"><div><p class="section-kicker">SHA-256</p><h2 id="verify-title">${escapeHtml(t('verify'))}</h2><p>${escapeHtml(t('verifyBody'))}</p></div></div><div class="command-grid">
      ${renderCommandBox(t('windows'), `Get-FileHash -Algorithm SHA256 .\\${config.downloads.files[0].file}`)}
      ${renderCommandBox(t('linux'), `sha256sum ./${config.downloads.files[1].file}`)}
    </div></section>
    <section class="section container" aria-labelledby="quick-title"><div class="section-heading"><div><p class="section-kicker">3 STEPS</p><h2 id="quick-title">${escapeHtml(t('quickStart'))}</h2></div></div><ol class="quick-grid">
      ${[[t('quick1Title'), t('quick1Body')], [t('quick2Title'), t('quick2Body')], [t('quick3Title'), t('quick3Body')]].map(([title, body], index) => `<li><span>${index + 1}</span><div><h3>${escapeHtml(title)}</h3><p>${escapeHtml(body)}</p></div></li>`).join('')}
    </ol></section>
    <section class="section container" aria-labelledby="coin-command-title"><div class="section-heading"><div><p class="section-kicker">STRATUM</p><h2 id="coin-command-title">${escapeHtml(t('coinCommands'))}</h2></div></div><div class="coin-command-grid">
      ${coins.map(renderQuickCoinCommands).join('')}
    </div></section>
    ${renderCliReference(coins)}`;
  wireCopyButtons();
}

function renderDownloadCard(file) {
  const ready = file.status === 'ready';
  const href = `${state.config.downloads.baseUrl}/${encodeURIComponent(file.file)}`;
  return `<article class="download-card"><div class="download-card-head"><div><p class="platform-icon" aria-hidden="true">${file.icon === 'windows' ? '⊞' : '⌁'}</p><h3>${escapeHtml(file.os)}</h3></div>${statusBadge(ready ? 'online' : 'info', ready ? t('ready') : t('building'))}</div><p class="file-name">${escapeHtml(file.file)}</p><p>${escapeHtml(localize(file.note))}</p>${ready ? `<a class="button primary" href="${escapeAttribute(href)}" download>${escapeHtml(t('downloadFile'))}</a><div class="sha-block"><span>${escapeHtml(t('sha256'))}</span><code class="copy-source">${escapeHtml(file.sha256)}</code><button class="button ghost copy-button" type="button" data-copy-text="${escapeAttribute(file.sha256)}">${escapeHtml(t('copy'))}</button></div>` : ''}</article>`;
}

function firstPoolEndpoint(coin) {
  return coin.stratum.find((endpoint) => endpoint.mode !== 'solo') || coin.stratum[0];
}

// 币可用 mining.binaryOverride 指定「主推第三方锄头」（BRVA 主推官方 xmrig-brisvia）。
// 未配置的币仍走 NTMminer 默认，行为不变。
function commandFor(coin, endpoint, platform, address, extraArgs = []) {
  const override = coin.mining.binaryOverride;
  const binary = override
    ? (platform === 'windows' ? override.windows : override.linux)
    : (platform === 'windows' ? 'NTMminer-windows-x64.exe' : './NTMminer-linux-x64');
  const algo = (override && override.algoFlag) ? override.algoFlag : coin.mining.ntmAlgoFlag;
  const args = [...extraArgs, ...((override && override.extraArgs) || [])];
  const suffix = args.length ? ` ${args.join(' ')}` : '';
  return `${binary} -a ${algo} -o ${endpoint.host}:${endpoint.port} -u ${address}${suffix}`;
}

function xmrigCommandFor(coin, endpoint, address) {
  return `xmrig -a ${coin.mining.xmrigAlgoFlag} -o ${endpoint.host}:${endpoint.port} -u ${address}`;
}

function supportsGpuMining(coin) {
  return coin.mining.modes.some((mode) => mode.id === 'gpu' || mode.id === 'hybrid');
}

export function miningCommandsForMode(coin, endpoint, mode, address) {
  const args = mode.commandFlag ? [mode.commandFlag] : [];
  const commands = {
    windows: commandFor(coin, endpoint, 'windows', address, args),
    linux: commandFor(coin, endpoint, 'linux', address, args),
  };
  if (mode.id === 'gpu' || mode.id === 'hybrid') {
    const selectedArgs = [...args, '--gpu-devices', '0,2'];
    commands.selectedWindows = commandFor(coin, endpoint, 'windows', address, selectedArgs);
    commands.selectedLinux = commandFor(coin, endpoint, 'linux', address, selectedArgs);
  }
  if (mode.id === 'cpu' && coin.mining.xmrigCompatible) commands.xmrig = xmrigCommandFor(coin, endpoint, address);
  return commands;
}

function renderQuickCoinCommands(coin) {
  const endpoint = firstPoolEndpoint(coin);
  const address = commandPlaceholderFor(coin);
  return `<article class="quick-command-card ${coin.settlement.noPayout ? 'quick-command-critical' : ''}">
    <div class="quick-command-head">${coinLogo(coin)}<div><h3>${escapeHtml(coin.name)} <small>${escapeHtml(coin.symbol)}</small></h3><p>${escapeHtml(coin.algo)}</p></div></div>
    <div class="capability-tags">${coin.mining.xmrigCompatible ? `<span>${escapeHtml(t('xmrigCompatible'))}</span>` : ''}${supportsGpuMining(coin) ? `<span>${escapeHtml(t('nativeMultiGpu'))}</span>` : ''}${coin.settlement.noPayout ? statusBadge('critical', t('statusNoPayout')) : ''}</div>
    ${coin.settlement.noPayout ? `<p class="critical-copy">${escapeHtml(localize(coin.settlement.notice))}</p>` : ''}
    ${renderCommandBox(t('windows'), commandFor(coin, endpoint, 'windows', address))}
    ${renderCommandBox(t('linux'), commandFor(coin, endpoint, 'linux', address))}
  </article>`;
}

function cliBilingual(zh, en, className = '') {
  return `<div class="cli-bilingual ${escapeAttribute(className)}"><span lang="zh-CN">${escapeHtml(zh)}</span><span lang="en">${escapeHtml(en)}</span></div>`;
}

function renderCliOptionRow(option) {
  const defaultZh = option.defaultZh ?? 'help 未注明';
  const defaultEn = option.defaultEn ?? 'Not stated in help';
  const scopeZh = option.scopeZh ?? 'help 未注明限制';
  const scopeEn = option.scopeEn ?? 'No restriction stated in help';
  const aliases = `<div class="cli-alias-list">${option.aliases.map((alias) => `<button class="cli-alias-button" type="button" data-copy-text="${escapeAttribute(alias)}" aria-label="${escapeAttribute(`复制参数 ${alias} / Copy option ${alias}`)}" title="${escapeAttribute('复制参数 / Copy option')}">${escapeHtml(alias)}</button>`).join('')}</div>`;
  const value = `<code>${escapeHtml(option.value ?? '—')}</code>`;
  return `<tr>
    ${cell('参数 / Aliases', aliases, 'cli-alias-cell')}
    ${cell('取值 / Value', value, 'cli-value-cell')}
    ${cell('默认值 / Default', cliBilingual(defaultZh, defaultEn), 'cli-default-cell')}
    ${cell('说明 / Description', cliBilingual(option.descriptionZh, option.descriptionEn, 'cli-description'), 'cli-description-cell')}
    ${cell('适用范围 / Scope', cliBilingual(scopeZh, scopeEn), 'cli-scope-cell')}
  </tr>`;
}

function renderCliTable(group) {
  const headings = ['参数 / Aliases', '取值 / Value', '默认值 / Default', '说明 / Description', '适用范围 / Scope'];
  return `<div class="table-shell cli-table-shell"><table class="cli-table" aria-describedby="cli-reference-source">
    <caption><span lang="zh-CN">${escapeHtml(group.titleZh)}</span> / <span lang="en">${escapeHtml(group.titleEn)}</span></caption>
    <thead><tr>${headings.map((heading) => `<th scope="col">${escapeHtml(heading)}</th>`).join('')}</tr></thead>
    <tbody>${group.options.map(renderCliOptionRow).join('')}</tbody>
  </table></div>`;
}

function renderCliReference(coins) {
  const exampleCoin = coins.find((coin) => coin.id === 'dragonx') || coins[0];
  const endpoint = firstPoolEndpoint(exampleCoin);
  const exampleBase = `NTMminer -a ${exampleCoin.mining.ntmAlgoFlag} -o ${endpoint.host}:${endpoint.port} -u YOUR_WALLET_ADDRESS`;
  return `<section class="section container cli-reference" aria-labelledby="cli-reference-title">
    <div class="section-heading"><div><p class="section-kicker">CLI · NTMminer 1.19.0</p><h2 id="cli-reference-title"><span lang="zh-CN">完整参数说明</span> / <span lang="en">Full CLI reference</span></h2>
      <div class="cli-source-note" id="cli-reference-source">
        <p lang="zh-CN">事实源：项目方于 2026-07-18 对与站上下载项逐字节一致（SHA-256 以 49eefdd8 开头）的 v1.19.0 二进制实跑 <code>--help</code>。中文说明忠实转写原始输出；英文为对应翻译。</p>
        <p lang="en">Source of truth: raw <code>--help</code> output run by the project owner on 2026-07-18 from the v1.19.0 binary that is byte-for-byte identical to the published download (SHA-256 begins with 49eefdd8). The Chinese descriptions faithfully transcribe that output; English is the corresponding translation.</p>
      </div></div></div>
    <div class="cli-usage-grid">
      <article class="cli-usage-card"><h3><span lang="zh-CN">用法</span> / <span lang="en">Usage</span></h3>
        ${renderCommandBox('命名参数 / Named options', 'NTMminer -o <host:port> -u <wallet> [选项]')}
        ${renderCommandBox('旧位置式（兼容） / Legacy positional (compatible)', 'NTMminer <host> <port> <wallet> [bench_secs] [threads] [lanes]')}
      </article>
      <article class="cli-usage-card"><h3><span lang="zh-CN">示例</span> / <span lang="en">Examples</span></h3>
        ${renderCommandBox('示例 1 / Example 1', exampleBase)}
        ${renderCommandBox('示例 2 · SOCKS5 / Example 2 · SOCKS5', `${exampleBase} --socks5 127.0.0.1:9050`)}
      </article>
    </div>
    <div class="cli-table-stack">${CLI_REFERENCE.map(renderCliTable).join('')}</div>
  </section>`;
}

function renderCommandBox(label, command, options = {}) {
  return `<div class="command-box"><div class="command-head"><strong>${escapeHtml(label)}</strong>${options.badge ? `<span>${escapeHtml(options.badge)}</span>` : ''}<button class="button ghost copy-button" type="button" data-copy-text="${escapeAttribute(command)}" ${options.disabled ? 'disabled' : ''}>${escapeHtml(t('copy'))}</button></div><pre><code class="copy-source">${escapeHtml(command)}</code></pre></div>`;
}

async function renderCoinPage(route) {
  const coin = route.coin;
  main.innerHTML = `
    <section class="coin-page-head container">
      <div class="coin-identity">${coinLogo(coin, true)}<div><div class="coin-name-row"><h1>${escapeHtml(coin.name)}</h1>${coin.settlement.noPayout ? statusBadge('critical', t('statusNoPayout')) : '<span id="coin-status">' + statusBadge('info', t('loading')) + '</span>'}</div><p>${escapeHtml(coin.symbol)} · ${escapeHtml(coin.algo)}</p></div></div>
      <div class="coin-links"><a href="${escapeAttribute(coin.links.site)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('officialSite'))} ↗</a><a href="${escapeAttribute(coin.links.git)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('sourceCode'))} ↗</a></div>
      ${coin.settlement.noPayout ? `<div id="coin-data-status" class="coin-data-status">${statusBadge('info', t('loading'))}</div>` : ''}
    </section>
    ${coin.settlement.noPayout ? renderNoPayoutBanner(coin) : ''}
    ${coin.settlement.payoutPaused ? renderPayoutPausedBanner(coin) : ''}
    <div class="tabs-shell"><div class="container"><div class="tabs" role="tablist" aria-label="${escapeAttribute(coin.name)}">
      ${tabsForCoin(coin).map((tab) => `<a id="tab-${tab}" role="tab" aria-controls="coin-panel" aria-selected="${String(route.tab === tab)}" class="tab ${route.tab === tab ? 'active' : ''}" href="/${encodeURIComponent(coin.id)}#${tab}" data-route>${escapeHtml(t(`tab${tab[0].toUpperCase()}${tab.slice(1)}`))}</a>`).join('')}
    </div></div></div>
    <section class="section coin-panel container" id="coin-panel" role="tabpanel" aria-labelledby="tab-${route.tab}" tabindex="0"><div class="panel-loading">${escapeHtml(t('loading'))}</div></section>`;
  const tabList = main.querySelector('.tabs');
  tabList.addEventListener('keydown', (event) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    const tabs = [...tabList.querySelectorAll('[role="tab"]')];
    const current = Math.max(0, tabs.indexOf(document.activeElement));
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : event.key === 'ArrowRight' ? (current + 1) % tabs.length : (current - 1 + tabs.length) % tabs.length;
    event.preventDefault();
    tabs[next].focus();
    navigate(tabs[next].getAttribute('href'));
  });
  const presencePromise = refreshCoinPresence(route);
  const tabPromise = renderCoinTab(route);
  await Promise.allSettled([presencePromise, tabPromise]);
}

function renderNoPayoutBanner(coin) {
  return `<section class="no-payout-banner container" role="alert"><div class="warning-icon" aria-hidden="true">⚠</div><div><span class="critical-pill">${escapeHtml(t('statusNoPayout'))}</span><h2>NO PAYOUTS / 不打款</h2><p>${escapeHtml(localize(coin.settlement.notice))}</p></div></section>`;
}

function renderPayoutPausedBanner(coin) {
  return `<section class="payout-paused-banner container" role="status"><div class="warning-icon" aria-hidden="true">⏸</div><div><h2>${escapeHtml(t('payoutPausedTitle'))}</h2><p>${escapeHtml(localize(coin.account?.notice))}</p></div><a class="button secondary" href="/${encodeURIComponent(coin.id)}#account" data-route>${escapeHtml(t('viewGuide'))}</a></section>`;
}

function tabsForCoin(coin) {
  const tabs = BASE_TABS.filter((tab) => !((coin.settlement.noPayout || coin.account?.paymentHistoryEnabled === false) && tab === 'payments'));
  if (isDOMAccountCoin(coin)) {
    tabs.splice(Math.max(0, tabs.indexOf('connect')), 0, 'account');
  }
  return tabs;
}

async function refreshCoinPresence(route) {
  const coin = route.coin;
  try {
    const base = apiBaseFor(state.config, coin);
    const record = await withSessionStale(`coin-presence:${base}`, () => getPools(state.config, base, state.controller.signal));
    if (state.route !== route) return;
    const pools = normalizePools(record.data);
    const online = pools.some((pool) => pool.id === coin.id);
    const kind = record.stale ? 'stale' : online ? 'online' : 'offline';
    const label = record.stale ? t('statusStale') : online ? t('statusOnline') : t('statusOffline');
    const target = document.getElementById(coin.settlement.noPayout ? 'coin-data-status' : 'coin-status');
    if (target) target.innerHTML = statusBadge(kind, label);
  } catch (error) {
    if (error.code === 'aborted') return;
    const target = document.getElementById(coin.settlement.noPayout ? 'coin-data-status' : 'coin-status');
    if (target) target.innerHTML = statusBadge('unavailable', t('statusUnavailable'));
  }
}

async function renderCoinTab(route) {
  if (route.tab === 'dashboard') await renderDashboard(route);
  else if (route.tab === 'blocks') await renderPagedTable(route, 'blocks');
  else if (route.tab === 'payments') await renderPagedTable(route, 'payments');
  else if (route.tab === 'miners') await renderMiners(route);
  else if (route.tab === 'lookup') renderLookup(route);
  else if (route.tab === 'account' && isDOMAccountCoin(route.coin)) await renderDOMAccount(route);
  else if (route.tab === 'connect') await renderConnect(route);
}

async function renderDOMAccount(route) {
  const panel = document.getElementById('coin-panel');
  try {
    const { renderDOMAccountPage } = await import('./dom-account-page.20260720g.js?release=20260721a');
    if (state.route !== route || state.controller.signal.aborted) return;
    state.routeHandle = renderDOMAccountPage(panel, {
      language: getLanguage(),
      endpoint: firstPoolEndpoint(route.coin),
      downloads: state.config.downloads.files,
      tutorialImage: route.coin.account.tutorialImage,
      payoutPaused: route.coin.settlement.payoutPaused,
      registrationEnabled: route.coin.account.registrationEnabled,
      claimsEnabled: route.coin.account.claimsEnabled,
    });
  } catch (error) {
    if (state.route !== route || state.controller.signal.aborted) return;
    panel.innerHTML = errorBox(error);
  }
}

function metricCard(label, value, primary = false, unit = '') {
  if (!hasValue(value)) return '';
  return `<article class="metric-card ${primary ? 'metric-primary' : ''}"><p>${escapeHtml(label)}</p><strong>${escapeHtml(value)}</strong>${unit ? `<span>${escapeHtml(unit)}</span>` : ''}</article>`;
}

async function renderDashboard(route) {
  const panel = document.getElementById('coin-panel');
  const coin = route.coin;
  const signal = state.controller.signal;
  const [poolResult, performanceResult, blocksResult] = await Promise.allSettled([
    withSessionStale(`dashboard:pool:${coin.id}`, () => getPool(state.config, coin, signal)),
    withSessionStale(`dashboard:performance:${coin.id}`, () => getPerformance(state.config, coin, signal)),
    withSessionStale(`dashboard:blocks:${coin.id}`, () => getBlocks(state.config, coin, 0, state.config.api.dashboardBlockCount, signal)),
  ]);
  if (state.route !== route || signal.aborted) return;
  const poolRecord = poolResult.status === 'fulfilled' ? poolResult.value : null;
  const pool = poolRecord ? normalizePoolDetail(poolRecord.data) : null;
  const performanceRecord = performanceResult.status === 'fulfilled' ? performanceResult.value : null;
  const samples = performanceRecord ? normalizePerformance(performanceRecord.data) : [];
  const blocksRecord = blocksResult.status === 'fulfilled' ? blocksResult.value : null;
  const blocks = blocksRecord ? normalizeList(blocksRecord.data) : [];
  const metrics = [];
  if (pool) {
    const stats = pool.poolStats || {};
    const network = pool.networkStats || {};
    metrics.push(metricCard(t('poolHashrate'), hasValue(stats.poolHashrate) ? formatHashrate(stats.poolHashrate, coin.hashUnit) : null, true));
    metrics.push(metricCard(t('networkHashrate'), hasValue(network.networkHashrate) ? formatHashrate(network.networkHashrate, coin.hashUnit) : null, true));
    const share = typeof stats.poolHashrate === 'number' && typeof network.networkHashrate === 'number' && network.networkHashrate > 0 ? (stats.poolHashrate / network.networkHashrate) * 100 : null;
    metrics.push(metricCard(t('poolShare'), share === null ? null : formatPercent(share), true));
    metrics.push(metricCard(t('onlineMiners'), hasValue(stats.connectedMiners) ? formatInteger(stats.connectedMiners) : null));
    metrics.push(metricCard(t('onlineWorkers'), hasValue(stats.connectedWorkers) ? formatInteger(stats.connectedWorkers) : null));
    metrics.push(metricCard(t('blockHeight'), hasValue(network.blockHeight) ? formatInteger(network.blockHeight) : null));
    metrics.push(metricCard(t('networkDifficulty'), hasValue(network.networkDifficulty) ? formatDecimal(network.networkDifficulty, 0, 2) : null));
    metrics.push(metricCard(t('totalBlocks'), hasValue(pool.totalBlocks) ? formatInteger(pool.totalBlocks) : null));
    metrics.push(metricCard(t('confirmedBlocks'), hasValue(pool.totalConfirmedBlocks) ? formatInteger(pool.totalConfirmedBlocks) : null));
    metrics.push(metricCard(t('orphanedBlocks'), hasValue(pool.totalOrphanedBlocks) ? formatInteger(pool.totalOrphanedBlocks) : null));
    if (!coin.settlement.noPayout) {
      metrics.push(metricCard(t('totalPaid'), hasValue(pool.totalPaid) ? formatAmount(pool.totalPaid, coin) : null));
      metrics.push(metricCard(t('poolFee'), hasValue(pool.poolFeePercent) ? formatPercent(pool.poolFeePercent) : null));
      metrics.push(metricCard(t('soloFee'), hasValue(pool.soloFeePercent) ? formatPercent(pool.soloFeePercent) : null));
      metrics.push(metricCard(t('payoutScheme'), hasValue(pool.paymentProcessing?.payoutScheme) ? String(pool.paymentProcessing.payoutScheme) : null));
      metrics.push(metricCard(coin.settlement.directPayout ? t('dustThreshold') : t('minimumPayment'), hasValue(pool.paymentProcessing?.minimumPayment) ? formatAmount(pool.paymentProcessing.minimumPayment, coin) : null));
    }
  }
  const staleRecords = [poolRecord, performanceRecord, blocksRecord].filter((record) => record?.stale);
  clearChartHandles();
  panel.innerHTML = `
    ${staleRecords.length ? `<div class="stale-banner">${statusBadge('stale', t('statusStale'))}<span>${escapeHtml(t('staleAt', { time: formatDate(staleRecords[0].lastSuccess) }))}</span></div>` : ''}
    ${pool ? `<div class="metric-grid">${metrics.filter(Boolean).join('') || emptyBox(t('noData'))}</div>` : errorBox(poolResult.reason)}
    <div class="dashboard-grid">
      <article class="chart-card"><div class="chart-card-head"><div><p class="section-kicker">PERFORMANCE</p><h2>${escapeHtml(t('hashrateHistory'))}</h2></div><p>${performanceRecord ? `${escapeHtml(t('lastUpdated'))}: ${escapeHtml(formatDate(performanceRecord.lastSuccess))}` : ''}</p></div><div id="hashrate-chart" class="chart-host">${performanceRecord ? '' : errorBox(performanceResult.reason)}</div></article>
      <article class="chart-card"><div class="chart-card-head"><div><p class="section-kicker">BLOCKS</p><h2>${escapeHtml(t('recentBlocks'))}</h2></div></div>${coin.settlement.noPayout ? `<p class="critical-copy">${escapeHtml(t('noPayoutBlockNote'))}</p>` : ''}<div id="blocks-chart" class="chart-host">${blocksRecord ? '' : errorBox(blocksResult.reason)}</div></article>
    </div>
    <article class="about-card"><div><p class="section-kicker">${escapeHtml(coin.symbol)}</p><h2>${escapeHtml(t('coinAbout'))}</h2></div><p>${escapeHtml(localize(coin.description))}</p><div class="about-links"><a href="${escapeAttribute(coin.links.site)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('officialSite'))} ↗</a><a href="${escapeAttribute(coin.links.git)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('sourceCode'))} ↗</a></div></article>`;
  if (performanceRecord) {
    const chartPoints = preparePerformance(samples, state.config.api.performanceBucketMs, 'poolHashrate');
    const summary = `${t('hashrateHistory')}: ${chartPoints.length} samples, ${chartPoints.length ? `${formatDate(chartPoints[0].time)} – ${formatDate(chartPoints[chartPoints.length - 1].time)}` : t('noData')}.`;
    state.chartHandles.push(renderHashrateChart(document.getElementById('hashrate-chart'), samples, {
      id: `hashrate-${coin.id}`, title: t('hashrateHistory'), summary, color: coin.color,
      bucketMs: state.config.api.performanceBucketMs, emptyText: t('notEnoughHistory'), valueField: 'poolHashrate',
      formatValue: (value) => formatHashrate(value, coin.hashUnit), formatTime: (date) => `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`,
      formatDateTime: dateParts,
    }));
  }
  if (blocksRecord) {
    const summary = `${t('recentBlocks')}: ${blocks.length}.`;
    state.chartHandles.push(renderBlocksTimeline(document.getElementById('blocks-chart'), blocks, {
      id: `blocks-${coin.id}`, title: t('recentBlocks'), summary, emptyText: t('emptyBlocks'), status: translateStatus,
      colors: { confirmed: '#3DDC97', pending: '#FFCC66', orphaned: '#FF5C73', unknown: '#B8D6E6', solo: '#8072FA' },
      formatDateTime: dateParts, formatInteger, formatAmount: (_value, block) => formatBlockReward(block, coin), heightLabel: t('blockHeight'), rewardLabel: t('reward'),
      pointLabel: (block, status) => `${t('blockHeight')} ${formatInteger(block.blockHeight)}, ${status}, ${t('reward')} ${formatBlockReward(block, coin)}`,
    }));
  }
  setAutoRefresh(() => renderDashboard(route));
}

async function renderPagedTable(route, kind) {
  const panel = document.getElementById('coin-panel');
  const coin = route.coin;
  const pageSize = state.config.api.pageSize;
  const apiPage = route.page - 1;
  try {
    const response = kind === 'blocks'
      ? await getBlocks(state.config, coin, apiPage, pageSize, state.controller.signal)
      : await getPayments(state.config, coin, apiPage, pageSize, state.controller.signal);
    if (state.route !== route) return;
    const rows = normalizeList(response.data);
    panel.innerHTML = `${kind === 'blocks' && coin.settlement.noPayout ? `<div class="no-payout-table-note">${statusBadge('critical', t('statusNoPayout'))}<span>${escapeHtml(t('noPayoutBlockNote'))}</span></div>` : ''}${kind === 'blocks' ? blocksTable(rows, coin) : paymentsTable(rows, coin)}${paginationMarkup(route, rows.length, response.total)}`;
    wireCopyButtons(panel);
  } catch (error) {
    if (error.code !== 'aborted') panel.innerHTML = errorBox(error, 'retry-table');
    document.getElementById('retry-table')?.addEventListener('click', () => renderPagedTable(route, kind));
  }
}

function cell(label, value, className = '') {
  return `<td class="${escapeAttribute(className)}"><span class="cell-label">${escapeHtml(label)}</span>${value}</td>`;
}

function blocksTable(rows, coin) {
  if (!rows.length) return emptyBox(t('emptyBlocks'));
  return `<div class="table-shell"><table><caption>${escapeHtml(t('blocksCaption'))}</caption><thead><tr>${[t('blockHeight'), t('status'), t('confirmations'), t('reward'), t('miner'), t('worker'), t('time'), t('hash')].map((label) => `<th scope="col">${escapeHtml(label)}</th>`).join('')}</tr></thead><tbody>${rows.map((block) => {
    const status = translateStatus(block.status);
    const statusMarkup = statusBadge(status.known ? (status.kind === 'confirmed' ? 'online' : status.kind === 'orphaned' ? 'critical' : 'stale') : 'unknown', status.text);
    const confirmation = hasValue(block.confirmationProgress) ? `${formatDecimal(Number(block.confirmationProgress) * (Number(block.confirmationProgress) <= 1 ? 100 : 1), 0, 2)}%` : t('noData');
    const miner = maskAddress(block.miner || block.minerId || '');
    const fullHash = String(block.hash || '');
    return `<tr>${cell(t('blockHeight'), escapeHtml(formatInteger(block.blockHeight)), 'numeric')}${cell(t('status'), `${statusMarkup}${block.solo ? `<span class="solo-tag">${escapeHtml(t('solo'))}</span>` : ''}`)}${cell(t('confirmations'), escapeHtml(confirmation), 'numeric')}${cell(t('reward'), escapeHtml(formatBlockReward(block, coin)), 'numeric')}${cell(t('miner'), `<span class="mono">${escapeHtml(miner || t('noData'))}</span>`)}${cell(t('worker'), escapeHtml(block.worker || t('noData')))}${cell(t('time'), timeMarkup(block.created))}${cell(t('hash'), fullHash ? `<div class="copy-cell"><code class="copy-source">${escapeHtml(shortValue(fullHash))}</code><button class="button ghost copy-button" type="button" data-copy-text="${escapeAttribute(fullHash)}">${escapeHtml(t('copy'))}</button></div>` : escapeHtml(t('noData')), 'hash-cell')}</tr>`;
  }).join('')}</tbody></table></div>`;
}

function paymentsTable(rows, coin) {
  if (!rows.length) return emptyBox(t('emptyPayments'));
  return `<div class="table-shell"><table><caption>${escapeHtml(t('paymentsCaption'))}</caption><thead><tr>${[t('time'), t('address'), t('amount'), t('status'), t('transaction')].map((label) => `<th scope="col">${escapeHtml(label)}</th>`).join('')}</tr></thead><tbody>${rows.map((payment) => {
    const status = translateStatus(payment.status);
    const txid = String(payment.transactionConfirmationData || '');
    return `<tr>${cell(t('time'), timeMarkup(payment.created))}${cell(t('address'), `<span class="mono">${escapeHtml(maskAddress(payment.address || payment.addressId || ''))}</span>`)}${cell(t('amount'), escapeHtml(formatAmount(payment.amount, coin)), 'numeric')}${cell(t('status'), statusBadge(status.known ? (status.kind === 'paid' || status.kind === 'confirmed' || status.kind === 'sent' ? 'online' : status.kind === 'failed' || status.kind === 'voided' ? 'critical' : 'stale') : 'unknown', status.text))}${cell(t('transaction'), txid ? `<div class="copy-cell"><code class="copy-source">${escapeHtml(shortValue(txid))}</code><button class="button ghost copy-button" type="button" data-copy-text="${escapeAttribute(txid)}">${escapeHtml(t('copy'))}</button></div>` : escapeHtml(t('noData')), 'hash-cell')}</tr>`;
  }).join('')}</tbody></table></div>`;
}

function paginationMarkup(route, rowCount, total) {
  const pageSize = state.config.api.pageSize;
  const pages = typeof total === 'number' ? Math.max(1, Math.ceil(total / pageSize)) : null;
  const previousDisabled = route.page <= 1;
  const nextDisabled = pages ? route.page >= pages : rowCount < pageSize;
  const href = (page) => `/${encodeURIComponent(route.coin.id)}?page=${page}#${route.tab}`;
  return `<nav class="pagination" aria-label="${escapeAttribute(t('page', { page: route.page }))}">${previousDisabled ? `<span class="button secondary disabled" aria-disabled="true">${escapeHtml(t('previous'))}</span>` : `<a class="button secondary" href="${href(route.page - 1)}" data-route>${escapeHtml(t('previous'))}</a>`}<span>${escapeHtml(pages ? t('pageOf', { page: route.page, pages }) : t('page', { page: route.page }))}</span>${nextDisabled ? `<span class="button secondary disabled" aria-disabled="true">${escapeHtml(t('next'))}</span>` : `<a class="button secondary" href="${href(route.page + 1)}" data-route>${escapeHtml(t('next'))}</a>`}</nav>`;
}

async function renderMiners(route) {
  const panel = document.getElementById('coin-panel');
  try {
    const record = await withSessionStale(`miners:${route.coin.id}`, () => getMiners(state.config, route.coin, state.controller.signal));
    if (state.route !== route) return;
    const rows = normalizeList(record.data);
    panel.innerHTML = `${record.stale ? `<div class="stale-banner">${statusBadge('stale', t('statusStale'))}<span>${escapeHtml(t('staleAt', { time: formatDate(record.lastSuccess) }))}</span></div>` : ''}${rows.length ? `<div class="table-shell"><table><caption>${escapeHtml(t('minersCaption'))}</caption><thead><tr>${[t('rank'), t('miner'), t('hashrate'), t('sharesPerSecond')].map((label) => `<th scope="col">${escapeHtml(label)}</th>`).join('')}</tr></thead><tbody>${rows.map((miner, index) => `<tr>${cell(t('rank'), escapeHtml(formatInteger(index + 1)), 'numeric')}${cell(t('miner'), `<span class="mono">${escapeHtml(miner.miner || miner.minerId || t('noData'))}</span>`)}${cell(t('hashrate'), escapeHtml(formatHashrate(miner.hashrate, route.coin.hashUnit)), 'numeric')}${cell(t('sharesPerSecond'), escapeHtml(formatDecimal(miner.sharesPerSecond, 0, 6)), 'numeric')}</tr>`).join('')}</tbody></table></div>` : emptyBox(t('emptyMiners'))}`;
    setAutoRefresh(() => renderMiners(route));
  } catch (error) {
    if (error.code !== 'aborted') panel.innerHTML = errorBox(error, 'retry-miners');
    document.getElementById('retry-miners')?.addEventListener('click', () => renderMiners(route));
  }
}

function isDOMAccountCoin(coin) {
  return coin?.id === 'dom' && coin.account?.enabled === true && coin.account.type === DOM_ACCOUNT_TYPE;
}

function isValidMiningIdentity(coin, value) {
  const candidate = String(value || '').trim();
  if (isDOMAccountCoin(coin)) return DOM_PUBLIC_ID.test(candidate);
  return candidate.length > 0 && candidate.length <= 256 && !DOM_RESERVED_INPUT.test(candidate);
}

function miningIdentityLabel(coin) {
  return isDOMAccountCoin(coin) ? t('publicMiningId') : t('walletAddress');
}

function miningIdentityPlaceholder(coin) {
  return isDOMAccountCoin(coin) ? t('publicMiningIdPlaceholder') : localize(coin.wallet.example);
}

function commandPlaceholderFor(coin) {
  return isDOMAccountCoin(coin) ? t('publicMiningIdCommandPlaceholder') : t('commandPlaceholder');
}

function addressFor(coin) {
  if (state.addresses.has(coin.id)) {
    const cached = state.addresses.get(coin.id);
    if (!cached || isValidMiningIdentity(coin, cached)) return cached;
    state.addresses.set(coin.id, '');
    try { localStorage.removeItem(`ntm_addr_${coin.id}`); } catch (_) { /* no-op */ }
    return '';
  }
  let value = '';
  try { value = localStorage.getItem(`ntm_addr_${coin.id}`) || ''; } catch (_) { /* no-op */ }
  if (value && !isValidMiningIdentity(coin, value)) {
    value = '';
    try { localStorage.removeItem(`ntm_addr_${coin.id}`); } catch (_) { /* no-op */ }
  }
  state.addresses.set(coin.id, value);
  return value;
}

function rememberFor(coin) {
  if (state.remember.has(coin.id)) return state.remember.get(coin.id);
  let value = true;
  try { value = localStorage.getItem(`ntm_remember_${coin.id}`) !== 'false'; } catch (_) { /* no-op */ }
  state.remember.set(coin.id, value);
  return value;
}

function saveAddress(coin, address, remember) {
  if (address && !isValidMiningIdentity(coin, address)) {
    address = '';
    remember = false;
  }
  state.addresses.set(coin.id, address);
  state.remember.set(coin.id, remember);
  try {
    localStorage.setItem(`ntm_remember_${coin.id}`, String(remember));
    if (remember && address) localStorage.setItem(`ntm_addr_${coin.id}`, address);
    else localStorage.removeItem(`ntm_addr_${coin.id}`);
  } catch (_) { /* no-op */ }
}

function renderLookup(route) {
  const panel = document.getElementById('coin-panel');
  const coin = route.coin;
  const address = addressFor(coin);
  const remember = rememberFor(coin);
  panel.innerHTML = `
    <section class="form-card" aria-labelledby="lookup-title"><div><p class="section-kicker">PRIVATE QUERY</p><h2 id="lookup-title">${escapeHtml(t('lookupTitle'))}</h2><p>${escapeHtml(t('lookupPrivacy'))}</p></div><form id="lookup-form"><label for="lookup-address">${escapeHtml(miningIdentityLabel(coin))}</label><div class="input-row"><input id="lookup-address" name="address" type="text" inputmode="text" maxlength="256" autocomplete="off" value="${escapeAttribute(address)}" placeholder="${escapeAttribute(miningIdentityPlaceholder(coin))}"><button class="button primary" type="submit">${escapeHtml(t('query'))}</button><button class="button secondary" type="button" id="lookup-clear">${escapeHtml(t('clear'))}</button></div><label class="check-row"><input id="lookup-remember" type="checkbox" ${remember ? 'checked' : ''}><span>${escapeHtml(t('rememberAddress'))}</span></label></form><div id="lookup-live" class="form-live" aria-live="polite"></div></section>
    <div id="lookup-results">${state.lookup.has(coin.id) ? renderLookupResult(route, state.lookup.get(coin.id)) : ''}</div>`;
  const form = document.getElementById('lookup-form');
  const input = document.getElementById('lookup-address');
  const rememberInput = document.getElementById('lookup-remember');
  input.addEventListener('input', () => state.addresses.set(coin.id, isValidMiningIdentity(coin, input.value) ? input.value.trim() : ''));
  rememberInput.addEventListener('change', () => saveAddress(coin, input.value.trim(), rememberInput.checked));
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    await queryMiner(route, input.value, rememberInput.checked);
  });
  document.getElementById('lookup-clear').addEventListener('click', () => {
    input.value = '';
    state.addresses.set(coin.id, '');
    state.lookup.delete(coin.id);
    state.lookupAutoQueried.delete(coin.id);
    try { localStorage.removeItem(`ntm_addr_${coin.id}`); } catch (_) { /* no-op */ }
    document.getElementById('lookup-results').replaceChildren();
    document.getElementById('lookup-live').textContent = '';
    input.focus();
  });
  if (address && !state.lookupAutoQueried.has(coin.id) && !state.lookup.has(coin.id)) {
    state.lookupAutoQueried.add(coin.id);
    queryMiner(route, address, remember);
  } else if (state.lookup.has(coin.id)) hydrateLookupChart(route, state.lookup.get(coin.id));
}

async function queryMiner(route, rawAddress, remember) {
  const address = rawAddress.trim();
  const live = document.getElementById('lookup-live');
  if (!isValidMiningIdentity(route.coin, address)) {
    live.textContent = isDOMAccountCoin(route.coin) ? t('invalidMiningId') : t('error400');
    return;
  }
  saveAddress(route.coin, address, remember);
  live.textContent = t('loading');
  try {
    const response = await getMiner(state.config, route.coin, address, state.controller.signal);
    if (state.route !== route) return;
    state.lookup.set(route.coin.id, response.data);
    live.textContent = `${t('lastUpdated')}: ${formatDate(response.receivedAt)}`;
    clearChartHandles();
    document.getElementById('lookup-results').innerHTML = renderLookupResult(route, response.data);
    hydrateLookupChart(route, response.data);
  } catch (error) {
    if (error.code === 'aborted') return;
    console.error(error);
    live.textContent = apiMessage(error);
    document.getElementById('lookup-results').replaceChildren();
  }
}

function renderLookupResult(route, data) {
  const coin = route.coin;
  const performance = data?.performance || {};
  const metrics = [metricCard(t('currentHashrate'), hasValue(performance.hashrate) ? formatHashrate(performance.hashrate, coin.hashUnit) : null, true)];
  if (!coin.settlement.noPayout) {
    metrics.push(metricCard(t('pendingBalance'), hasValue(data?.pendingBalance) ? formatAmount(data.pendingBalance, coin) : null));
    metrics.push(metricCard(t('totalPaid'), hasValue(data?.totalPaid) ? formatAmount(data.totalPaid, coin) : null));
    const last = hasValue(data?.lastPaymentAmount) ? `${formatAmount(data.lastPaymentAmount, coin)} · ${formatDate(data.lastPayment)}` : hasValue(data?.lastPayment) ? formatDate(data.lastPayment) : null;
    metrics.push(metricCard(t('lastPayment'), last));
  }
  const workers = performance.workers && typeof performance.workers === 'object' ? Object.entries(performance.workers) : [];
  const cutoff = Date.now() - 24 * 60 * 60 * 1000;
  const historical = new Set();
  (Array.isArray(data?.performanceSamples) ? data.performanceSamples : []).forEach((sample) => {
    const time = new Date(sample?.created).getTime();
    if (Number.isFinite(time) && time >= cutoff && sample.workers && typeof sample.workers === 'object') Object.keys(sample.workers).forEach((name) => historical.add(name));
  });
  const currentNames = new Set(workers.map(([name]) => name));
  const offline = [...historical].filter((name) => !currentNames.has(name));
  const recentPayments = !coin.settlement.noPayout && Array.isArray(data?.recentPayments) ? data.recentPayments : [];
  // 近 7 日到账曲线：payments7d 字段存在（=新版池 API）才渲染卡片；旧版池优雅降级不显示
  const payments7d = !coin.settlement.noPayout && Array.isArray(data?.payments7d) ? data.payments7d : null;
  const payoutCard = payments7d
    ? `<article class="chart-card"><div class="chart-card-head"><div><p class="section-kicker">7D</p><h2>${escapeHtml(t('payout7dTitle'))}</h2></div><p>${escapeHtml(t('payout7dTotal'))}: ${escapeHtml(formatAmount(sumPayments7dLocal(payments7d), coin))} · ${escapeHtml(t('payout7dDayBoundary'))}</p></div><div id="lookup-payout-chart" class="chart-host"></div></article>`
    : '';
  return `<section class="lookup-results-section"><div class="metric-grid">${metrics.filter(Boolean).join('') || emptyBox(t('noData'))}</div><article class="chart-card"><div class="chart-card-head"><div><p class="section-kicker">24H</p><h2>${escapeHtml(t('hashrateHistory'))}</h2></div></div><div id="lookup-chart" class="chart-host"></div></article>${payoutCard}<div class="worker-grid"><article class="worker-card"><h2>${escapeHtml(t('onlineWorker'))}</h2>${workers.length ? `<dl>${workers.map(([name, worker]) => `<div><dt>${escapeHtml(name)}</dt><dd>${escapeHtml(formatHashrate(worker?.hashrate, coin.hashUnit))}<small>${escapeHtml(formatDecimal(worker?.sharesPerSecond, 0, 6))} shares/s</small></dd></div>`).join('')}</dl>` : emptyBox(t('emptyMiners'))}</article><article class="worker-card"><h2>${escapeHtml(t('offlineWorker'))}</h2><p>${escapeHtml(t('offlineWorkerDefinition'))}</p>${offline.length ? `<ul>${offline.map((name) => `<li>${escapeHtml(name)}</li>`).join('')}</ul>` : emptyBox(t('emptyMiners'))}</article></div>${recentPayments.length ? `<div class="lookup-payments"><h2>${escapeHtml(t('recentPayments'))}</h2>${paymentsTable(recentPayments, coin)}</div>` : ''}</section>`;
}

function hydrateLookupChart(route, data) {
  const target = document.getElementById('lookup-chart');
  if (!target) return;
  const samples = Array.isArray(data?.performanceSamples) ? data.performanceSamples : [];
  const chartPoints = preparePerformance(samples, state.config.api.performanceBucketMs, 'hashrate');
  const summary = `${t('hashrateHistory')}: ${chartPoints.length} samples.`;
  state.chartHandles.push(renderHashrateChart(target, samples, {
    id: `lookup-${route.coin.id}`, title: t('hashrateHistory'), summary, color: route.coin.color,
    bucketMs: state.config.api.performanceBucketMs, emptyText: t('notEnoughHistory'), valueField: 'hashrate',
    formatValue: (value) => formatHashrate(value, route.coin.hashUnit), formatTime: (date) => `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`,
    formatDateTime: dateParts,
  }));
  const payoutTarget = document.getElementById('lookup-payout-chart');
  if (payoutTarget && Array.isArray(data?.payments7d)) {
    const pad = (value) => String(value).padStart(2, '0');
    state.chartHandles.push(renderDailyPayoutChart(payoutTarget, data.payments7d, {
      id: `lookup-payout-${route.coin.id}`, title: t('payout7dTitle'),
      summary: `${t('payout7dTitle')}: ${data.payments7d.length} payouts.`, color: route.coin.color,
      emptyText: t('payout7dEmpty'),
      formatValue: (value) => formatAmount(value, route.coin),
      formatAxisValue: (value) => formatDecimal(value, 0, 4),
      formatDay: (date) => `${date.getMonth() + 1}/${date.getDate()}`,
      formatDayFull: (date) => `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
      countText: (count) => t('payout7dCount').replace('{count}', String(count)),
    }));
  }
}

// 7 日合计：与 renderDailyPayoutChart 相同的本地时区日界口径（今天与之前 6 个自然日）
function sumPayments7dLocal(payments) {
  const now = new Date();
  const first = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 6).getTime();
  let total = 0;
  for (const row of payments) {
    const time = new Date(row?.created).getTime();
    const amount = typeof row?.amount === 'number' ? row.amount : Number(row?.amount);
    if (!Number.isFinite(time) || !Number.isFinite(amount) || amount < 0 || time < first) continue;
    total += amount;
  }
  return total;
}

function noPayoutAcknowledged(coin) {
  try { return sessionStorage.getItem(`ntm_no_payout_ack_${coin.id}`) === 'true'; }
  catch (_) { return false; }
}

function miningModeTitle(modeId) {
  const keys = { cpu: 'modeCpuTitle', gpu: 'modeGpuTitle', hybrid: 'modeHybridTitle' };
  return t(keys[modeId]);
}

function miningParameterText(option, field) {
  const language = getLanguage();
  const localizedField = `${field}${language === 'zh' ? 'Zh' : 'En'}`;
  if (option[localizedField]) return option[localizedField];
  return field === 'default' ? t('notStatedInHelp') : t('noScopeInHelp');
}

function renderMiningParameter(parameterId) {
  const option = MINING_PARAMETER_OPTIONS.get(parameterId);
  if (!option) return '';
  const aliases = option.aliases.map((alias) => `<button class="cli-alias-button" type="button" data-copy-text="${escapeAttribute(alias)}" aria-label="${escapeAttribute(`${t('copyParameter')} ${alias}`)}" title="${escapeAttribute(t('copyParameter'))}">${escapeHtml(alias)}</button>`).join('');
  const value = option.value ? ` <code>${escapeHtml(option.value)}</code>` : '';
  return `<div><dt><span class="cli-alias-list">${aliases}</span>${value}</dt><dd><p>${escapeHtml(miningParameterText(option, 'description'))}</p><small>${escapeHtml(t('defaultValue'))}: ${escapeHtml(miningParameterText(option, 'default'))} · ${escapeHtml(t('scope'))}: ${escapeHtml(miningParameterText(option, 'scope'))}</small></dd></div>`;
}

function renderMiningMode(coin, endpoint, mode, address, blocked) {
  const title = miningModeTitle(mode.id);
  const modeCommands = miningCommandsForMode(coin, endpoint, mode, address);
  const commands = [
    renderCommandBox(`${t('windows')} · ${title}`, modeCommands.windows, { badge: localize(endpoint.label), disabled: blocked }),
    renderCommandBox(`${t('linux')} · ${title}`, modeCommands.linux, { badge: localize(endpoint.label), disabled: blocked }),
  ];
  if (mode.id === 'gpu' || mode.id === 'hybrid') {
    commands.push(renderCommandBox(`${t('windows')} · ${t('selectedGpuExample')}`, modeCommands.selectedWindows, { badge: 'GPU 0,2', disabled: blocked }));
    commands.push(renderCommandBox(`${t('linux')} · ${t('selectedGpuExample')}`, modeCommands.selectedLinux, { badge: 'GPU 0,2', disabled: blocked }));
  }
  if (mode.id === 'cpu' && coin.mining.xmrigCompatible) {
    commands.push(renderCommandBox(`xmrig · ${title}`, modeCommands.xmrig, { badge: t('xmrigCompatible'), disabled: blocked }));
  }
  return `<article class="settlement-card"><p class="section-kicker">${escapeHtml(t(`mode${mode.id[0].toUpperCase()}${mode.id.slice(1)}Kicker`))}</p><h3>${escapeHtml(title)}</h3><p>${escapeHtml(localize(mode.description))}</p><div class="command-stack">${commands.join('')}</div><h4>${escapeHtml(t('modeParameters'))}</h4><dl class="settlement-list">${mode.parameters.map(renderMiningParameter).join('')}</dl></article>`;
}

async function renderConnect(route) {
  const panel = document.getElementById('coin-panel');
  const coin = route.coin;
  const endpointIndex = state.connectEndpoint.get(coin.id) ?? Math.max(0, coin.stratum.findIndex((endpoint) => endpoint.mode !== 'solo'));
  state.connectEndpoint.set(coin.id, endpointIndex);
  let pool = null;
  let poolError = null;
  try {
    const response = await getPool(state.config, coin, state.controller.signal);
    pool = normalizePoolDetail(response.data);
  } catch (error) {
    if (error.code === 'aborted') return;
    poolError = error;
  }
  if (state.route !== route) return;
  const address = addressFor(coin);
  const remember = rememberFor(coin);
  panel.innerHTML = `
    <ol class="connect-steps"><li><span>1</span><div><h2>${escapeHtml(t('connectStep1'))}</h2><a href="${escapeAttribute(coin.links.site)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t('officialSite'))} ↗</a></div></li><li><span>2</span><div><h2>${escapeHtml(t('connectStep2'))}</h2><a href="/download" data-route>${escapeHtml(t('downloadMiner'))}</a></div></li><li><span>3</span><div><h2>${escapeHtml(t('connectStep3'))}</h2></div></li></ol>
    <section class="form-card connect-form" aria-labelledby="connect-title"><div><p class="section-kicker">STRATUM</p><h2 id="connect-title">${escapeHtml(t('connectTitle'))}</h2></div><div class="form-grid"><label for="endpoint-select">${escapeHtml(t('endpoint'))}</label><select id="endpoint-select">${coin.stratum.map((endpoint, index) => `<option value="${index}" ${index === endpointIndex ? 'selected' : ''}>${escapeHtml(`${endpoint.region} · ${endpoint.host}:${endpoint.port} · ${localize(endpoint.label)} · ${endpoint.tls ? t('tls') : t('noTls')}`)}</option>`).join('')}</select><label for="connect-address">${escapeHtml(miningIdentityLabel(coin))}</label><input id="connect-address" type="text" maxlength="256" autocomplete="off" value="${escapeAttribute(address)}" placeholder="${escapeAttribute(miningIdentityPlaceholder(coin))}"><label class="check-row"><input id="connect-remember" type="checkbox" ${remember ? 'checked' : ''}><span>${escapeHtml(t('rememberAddress'))}</span></label>${coin.settlement.noPayout ? `<label class="check-row critical-check"><input id="no-payout-ack" type="checkbox" ${noPayoutAcknowledged(coin) ? 'checked' : ''}><span>${escapeHtml(t('noPayoutAck'))}</span></label>` : ''}</div><div id="connect-warning" class="form-live" aria-live="polite"></div></section>
    <div id="connect-commands"></div>
    <section class="settlement-card"><p class="section-kicker">POOL RULES</p><h2>${escapeHtml(t('settlement'))}</h2>${renderSettlement(coin, pool, poolError)}</section>`;
  const update = () => updateConnectCommands(route);
  document.getElementById('endpoint-select').addEventListener('change', (event) => { state.connectEndpoint.set(coin.id, Number(event.target.value)); update(); });
  const input = document.getElementById('connect-address');
  const rememberInput = document.getElementById('connect-remember');
  input.addEventListener('input', () => { state.addresses.set(coin.id, isValidMiningIdentity(coin, input.value) ? input.value.trim() : ''); update(); });
  input.addEventListener('change', () => saveAddress(coin, input.value.trim(), rememberInput.checked));
  rememberInput.addEventListener('change', () => saveAddress(coin, input.value.trim(), rememberInput.checked));
  document.getElementById('no-payout-ack')?.addEventListener('change', (event) => {
    try {
      if (event.target.checked) sessionStorage.setItem(`ntm_no_payout_ack_${coin.id}`, 'true');
      else sessionStorage.removeItem(`ntm_no_payout_ack_${coin.id}`);
    } catch (_) { /* no-op */ }
    update();
  });
  update();
}

function updateConnectCommands(route) {
  const coin = route.coin;
  const endpoint = coin.stratum[state.connectEndpoint.get(coin.id) ?? 0];
  const addressInput = document.getElementById('connect-address');
  const rawAddress = addressInput.value.trim();
  const realAddress = rawAddress.length > 0;
  const validAddress = !realAddress || isValidMiningIdentity(coin, rawAddress);
  const acknowledged = !coin.settlement.noPayout || noPayoutAcknowledged(coin);
  const blocked = !validAddress || (coin.settlement.noPayout && realAddress && !acknowledged);
  const address = realAddress && validAddress && acknowledged ? rawAddress : commandPlaceholderFor(coin);
  const warning = document.getElementById('connect-warning');
  warning.textContent = !validAddress ? t('invalidMiningId') : blocked ? t('noPayoutAckNeeded') : endpoint.mode === 'solo' ? t('soloWarning') : '';
  document.getElementById('connect-commands').innerHTML = `${endpoint.mode === 'solo' ? `<div class="solo-warning">${statusBadge('stale', 'SOLO')}<span>${escapeHtml(t('soloWarning'))}</span></div>` : ''}<section aria-labelledby="mining-modes-title"><div class="section-heading"><div><p class="section-kicker">CPU / GPU</p><h2 id="mining-modes-title">${escapeHtml(t('miningModesTitle'))}</h2><p>${escapeHtml(t('miningModesIntro'))}</p></div></div>${coin.mining.modes.map((mode) => renderMiningMode(coin, endpoint, mode, address, blocked)).join('')}</section>`;
  wireCopyButtons(document.getElementById('connect-commands'));
}

function renderSettlement(coin, pool, error) {
  if (coin.settlement.noPayout) return `<div class="critical-copy">${escapeHtml(localize(coin.settlement.notice))}</div>`;
  if (!pool) return `<div class="state-box state-error"><span aria-hidden="true">⚠</span><p>${escapeHtml(t('settlementUnavailable'))}</p></div>`;
  const rows = [];
  if (hasValue(pool.paymentProcessing?.payoutScheme)) rows.push([t('payoutScheme'), String(pool.paymentProcessing.payoutScheme)]);
  if (hasValue(pool.poolFeePercent)) rows.push([t('poolFee'), formatPercent(pool.poolFeePercent)]);
  if (hasValue(pool.soloFeePercent)) rows.push([t('soloFee'), formatPercent(pool.soloFeePercent)]);
  if (hasValue(pool.paymentProcessing?.minimumPayment)) rows.push([coin.settlement.directPayout ? t('dustThreshold') : t('minimumPayment'), formatAmount(pool.paymentProcessing.minimumPayment, coin)]);
  rows.push([t('confirmations'), formatInteger(coin.settlement.confirmations)]);
  return `${coin.settlement.payoutPaused ? `<p class="payout-paused-copy">${escapeHtml(localize(coin.account?.notice))}</p>` : ''}${coin.settlement.directPayout ? `<p class="info-copy">${escapeHtml(t('directPayout'))}</p>` : ''}<dl class="settlement-list">${rows.map(([label, value]) => `<div><dt>${escapeHtml(label)}</dt><dd>${escapeHtml(value)}</dd></div>`).join('')}</dl>`;
}

function renderNotFound() {
  main.innerHTML = `<section class="not-found container"><img src="${escapeAttribute(state.config.brand.assets.mark)}" alt="" width="240" height="180"><p class="eyebrow">404</p><h1>${escapeHtml(t('pageNotFound'))}</h1><p>${escapeHtml(t('pageNotFoundBody'))}</p><div><a class="button primary" href="/" data-route>${escapeHtml(t('backHome'))}</a><a class="button secondary" href="/#pools" data-route>${escapeHtml(t('choosePool'))}</a></div></section>`;
}

function bindGlobalEvents() {
  document.addEventListener('click', (event) => {
    const link = event.target.closest('a[data-route]');
    if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const url = new URL(link.href, window.location.href);
    if (url.origin !== window.location.origin) return;
    event.preventDefault();
    navigate(`${url.pathname}${url.search}${url.hash}`);
  });
  window.addEventListener('popstate', () => queueRender());
  window.addEventListener('hashchange', () => queueRender());
  window.addEventListener('ntm:language', () => queueRender({ preserveScroll: true }));
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) { window.clearInterval(state.timer); state.timer = null; }
    else if (state.route?.refreshVisible) { state.route.refreshVisible(); setAutoRefresh(state.route.refreshVisible); }
  });
}

function fatalConfig(errors) {
  console.error('Config validation failed', errors);
  main.innerHTML = `<section class="not-found container"><p class="eyebrow">CONFIG</p><h1>${escapeHtml(t('configError'))}</h1><pre>${escapeHtml(errors.join('\n'))}</pre></section>`;
}

function boot() {
  try { cleanupDOMStorage(globalThis.localStorage, globalThis.sessionStorage); } catch (_) { /* no-op */ }
  if (!window.NTM_CONFIG) { fatalConfig(['window.NTM_CONFIG is missing']); return; }
  state.config = window.NTM_CONFIG;
  const errors = validateRuntimeConfig(state.config);
  if (errors.length) { fatalConfig(errors); return; }
  bindGlobalEvents();
  renderRoute();
}

function waitForConfig(attempt = 0) {
  if (window.NTM_CONFIG) { boot(); return; }
  if (attempt >= 20) { fatalConfig(['window.NTM_CONFIG is missing']); return; }
  window.setTimeout(() => waitForConfig(attempt + 1), 25);
}

waitForConfig();
