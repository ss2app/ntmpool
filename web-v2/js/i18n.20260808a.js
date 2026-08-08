const LANG_KEY = 'ntm_lang';

const messages = {
  zh: {
    skip: '跳到主要内容',
    navHome: '首页', navDownload: '下载', discord: 'Discord', menu: '菜单', closeMenu: '关闭菜单',
    switchLanguage: '英文', downloadMiner: '下载 NTMminer', startMining: '开始挖矿',
    loading: '正在加载真实数据…', retry: '重试', refresh: '刷新', viewPool: '查看矿池', external: '外部链接',
    heroEyebrow: 'NEXT-TIER MINING', promotion: '推广',
    overview: '矿池概览', onlinePools: '在线池数', minerConnections: '在线矿工连接数', totalBlocks: '累计爆块',
    partialUnavailable: '部分数据不可用', choosePool: '选择矿池', why: '为什么选择 NTMminer Pools',
    benefitFeeTitle: '0% 开发者费', benefitFeeBody: 'NTMminer 当前开发者费为 0%。',
    benefitApiTitle: '自研矿池与公共 API', benefitApiBody: '同源公共 API 提供真实、可追溯的矿池数据。',
    benefitTransparentTitle: '费率与结算透明', benefitTransparentBody: '每个池的结算方式和矿池费率以 API 实际返回为准。',
    statusOnline: '在线', statusOffline: '离线', statusUnavailable: '数据暂不可用', statusStale: '数据可能已过期', statusNoPayout: '不打款', statusUnknown: '未知', payoutPausedTitle: '领取安全暂停', viewGuide: '查看账户与领取指南',
    lastUpdated: '最后更新', noData: '暂无数据', notEnoughHistory: '暂无足够历史数据',
    poolHashrate: '池算力', networkHashrate: '全网算力', poolShare: '池占比', onlineMiners: '在线矿工', onlineWorkers: '在线矿机',
    blockHeight: '区块高度', networkDifficulty: '网络难度', confirmedBlocks: '确认块数', orphanedBlocks: '孤块数',
    totalPaid: '总支付', poolFee: '池费率', soloFee: 'SOLO 费率', payoutScheme: '结算方式', minimumPayment: '起付额', dustThreshold: '尘埃阈值',
    hashrateHistory: '算力历史', recentBlocks: '近期爆块', coinAbout: '币种介绍', chartSummary: '图表文本摘要',
    tabDashboard: '仪表板', tabBlocks: '区块', tabPayments: '支付', tabMiners: '矿工', tabLookup: '我的矿机', tabAccount: '账户与领取', tabConnect: '接入',
    officialSite: '官网', sourceCode: '源码', pageNotFound: '页面不存在', pageNotFoundBody: '该路径不是已注册的页面或矿池。', backHome: '返回首页',
    blocksCaption: '爆块历史', paymentsCaption: '打款历史', minersCaption: '当前矿工榜',
    rank: '排名', status: '状态', confirmations: '确认进度', reward: '奖励', miner: '矿工', worker: 'worker', time: '时间', hash: '区块 hash', solo: 'SOLO',
    address: '地址', amount: '金额', transaction: 'txid', hashrate: '算力', sharesPerSecond: 'shares/s',
    previous: '上一页', next: '下一页', page: '第 {page} 页', pageOf: '第 {page} / {pages} 页',
    copy: '复制', copied: '已复制', copyFailed: '复制失败，已选择完整文本，请手动复制。',
    downloadTitle: '下载 NTMminer', downloadSubtitle: '闭源 · 只发布二进制 · 0% 开发者费', downloads: '平台下载', ready: '可下载', building: '构建中', downloadFile: '下载文件',
    sha256: 'SHA-256', verify: '校验下载', verifyBody: '下载后请在本地核对 SHA-256，再运行二进制。',
    quickStart: '快速开始', quick1Title: '准备钱包地址', quick1Body: '从币种官网创建或准备收款地址。', quick2Title: '下载对应二进制', quick2Body: '选择 Windows 或 Linux / HiveOS 版本并校验 SHA-256。', quick3Title: '复制接入命令', quick3Body: '从逐币速查复制命令，替换钱包地址并按需填写 worker。',
    coinCommands: '逐币命令速查', windows: 'Windows', linux: 'Linux / HiveOS', nativeMultiGpu: '原生多显卡', xmrigCompatible: 'xmrig 兼容',
    lookupTitle: '查询我的矿机', walletAddress: '钱包地址', walletPlaceholder: '输入完整钱包地址', publicMiningId: '公开 mining ID', publicMiningIdPlaceholder: 'domh_ + 64 位小写十六进制', publicMiningIdCommandPlaceholder: 'YOUR_DOMH_MINING_ID', invalidMiningId: 'DOM 挖矿只接受 domh_ + 64 位小写十六进制。不要填写 domw_、domp_、钱包恢复串或 Slate。', rememberAddress: '记住地址', query: '查询', clear: '清除',
    lookupPrivacy: '地址只会发送到该币种的同源矿池 API。', currentHashrate: '当前算力', pendingBalance: '待支付', lastPayment: '上次支付', onlineWorker: '在线 worker', offlineWorker: '离线 worker', recentPayments: '近期支付',
    payout7dTitle: '近 7 日到账', payout7dTotal: '7 日合计', payout7dEmpty: '近 7 日暂无到账', payout7dCount: '当日 {count} 笔', payout7dDayBoundary: '按浏览器本地时区切日',
    offlineWorkerDefinition: '离线 worker 指 24 小时历史样本内出现过、但当前 performance.workers 中不存在的 worker；这不等同于硬件故障。',
    connectTitle: '接入矿池', connectStep1: '准备钱包地址', connectStep2: '下载当前 NTMminer', connectStep3: '选择接入点并复制命令',
    endpoint: '接入点', region: '区域', poolMode: '普通池', tls: 'TLS', noTls: '非 TLS', commandPlaceholder: 'YOUR_WALLET_ADDRESS',
    settlement: '结算说明', settlementUnavailable: '动态结算数据暂不可用；静态接入信息仍可使用。',
    soloWarning: 'SOLO：奖励扣除 API 返回费率后归爆块者；收益波动显著。',
    miningModesTitle: '按挖矿模式接入', miningModesIntro: '只显示该币在 NTMminer v1.19.0 中真实支持的模式；选择接入点或填写地址后，下面所有命令会同步更新。',
    modeCpuTitle: '纯 CPU 挖矿', modeGpuTitle: '纯 GPU 挖矿', modeHybridTitle: 'CPU + GPU 同挖',
    modeCpuKicker: 'CPU 模式', modeGpuKicker: 'GPU 模式', modeHybridKicker: '同挖模式',
    modeParameters: '本模式相关参数', selectedGpuExample: '指定多卡示例', copyParameter: '复制参数',
    defaultValue: '默认值', scope: '适用范围', notStatedInHelp: 'help 未注明', noScopeInHelp: 'help 未注明限制',
    noPayoutAck: '我已知晓本池不打款，所有爆块收益归承包方。', noPayoutAckNeeded: '生成或复制包含真实地址的命令前，必须先确认不打款风险。',
    directPayout: '该池使用 coinbase 直付；支付为空时请查看区块。',
    error400: '地址格式无法查询。', error404: '未找到该矿工。', error429: '请求过于频繁，请稍后重试。', error5xx: '服务暂不可用。', errorTimeout: '请求超时，请稍后重试。', errorNetwork: '网络请求失败。',
    emptyBlocks: '暂无爆块记录', emptyPayments: '暂无打款记录', emptyMiners: '暂无矿工记录',
    footerRisk: '挖矿涉及硬件、电力与网络风险。请核对币种、地址、费率及矿池状态；不打款池不会向矿工分配收益。',
    allRights: '保留所有权利。', staleAt: '数据可能已过期，最后成功更新于 {time}。',
    unknownValue: '未知：{value}', confirmed: '已确认', pending: '待确认', orphaned: '孤块', paid: '已支付', processing: '处理中', created: '已创建', sent: '已发送', confirming: '确认中', failed: '发送失败', voided: '已作废',
    domCandidateRewardIncomplete: '候选提交未完成，不计入奖励',
    justNow: '刚刚', minutesAgo: '{value} 分钟前', hoursAgo: '{value} 小时前', daysAgo: '{value} 天前',
    configError: '站点配置无效，无法启动。', noPayoutBlockNote: '本池不打款；链上区块奖励归承包方。',
  },
  en: {
    skip: 'Skip to content',
    navHome: 'Home', navDownload: 'Download', discord: 'Discord', menu: 'Menu', closeMenu: 'Close menu',
    switchLanguage: '中文', downloadMiner: 'Download NTMminer', startMining: 'Start mining',
    loading: 'Loading live data…', retry: 'Retry', refresh: 'Refresh', viewPool: 'View pool', external: 'External link',
    heroEyebrow: 'NEXT-TIER MINING', promotion: 'Promotion',
    overview: 'Pool overview', onlinePools: 'Online pools', minerConnections: 'Miner connections', totalBlocks: 'Blocks found',
    partialUnavailable: 'Some data is unavailable', choosePool: 'Choose a pool', why: 'Why NTMminer Pools',
    benefitFeeTitle: '0% developer fee', benefitFeeBody: 'NTMminer currently charges a 0% developer fee.',
    benefitApiTitle: 'In-house pools and public API', benefitApiBody: 'Same-origin public APIs provide real, traceable pool data.',
    benefitTransparentTitle: 'Transparent fees and settlement', benefitTransparentBody: 'Each pool’s scheme and fee are shown from its current API response.',
    statusOnline: 'Online', statusOffline: 'Offline', statusUnavailable: 'Data unavailable', statusStale: 'Data may be stale', statusNoPayout: 'No payouts', statusUnknown: 'Unknown', payoutPausedTitle: 'Claims safely paused', viewGuide: 'View account and claim guide',
    lastUpdated: 'Last updated', noData: 'No data', notEnoughHistory: 'Not enough history yet',
    poolHashrate: 'Pool hashrate', networkHashrate: 'Network hashrate', poolShare: 'Pool share', onlineMiners: 'Online miners', onlineWorkers: 'Online rigs',
    blockHeight: 'Block height', networkDifficulty: 'Network difficulty', confirmedBlocks: 'Confirmed blocks', orphanedBlocks: 'Orphaned blocks',
    totalPaid: 'Total paid', poolFee: 'Pool fee', soloFee: 'SOLO fee', payoutScheme: 'Payout scheme', minimumPayment: 'Minimum payment', dustThreshold: 'Dust threshold',
    hashrateHistory: 'Hashrate history', recentBlocks: 'Recent blocks', coinAbout: 'About this coin', chartSummary: 'Chart text summary',
    tabDashboard: 'Dashboard', tabBlocks: 'Blocks', tabPayments: 'Payments', tabMiners: 'Miners', tabLookup: 'My rigs', tabAccount: 'Account & claims', tabConnect: 'Connect',
    officialSite: 'Website', sourceCode: 'Source', pageNotFound: 'Page not found', pageNotFoundBody: 'This path is not a registered page or pool.', backHome: 'Back home',
    blocksCaption: 'Block history', paymentsCaption: 'Payment history', minersCaption: 'Current miner ranking',
    rank: 'Rank', status: 'Status', confirmations: 'Confirmations', reward: 'Reward', miner: 'Miner', worker: 'worker', time: 'Time', hash: 'block hash', solo: 'SOLO',
    address: 'Address', amount: 'Amount', transaction: 'txid', hashrate: 'Hashrate', sharesPerSecond: 'shares/s',
    previous: 'Previous', next: 'Next', page: 'Page {page}', pageOf: 'Page {page} of {pages}',
    copy: 'Copy', copied: 'Copied', copyFailed: 'Copy failed. The full text is selected; copy it manually.',
    downloadTitle: 'Download NTMminer', downloadSubtitle: 'Closed source · binaries only · 0% developer fee', downloads: 'Platform downloads', ready: 'Ready', building: 'Building', downloadFile: 'Download file',
    sha256: 'SHA-256', verify: 'Verify the download', verifyBody: 'Check SHA-256 locally before running the binary.',
    quickStart: 'Quick Start', quick1Title: 'Prepare a wallet address', quick1Body: 'Create or prepare a payout address from the coin’s official site.', quick2Title: 'Download the binary', quick2Body: 'Choose Windows or Linux / HiveOS and verify SHA-256.', quick3Title: 'Copy a connection command', quick3Body: 'Copy a coin command, replace the wallet address, and add a worker if needed.',
    coinCommands: 'Commands by coin', windows: 'Windows', linux: 'Linux / HiveOS', nativeMultiGpu: 'Native multi-GPU', xmrigCompatible: 'xmrig compatible',
    lookupTitle: 'Look up my rigs', walletAddress: 'Wallet address', walletPlaceholder: 'Enter the complete wallet address', publicMiningId: 'Public mining ID', publicMiningIdPlaceholder: 'domh_ + 64 lowercase hex characters', publicMiningIdCommandPlaceholder: 'YOUR_DOMH_MINING_ID', invalidMiningId: 'DOM mining accepts only domh_ plus 64 lowercase hex characters. Do not enter domw_, domp_, wallet recovery strings, or Slates.', rememberAddress: 'Remember address', query: 'Look up', clear: 'Clear',
    lookupPrivacy: 'The address is sent only to this coin’s same-origin pool API.', currentHashrate: 'Current hashrate', pendingBalance: 'Pending balance', lastPayment: 'Last payment', onlineWorker: 'Online worker', offlineWorker: 'Offline worker', recentPayments: 'Recent payments',
    payout7dTitle: 'Payouts · last 7 days', payout7dTotal: '7-day total', payout7dEmpty: 'No payouts in the last 7 days', payout7dCount: '{count} payout(s) that day', payout7dDayBoundary: 'Days split by your browser’s local timezone',
    offlineWorkerDefinition: 'An offline worker appeared in the past 24 hours of samples but is absent from current performance.workers; this does not prove a hardware failure.',
    connectTitle: 'Connect to the pool', connectStep1: 'Prepare a wallet address', connectStep2: 'Download the current NTMminer', connectStep3: 'Choose an endpoint and copy a command',
    endpoint: 'Endpoint', region: 'Region', poolMode: 'Pool', tls: 'TLS', noTls: 'No TLS', commandPlaceholder: 'YOUR_WALLET_ADDRESS',
    settlement: 'Settlement', settlementUnavailable: 'Dynamic settlement data is unavailable; static connection details remain usable.',
    soloWarning: 'SOLO: the block finder receives the reward after the API-reported fee; variance is significant.',
    miningModesTitle: 'Connect by mining mode', miningModesIntro: 'Only modes actually supported for this coin by NTMminer v1.19.0 are shown. Every command below updates when you change the endpoint or wallet address.',
    modeCpuTitle: 'CPU-only mining', modeGpuTitle: 'GPU-only mining', modeHybridTitle: 'Simultaneous CPU + GPU mining',
    modeCpuKicker: 'CPU mode', modeGpuKicker: 'GPU mode', modeHybridKicker: 'Hybrid mode',
    modeParameters: 'Parameters for this mode', selectedGpuExample: 'Selected multi-GPU example', copyParameter: 'Copy option',
    defaultValue: 'Default', scope: 'Scope', notStatedInHelp: 'Not stated in help', noScopeInHelp: 'No restriction stated in help',
    noPayoutAck: 'I understand that this pool pays nothing and all block rewards go to the sponsor.', noPayoutAckNeeded: 'Confirm the no-payout risk before generating or copying a command with a real address.',
    directPayout: 'This pool pays coinbase directly; when Payments is empty, see Blocks.',
    error400: 'The address format cannot be queried.', error404: 'Miner not found.', error429: 'Too many requests. Try again later.', error5xx: 'The service is temporarily unavailable.', errorTimeout: 'The request timed out. Try again later.', errorNetwork: 'The network request failed.',
    emptyBlocks: 'No blocks found', emptyPayments: 'No payments found', emptyMiners: 'No miners found',
    footerRisk: 'Mining involves hardware, power, and network risk. Verify the coin, address, fees, and pool state; no-payout pools do not distribute rewards to miners.',
    allRights: 'All rights reserved.', staleAt: 'Data may be stale. Last successful update: {time}.',
    unknownValue: 'Unknown: {value}', confirmed: 'Confirmed', pending: 'Pending', orphaned: 'Orphaned', paid: 'Paid', processing: 'Processing', created: 'Created', sent: 'Sent', confirming: 'Confirming', failed: 'Failed', voided: 'Voided',
    domCandidateRewardIncomplete: 'Candidate submission incomplete; not counted as a reward',
    justNow: 'just now', minutesAgo: '{value} minutes ago', hoursAgo: '{value} hours ago', daysAgo: '{value} days ago',
    configError: 'The site configuration is invalid and the app cannot start.', noPayoutBlockNote: 'This pool pays nothing; on-chain block rewards go to the sponsor.',
  },
};

