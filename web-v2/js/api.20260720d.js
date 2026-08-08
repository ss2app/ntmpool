const resourceCache = new Map();
const activeRequests = new Map();

export class ApiError extends Error {
  constructor(message, options = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = options.status ?? null;
    this.code = options.code ?? 'network';
    this.url = options.url ?? '';
  }
}

function isRootPath(value) {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\?#]/.test(value)) return false;
  try {
    const parsed = new URL(value, 'https://www.ntmminer.com');
    return parsed.origin === 'https://www.ntmminer.com' && parsed.pathname === value;
  } catch (_) {
    return false;
  }
}

function isLocalized(value) {
  return value && typeof value === 'object' && typeof value.zh === 'string' && value.zh.trim() && typeof value.en === 'string' && value.en.trim();
}

const RUNTIME_MODE_SPECS = {
  cpu: { flag: null, required: ['threads'], parameters: new Set(['threads', 'smt', 'hugePages', 'noHugePages', 'msr']) },
  gpu: { flag: '--gpu-only', required: ['gpuOnly', 'gpuDevices'], parameters: new Set(['gpuOnly', 'gpuDevices']) },
  hybrid: { flag: '--gpu', required: ['gpu', 'gpuDevices'], parameters: new Set(['gpu', 'gpuDevices']) },
};

export function validateRuntimeConfig(config) {
  const errors = [];
  if (!config || typeof config !== 'object') return ['config must be an object'];
  if (config.schemaVersion !== 1) errors.push('unsupported schemaVersion');
  if (!config.brand || !config.api || !config.downloads || !config.coins) errors.push('missing root sections');
  if (config.api && (!isRootPath(config.api.base) || !Number.isInteger(config.api.refreshMs))) errors.push('invalid api config');
  const ids = new Set();
  for (const [key, coin] of Object.entries(config.coins || {})) {
    if (!coin || coin.id !== key || ids.has(coin.id)) errors.push(`invalid or duplicate coin id: ${key}`);
    ids.add(coin && coin.id);
    if (!coin || !coin.enabled) continue;
    if (!coin.name || !coin.symbol || !coin.algo || !coin.mining?.ntmAlgoFlag) errors.push(`missing required coin field: ${key}`);
    if (!Array.isArray(coin.stratum) || !coin.stratum.length) errors.push(`missing endpoints: ${key}`);
    if (!isLocalized(coin.description) || !isLocalized(coin.wallet?.example)) errors.push(`missing localized text: ${key}`);
    if (coin.settlement?.noPayout && !isLocalized(coin.settlement.notice)) errors.push(`missing noPayout notice: ${key}`);
    if (coin.settlement?.noPayout && coin.settlement?.directPayout) errors.push(`conflicting settlement: ${key}`);
    if (coin.settlement?.payoutPaused !== undefined && typeof coin.settlement.payoutPaused !== 'boolean') errors.push(`invalid payoutPaused flag: ${key}`);
    if (coin.settlement?.noPayout && coin.settlement?.payoutPaused) errors.push(`conflicting payout state: ${key}`);
    if (coin.account !== undefined) {
      const account = coin.account;
      if (key !== 'dom' || !account || account.type !== 'dom-slate-v4' || typeof account.enabled !== 'boolean'
          || !isRootPath(account.apiBase) || !isRootPath(account.tutorialImage)
          || typeof account.registrationEnabled !== 'boolean'
          || typeof account.rotationEnabled !== 'boolean'
          || typeof account.claimsEnabled !== 'boolean'
          || typeof account.paymentHistoryEnabled !== 'boolean'
          || !isLocalized(account.notice)) errors.push(`invalid account config: ${key}`);
    }
    if (Object.prototype.hasOwnProperty.call(coin.mining || {}, 'gpuMulti')) errors.push(`obsolete gpuMulti field: ${key}`);
    if (typeof coin.mining?.xmrigCompatible !== 'boolean') errors.push(`invalid xmrig capability: ${key}`);
    if (coin.mining?.xmrigCompatible && !coin.mining.xmrigAlgoFlag) errors.push(`missing xmrig flag: ${key}`);
    if (!coin.mining?.xmrigCompatible && coin.mining?.xmrigAlgoFlag !== null) errors.push(`unexpected xmrig flag: ${key}`);
    if (!Array.isArray(coin.mining?.modes) || !coin.mining.modes.length) errors.push(`missing mining modes: ${key}`);
    const modeIds = new Set();
    for (const mode of coin.mining?.modes || []) {
      const spec = RUNTIME_MODE_SPECS[mode?.id];
      if (!spec || modeIds.has(mode.id) || mode.commandFlag !== spec.flag || !isLocalized(mode.description)
          || !Array.isArray(mode.parameters) || !mode.parameters.length
          || new Set(mode.parameters).size !== mode.parameters.length
          || mode.parameters.some((parameter) => !spec.parameters.has(parameter))
          || spec.required.some((parameter) => !mode.parameters.includes(parameter))) errors.push(`invalid mining mode: ${key}`);
      modeIds.add(mode?.id);
    }
    if (coin.logo !== null && !isRootPath(coin.logo)) errors.push(`invalid logo path: ${key}`);
    if (coin.apiBase !== null && !isRootPath(coin.apiBase)) errors.push(`invalid API base: ${key}`);
  }
  return errors;
}

