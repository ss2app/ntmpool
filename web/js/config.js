/* NTM pool frontend — 唯一编辑点：品牌 + 币种注册表。
 * 动态数据一律来自 /api（NTMPool 公共 API），此处只放静态元信息。 */
window.NTM_CONFIG = {
  brand: {
    // NTM 英文全称（改这里即可全站生效）
    expansionEn: 'Next-Tier Mining',
    minerVersion: 'v1.16.0',
  },
  // 矿工软件下载（自托管于 www.ntmminer.com/downloads/，闭源只发二进制）。
  // 加平台/换版本改这里；sha256 用 `sha256sum <文件>` 现算后填，方便矿工核验。
  // status: 'ready'=有真实文件可下载；'building'=暂未提供（页面显示「构建中」，不给死链）。
  downloads: {
    version: 'v1.16.0',
    baseUrl: '/downloads',
    files: [
      { os: 'Windows x64', icon: 'win', file: 'NTMminer-windows-x64.exe', status: 'ready',
        sha256: '8c672a4e6d0c8bff2496e7bf0d0a5e615472ad8f9aab8dabd883548a80465ae3',
        noteZh: 'Windows 10/11 64 位。含 CPU + NVIDIA GPU 后端，原生多显卡（--gpu-devices）。本版改为单文件静态构建，无需任何额外 DLL —— 修复旧版因缺少 libwinpthread-1.dll / libstdc++-6.dll 而「无法继续执行代码」的问题。解压即用，无需安装。首次运行若被 Defender 拦截，选「仍要运行」。',
        noteEn: 'Windows 10/11 64-bit. CPU + NVIDIA GPU backends, native multi-GPU (--gpu-devices). This build is now a single static executable requiring no extra DLLs — it fixes the previous “cannot proceed, libwinpthread-1.dll not found” error on clean systems. No install needed. If Defender warns, choose “Run anyway”.' },
      { os: 'Linux x64 / HiveOS', icon: 'linux', file: 'NTMminer-linux-x64', status: 'ready',
        sha256: '1ed3a7028f72f731bcddf74cb498b1594bf0ddd1575c1960eec89b496f52e7ee',
        noteZh: '通用 x86-64 Linux（Ubuntu/Debian/HiveOS 等）。含 CPU + NVIDIA GPU 后端，原生多显卡（--gpu-devices）。下载后 chmod +x 即可运行。',
        noteEn: 'Generic x86-64 Linux (Ubuntu/Debian/HiveOS). CPU + NVIDIA GPU backends, native multi-GPU (--gpu-devices). chmod +x and run.' },
    ],
  },
  api: { base: '/api', refreshMs: 20000 },
  // key = NTMPool 池 id（/api/pools 里的 id）。只有出现在 /api/pools 里的币才会显示为在线。
  coins: {
    dragonx: {
      symbol: 'DRGX',
      name: 'DragonX',
      algo: 'rx/dragonx',
      color: '#e34948',
      logo: '/img/dragonx.png',
      hashUnit: 'H/s',
      // 矿工公网入口（不暴露矿池真实 IP）
      stratum: [
        { host: 'hk.ntmminer.com', port: 3333 },
        { host: 'hk.ntmminer.com', port: 4444 },
        { host: 'hk.ntmminer.com', port: 5555 },
        { host: 'hk.ntmminer.com', port: 7777, mode: 'solo' },
      ],
      ntmAlgoFlag: 'rx/dragonx',
      addressPrefix: 'zs1',
      addressExample: 'zs1qknw...(你的 Sapling zs 地址)',
      xmrigCompatible: true, // drg-xmrig / xmrig 系可直连
      confirmations: 10,
      links: {
        site: 'https://dragonx.is',
        git: 'https://git.dragonx.is/DragonX/dragonx',
      },
      descEn: 'DragonX (DRGX) is a privacy chain in the Hush / Komodo family. PoW is the rx/dragonx RandomX variant — CPU friendly, ASIC resistant. Miners use shielded zs addresses for both login and payouts.',
      descZh: 'DragonX（DRGX）是 Hush / Komodo 系的强隐私链，PoW 为 RandomX 变体 rx/dragonx —— CPU 友好、抗 ASIC。矿工登录与收款均使用 zs 隐私地址。',
    },
    btc09: {
      symbol: '09C',
      name: 'Bitcoin 09',
      algo: 'Argon2id (64 MiB)',
      color: '#f7931a',
      logo: '/img/bitcoin09.png',
      hashUnit: 'H/s',
      // 独立 NTMPool 实例：前端经 /api-btc09 反代（nginx→隧道→池 4410）
      apiBase: '/api-btc09',
      stratum: [
        { host: 'hk.ntmminer.com', port: 8344 },
        { host: 'hk.ntmminer.com', port: 8345, mode: 'solo' },
      ],
      ntmAlgoFlag: 'btc09',
      addressPrefix: '4',
      addressExample: '4k26Vj...(你的 09C 地址)',
      xmrigCompatible: false, // 09C 是 NTMminer 专属线协议
      confirmations: 10,
      links: {
        site: 'https://btc09.org',
        git: 'https://github.com/krutftw/bitcoin09',
      },
      descEn: 'Bitcoin 09 (09C) is a clean-room Go rewrite of Bitcoin with one change: PoW is Argon2id at 64 MiB per hash — memory-hard, CPU-only, ASIC/GPU hostile. 21M cap, 50-coin subsidy, 10-minute blocks, halving every 210,000 blocks.',
      descZh: 'Bitcoin 09（09C）是 Bitcoin 的 clean-room Go 重写，只改一处：PoW 换成 64 MiB Argon2id —— 内存硬、CPU 专属、天然抗 ASIC/GPU。2100 万上限、50 币补贴、10 分钟出块、每 21 万块减半，经济模型与比特币逐条一致。',
    },
    noctari: {
      symbol: 'NCTI',
      name: 'Noctari',
      algo: 'Quark',
      color: '#8b7cf6',
      logo: '/img/noctari.png',
      hashUnit: 'H/s',
      // 独立 NTMPool 实例：前端经 /api-noctari 反代（nginx→隧道→池 4020）
      apiBase: '/api-noctari',
      stratum: [
        { host: 'hk.ntmminer.com', port: 4455 },
        { host: 'hk.ntmminer.com', port: 4456, mode: 'solo' },
      ],
      ntmAlgoFlag: 'quark',
      addressPrefix: 'N',
      addressExample: 'Ngar...(你的 NCTI 地址)',
      xmrigCompatible: false, // Noctari 走 NTMminer 专属 cnjob 线协议
      gpuMulti: true, // Quark GPU 友好：Connect 页出「多显卡挖矿」教程
      confirmations: 100,
      links: {
        site: 'https://github.com/noctari-core/noctari',
        git: 'https://github.com/noctari-core/noctari',
      },
      descEn: 'Noctari (NCTI) is a PIVX v5 fork mined with the Quark algorithm (a 9-round hash chain of blake/bmw/groestl/jh/keccak/skein) — CPU and GPU friendly. Fair launch: blocks 1–20159 are a ~7-day PoW window (50 NCTI per block, entirely to miners, zero premine) after which the chain switches to PoS permanently. This pool settles via PPLNS: block rewards accrue to the pool wallet and are paid out by share weight once coinbase matures at 100 confirmations.',
      descZh: 'Noctari（NCTI）是 PIVX v5 fork，用 Quark 算法（blake/bmw/groestl/jh/keccak/skein 九轮链式哈希）挖矿 —— CPU、GPU 均可。公平启动：区块 1–20159 为约 7 天的 PoW 窗口（50 NCTI/块全归矿工、零 premine），窗口结束后永久转 PoS。本池 PPLNS 结算：爆块奖励先进矿池钱包，coinbase 满 100 确认成熟后按 share 占比打款给矿工。',
    },
  },
};