let language = readInitialLanguage();

function readInitialLanguage() {
  try {
    const saved = localStorage.getItem(LANG_KEY);
    if (saved === 'zh' || saved === 'en') return saved;
  } catch (_) {
    // Storage may be unavailable; browser language remains a safe fallback.
  }
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

export function getLanguage() {
  return language;
}

export function getLocale() {
  return language === 'zh' ? 'zh-CN' : 'en-US';
}

export function setLanguage(next) {
  if (next !== 'zh' && next !== 'en') return;
  language = next;
  try { localStorage.setItem(LANG_KEY, next); } catch (_) { /* no-op */ }
  document.documentElement.lang = next === 'zh' ? 'zh-CN' : 'en';
  window.dispatchEvent(new CustomEvent('ntm:language', { detail: { language: next } }));
}

export function t(key, variables = {}) {
  const value = messages[language][key] ?? messages.en[key] ?? key;
  return String(value).replace(/\{(\w+)\}/g, (_, name) => String(variables[name] ?? `{${name}}`));
}

export function localize(value) {
  if (!value || typeof value !== 'object') return '';
  return value[language] || value.en || value.zh || '';
}

export function translateStatus(value) {
  const normalized = String(value ?? '').toLowerCase();
  const keys = {
    confirmed: 'confirmed', paid: 'paid', pending: 'pending', processing: 'processing',
    orphaned: 'orphaned', orphan: 'orphaned', immature: 'pending', unlocked: 'confirmed',
    created: 'created', sent: 'sent', confirming: 'confirming', failed: 'failed', voided: 'voided',
  };
  const key = keys[normalized];
  return key ? { text: t(key), known: true, kind: key } : { text: t('unknownValue', { value: String(value ?? '') }), known: false, kind: 'unknown' };
}

export function formatRelative(date) {
  const milliseconds = Date.now() - date.getTime();
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return '';
  const minutes = Math.floor(milliseconds / 60000);
  if (minutes < 1) return t('justNow');
  if (minutes < 60) return t('minutesAgo', { value: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t('hoursAgo', { value: hours });
  return t('daysAgo', { value: Math.floor(hours / 24) });
}

document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