export function apiBaseFor(config, coin) {
  return coin.apiBase || config.api.base;
}

function combineSignal(externalSignal, timeoutMs) {
  const controller = new AbortController();
  let timedOut = false;
  const timer = window.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  if (externalSignal) {
    if (externalSignal.aborted) controller.abort();
    else externalSignal.addEventListener('abort', () => controller.abort(), { once: true });
  }
  return { signal: controller.signal, cleanup: () => window.clearTimeout(timer), didTimeout: () => timedOut };
}

export async function fetchJson(config, base, path, options = {}) {
  const url = `${base}${path}`;
  const requestKey = options.requestKey || url;
  if (activeRequests.has(requestKey)) return activeRequests.get(requestKey);
  const task = (async () => {
    const combined = combineSignal(options.signal, config.api.timeoutMs);
    try {
      const response = await fetch(url, {
        method: 'GET',
        headers: { Accept: 'application/json' },
        credentials: 'same-origin',
        signal: combined.signal,
      });
      if (!response.ok) throw new ApiError(`HTTP ${response.status}`, { status: response.status, code: 'http', url });
      let data;
      try { data = await response.json(); }
      catch (error) { throw new ApiError('Invalid JSON', { code: 'json', url }); }
      const rawTotal = response.headers.get('X-Total-Count');
      const parsedTotal = rawTotal === null ? null : Number.parseInt(rawTotal, 10);
      return { data, total: Number.isFinite(parsedTotal) ? parsedTotal : null, receivedAt: new Date() };
    } catch (error) {
      if (error instanceof ApiError) throw error;
      if (combined.didTimeout()) throw new ApiError('Request timeout', { code: 'timeout', url });
      if (options.signal?.aborted) throw new ApiError('Request aborted', { code: 'aborted', url });
      throw new ApiError('Network request failed', { code: 'network', url });
    } finally {
      combined.cleanup();
    }
  })();
  activeRequests.set(requestKey, task);
  try { return await task; }
  finally { activeRequests.delete(requestKey); }
}

export async function withSessionStale(cacheKey, loader) {
  try {
    const result = await loader();
    const record = { ...result, stale: false, lastSuccess: result.receivedAt || new Date() };
    resourceCache.set(cacheKey, record);
    return record;
  } catch (error) {
    if (error.code === 'aborted') throw error;
    const previous = resourceCache.get(cacheKey);
    if (previous) return { ...previous, stale: true, error };
    throw error;
  }
}

export function clearResourceCache() {
  resourceCache.clear();
}

export function normalizePools(payload) {
  return payload && Array.isArray(payload.pools) ? payload.pools : [];
}

export function normalizePoolDetail(payload) {
  if (payload && payload.pool && typeof payload.pool === 'object') return payload.pool;
  if (payload && typeof payload === 'object' && payload.id) return payload;
  return null;
}

export function normalizePerformance(payload) {
  return payload && Array.isArray(payload.stats) ? payload.stats : [];
}

export function normalizeList(payload) {
  return Array.isArray(payload) ? payload : [];
}

export function getPools(config, base, signal) {
  return fetchJson(config, base, '/pools', { signal, requestKey: `pools:${base}` });
}

export function getPool(config, coin, signal) {
  const base = apiBaseFor(config, coin);
  return fetchJson(config, base, `/pools/${encodeURIComponent(coin.id)}`, { signal, requestKey: `pool:${base}:${coin.id}` });
}

export function getPerformance(config, coin, signal) {
  const base = apiBaseFor(config, coin);
  return fetchJson(config, base, `/pools/${encodeURIComponent(coin.id)}/performance`, { signal, requestKey: `performance:${base}:${coin.id}` });
}

export function getBlocks(config, coin, page, pageSize, signal) {
  const base = apiBaseFor(config, coin);
  const path = `/pools/${encodeURIComponent(coin.id)}/blocks?page=${page}&pageSize=${pageSize}`;
  return fetchJson(config, base, path, { signal, requestKey: `blocks:${base}:${coin.id}:${page}:${pageSize}` });
}

export function getPayments(config, coin, page, pageSize, signal) {
  const base = apiBaseFor(config, coin);
  const path = `/pools/${encodeURIComponent(coin.id)}/payments?page=${page}&pageSize=${pageSize}`;
  return fetchJson(config, base, path, { signal, requestKey: `payments:${base}:${coin.id}:${page}:${pageSize}` });
}

export function getMiners(config, coin, signal) {
  const base = apiBaseFor(config, coin);
  return fetchJson(config, base, `/pools/${encodeURIComponent(coin.id)}/miners`, { signal, requestKey: `miners:${base}:${coin.id}` });
}

export function getMiner(config, coin, address, signal) {
  const base = apiBaseFor(config, coin);
  const path = `/pools/${encodeURIComponent(coin.id)}/miners/${encodeURIComponent(address)}`;
  return fetchJson(config, base, path, { signal, requestKey: `miner:${base}:${coin.id}:${address}` });
}
